// Package service implements the mirror operations behind the CLI
// subcommands. Cloudflare is the single source of truth; the local database
// is brought in line with it.
package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/webzone/cloudflare_dns/internal/cf"
	"github.com/webzone/cloudflare_dns/internal/store"
)

// Result summarizes one sync run.
type Result struct {
	ZonesAdded       int
	ZonesRenamed     int
	ZonesAttached    int
	ZonesReactivated int
	ZonesDisabled    int
	RecordsAdded     int
	RecordsUpdated   int
	RecordsDisabled  int
}

// Sync mirrors Cloudflare state into the local store.
type Sync struct {
	cf     *cf.Client
	st     *store.Store
	log    *slog.Logger
	dryRun bool
}

// NewSync builds a sync runner.
func NewSync(c *cf.Client, s *store.Store, log *slog.Logger, dryRun bool) *Sync {
	return &Sync{cf: c, st: s, log: log, dryRun: dryRun}
}

// Run lists all Cloudflare zones and records, diffs against the mirror, and
// applies (or logs, under dry-run) the changes.
func (s *Sync) Run(ctx context.Context) (*Result, error) {
	cfZones, err := s.cf.ListZones(ctx)
	if err != nil {
		return nil, err
	}
	present := map[string]bool{}
	for _, z := range cfZones {
		present[z.ID] = true
	}

	dbZones, err := s.st.ListZones(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]store.Zone, len(dbZones))
	for _, dz := range dbZones {
		byName[dz.Name] = dz
	}

	res := &Result{}
	for _, z := range cfZones {
		if err := s.syncZone(ctx, z, dbZones, byName, res); err != nil {
			return res, err
		}
	}

	// Disable zones that Cloudflare no longer has.
	for _, dz := range dbZones {
		if dz.Status != store.StatusOn || present[dz.ZoneID] {
			continue
		}
		s.logAction("zone", "disable", fmt.Sprintf("%s (absent from Cloudflare)", dz.Name))
		res.ZonesDisabled++
		if !s.dryRun {
			if err := s.st.SetZoneStatus(ctx, dz.ID, store.StatusOff); err != nil {
				return res, err
			}
		}
	}

	s.log.Info("sync complete",
		"dry_run", s.dryRun,
		"zones", map[string]int{"added": res.ZonesAdded, "renamed": res.ZonesRenamed,
			"attached": res.ZonesAttached, "reactivated": res.ZonesReactivated,
			"disabled": res.ZonesDisabled},
		"records", map[string]int{"added": res.RecordsAdded, "updated": res.RecordsUpdated,
			"disabled": res.RecordsDisabled})
	return res, nil
}

// newZoneDomainID is the dry-run sentinel for a zone that would be created:
// no mirror rows exist yet, so record diffing is skipped for it.
const newZoneDomainID = -1

func (s *Sync) syncZone(ctx context.Context, z cf.Zone, dbZones map[string]store.Zone,
	byName map[string]store.Zone, res *Result) error {
	dz, hasID := dbZones[z.ID]
	var domainID int64
	switch {
	case hasID:
		domainID = dz.ID
		if dz.Name != z.Name {
			s.logAction("zone", "rename", fmt.Sprintf("%s -> %s", dz.Name, z.Name))
			res.ZonesRenamed++
			if !s.dryRun {
				if err := s.st.RenameZone(ctx, dz.ID, z.Name); err != nil {
					return err
				}
			}
		}
		if dz.Status != store.StatusOn {
			s.logAction("zone", "reactivate", z.Name)
			res.ZonesReactivated++
			if !s.dryRun {
				if err := s.st.SetZoneStatus(ctx, dz.ID, store.StatusOn); err != nil {
					return err
				}
			}
		}
		if dz.Registrar != "cloudflare" {
			s.logAction("zone", "registrar", fmt.Sprintf("%s -> cloudflare (DNS managed on Cloudflare)", z.Name))
			if !s.dryRun {
				if err := s.st.SetZoneRegistrar(ctx, dz.ID, "cloudflare"); err != nil {
					return err
				}
			}
		}
	case !hasID && byName[z.Name].ID != 0:
		// Legacy row existed by name without (or with a stale) zone ID.
		nz := byName[z.Name]
		domainID = nz.ID
		s.logAction("zone", "attach", fmt.Sprintf("%s zoneid %s", z.Name, z.ID))
		res.ZonesAttached++
		if !s.dryRun {
			if err := s.st.SetZoneID(ctx, nz.ID, z.ID); err != nil {
				return err
			}
			if err := s.st.SetZoneRegistrar(ctx, nz.ID, "cloudflare"); err != nil {
				return err
			}
		}
	default:
		s.logAction("zone", "add", fmt.Sprintf("%s zoneid %s", z.Name, z.ID))
		res.ZonesAdded++
		if s.dryRun {
			domainID = newZoneDomainID
		} else {
			id, err := s.st.InsertZone(ctx, z.Name, z.ID)
			if err != nil {
				return err
			}
			domainID = id
		}
	}

	return s.syncRecords(ctx, z, domainID, res)
}

func (s *Sync) syncRecords(ctx context.Context, z cf.Zone, domainID int64, res *Result) error {
	cfRecs, err := s.cf.ListRecords(ctx, z.ID)
	if err != nil {
		return err
	}
	if domainID == newZoneDomainID {
		// Zone would be new under dry-run; no mirror rows exist to diff.
		s.log.Info("would mirror records of new zone", "zone", z.Name, "count", len(cfRecs))
		res.RecordsAdded += len(cfRecs)
		return nil
	}

	dbRecs, err := s.st.ListRecords(ctx, domainID)
	if err != nil {
		return err
	}

	byID := make(map[string]store.Record, len(dbRecs))
	var legacyEmpty []store.Record
	for _, dr := range dbRecs {
		if dr.RecordID == "" {
			legacyEmpty = append(legacyEmpty, dr)
		} else {
			byID[dr.RecordID] = dr
		}
	}

	present := map[string]bool{}
	for _, cr := range cfRecs {
		present[cr.ID] = true
		rec := mirrorRecord(cr)
		if dr, ok := byID[cr.ID]; ok {
			if !recordEqual(dr, rec) || dr.Status != store.StatusOn {
				s.logAction("record", "update", recordDesc(z.Name, rec))
				res.RecordsUpdated++
				if !s.dryRun {
					if err := s.st.UpdateRecord(ctx, dr.ID, rec); err != nil {
						return err
					}
				}
			}
			continue
		}
		if i := matchLegacy(legacyEmpty, rec, z.Name); i >= 0 {
			dr := legacyEmpty[i]
			legacyEmpty = append(legacyEmpty[:i], legacyEmpty[i+1:]...)
			s.logAction("record", "fill", recordDesc(z.Name, rec))
			res.RecordsUpdated++
			if !s.dryRun {
				if err := s.st.UpdateRecord(ctx, dr.ID, rec); err != nil {
					return err
				}
			}
			continue
		}
		s.logAction("record", "add", recordDesc(z.Name, rec))
		res.RecordsAdded++
		if !s.dryRun {
			if _, err := s.st.InsertRecord(ctx, domainID, rec); err != nil {
				return err
			}
		}
	}

	for _, dr := range dbRecs {
		if dr.Status != store.StatusOn || dr.RecordID == "" {
			continue
		}
		if !present[dr.RecordID] {
			s.logAction("record", "disable", fmt.Sprintf("%s %s (%s) -> absent from Cloudflare",
				dr.Type, dr.Name, dr.Content))
			res.RecordsDisabled++
			if !s.dryRun {
				if err := s.st.SetRecordStatus(ctx, dr.ID, store.StatusOff); err != nil {
					return err
				}
			}
		}
	}
	// Legacy rows with no Cloudflare identity that matched nothing are stale.
	for _, dr := range legacyEmpty {
		if dr.Status == store.StatusOn {
			s.logAction("record", "disable", fmt.Sprintf("%s %s (%s) -> no matching Cloudflare record",
				dr.Type, dr.Name, dr.Content))
			res.RecordsDisabled++
			if !s.dryRun {
				if err := s.st.SetRecordStatus(ctx, dr.ID, store.StatusOff); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func mirrorRecord(cr cf.Record) store.Record {
	return store.Record{
		Type:     cr.Type,
		Name:     cr.Name,
		Content:  cr.Content,
		Proxied:  cr.Proxied,
		TTL:      cr.TTL,
		Priority: cr.Priority,
		RecordID: cr.ID,
	}
}

func recordEqual(dr store.Record, rec store.Record) bool {
	return dr.Type == rec.Type && dr.Name == rec.Name && dr.Content == rec.Content &&
		dr.Proxied == rec.Proxied && dr.TTL == rec.TTL && dr.Priority == rec.Priority
}

// matchLegacy finds a DB row (with empty recordid) matching the CF record.
// Legacy rows store relative labels (@, *, www) while Cloudflare returns
// FQDNs, so the apex/wildcard/www labels are resolved against the zone name.
func matchLegacy(rows []store.Record, rec store.Record, zone string) int {
	for i, r := range rows {
		if r.Type == rec.Type && r.Name == rec.Name && r.Content == rec.Content {
			return i
		}
	}
	for i, r := range rows {
		if r.Type != rec.Type || r.Content != rec.Content {
			continue
		}
		switch r.Name {
		case "@":
			if rec.Name == zone {
				return i
			}
		case "*":
			if rec.Name == "*."+zone {
				return i
			}
		case "www":
			if rec.Name == "www."+zone {
				return i
			}
		}
	}
	return -1
}

func recordDesc(zone string, rec store.Record) string {
	return fmt.Sprintf("%s %s %s %s", zone, rec.Type, rec.Name, rec.Content)
}

func (s *Sync) logAction(entity, op, detail string) {
	s.log.Info("sync action", "entity", entity, "op", op, "detail", detail, "dry_run", s.dryRun)
}
