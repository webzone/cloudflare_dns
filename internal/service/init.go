package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/webzone/cloudflare_dns/internal/cf"
	"github.com/webzone/cloudflare_dns/internal/store"
)

// Initiator sets up DNS for a zone added to Cloudflare. It detects the public
// IP once, ensures the base A records point at it on Cloudflare (updating or
// creating as needed), mirrors them locally and flags them track_ip=1 so
// update-ip owns them from then on.
type Initiator struct {
	cf     *cf.Client
	st     *store.Store
	log    *slog.Logger
	detect IPDetector
	dryRun bool
}

// NewInitiator builds a zone initializer.
func NewInitiator(c *cf.Client, s *store.Store, log *slog.Logger, detect IPDetector, dryRun bool) *Initiator {
	return &Initiator{cf: c, st: s, log: log, detect: detect, dryRun: dryRun}
}

// Run initializes the named zone.
func (i *Initiator) Run(ctx context.Context, zoneName string, wildcard bool) error {
	zones, err := i.cf.ListZones(ctx)
	if err != nil {
		return err
	}
	var z *cf.Zone
	for idx := range zones {
		if strings.EqualFold(zones[idx].Name, zoneName) {
			z = &zones[idx]
			break
		}
	}
	if z == nil {
		return fmt.Errorf("zone %q not on Cloudflare: add it in the dashboard (dash.cloudflare.com -> Add site) first", zoneName)
	}

	addr, err := i.detect(ctx)
	if err != nil {
		return fmt.Errorf("skip run: %w", err)
	}
	ip := addr.String()

	existing, err := i.cf.ListRecords(ctx, z.ID)
	if err != nil {
		return err
	}
	allByName := map[string]cf.Record{}
	for _, r := range existing {
		allByName[strings.ToLower(strings.TrimSuffix(r.Name, "."))] = r
	}

	labels := baseLabels[:2]
	if wildcard {
		labels = baseLabels
	}

	type cell struct {
		fqdn string
		rec  cf.Record
	}
	var cells []cell
	created, updated, skipped := 0, 0, 0

	for _, label := range labels {
		fqdn := FQDNHost(label, zoneName)
		all, ok := allByName[strings.ToLower(fqdn)]
		if ok && all.Type != "A" {
			skipped++
			i.log.Warn("init: non-A record at name; leaving it", "zone", zoneName, "name", fqdn, "type", all.Type)
			continue
		}
		if ok && all.Content == ip {
			skipped++
			cells = append(cells, cell{fqdn: fqdn, rec: all})
			i.log.Info("init: A record already at home IP", "zone", zoneName, "name", fqdn, "content", ip)
			continue
		}
		if ok {
			ttl := all.TTL
			if ttl <= 0 {
				ttl = 1
			}
			i.log.Info("init: updating A record to home IP", "zone", zoneName, "name", fqdn,
				"from", all.Content, "to", ip, "dry_run", i.dryRun)
			if !i.dryRun {
				if err := i.cf.UpdateARecord(ctx, cf.Record{
					ID: all.ID, ZoneID: z.ID, Type: "A", Name: all.Name,
					Proxied: all.Proxied, TTL: ttl,
				}, ip); err != nil {
					return err
				}
			}
			updated++
			cells = append(cells, cell{fqdn: fqdn, rec: all})
			continue
		}
		i.log.Info("init: creating A record", "zone", zoneName, "name", fqdn, "content", ip, "dry_run", i.dryRun)
		created++
		if i.dryRun {
			continue
		}
		rec, err := i.cf.CreateARecord(ctx, z.ID, fqdn, ip, true, 1)
		if err != nil {
			return err
		}
		cells = append(cells, cell{fqdn: fqdn, rec: rec})
	}

	i.log.Info("init: Cloudflare phase complete", "zone", zoneName, "ip", ip,
		"created", created, "updated", updated, "skipped", skipped, "dry_run", i.dryRun)
	if i.dryRun {
		return nil
	}

	// Mirror phase: ensure the zone row exists, upsert the base records and
	// flag exactly those track_ip=1, seed last_known_ip.
	dz, ok, err := i.st.ZoneByName(ctx, zoneName)
	if err != nil {
		return err
	}
	var domainID int64
	if !ok {
		domainID, err = i.st.InsertZone(ctx, z.Name, z.ID)
		if err != nil {
			return err
		}
	} else {
		domainID = dz.ID
		if dz.ZoneID != z.ID {
			if err := i.st.SetZoneID(ctx, domainID, z.ID); err != nil {
				return err
			}
		}
		if dz.Status != store.StatusOn {
			if err := i.st.SetZoneStatus(ctx, domainID, store.StatusOn); err != nil {
				return err
			}
		}
	}

	dbRecs, err := i.st.ListRecords(ctx, domainID)
	if err != nil {
		return err
	}
	byRecordID := map[string]store.Record{}
	for _, dr := range dbRecs {
		if dr.RecordID != "" {
			byRecordID[dr.RecordID] = dr
		}
	}

	mirrored := 0
	for _, c := range cells {
		mr := store.Record{
			Type: "A", Name: c.rec.Name, Content: c.rec.Content,
			Proxied: c.rec.Proxied, TTL: c.rec.TTL, RecordID: c.rec.ID,
		}
		var dnsID int64
		if dr, have := byRecordID[c.rec.ID]; have {
			if err := i.st.UpdateRecord(ctx, dr.ID, mr); err != nil {
				return err
			}
			dnsID = dr.ID
		} else {
			dnsID, err = i.st.InsertRecord(ctx, domainID, mr)
			if err != nil {
				return err
			}
		}
		if err := i.st.SetRecordTrack(ctx, dnsID, true); err != nil {
			return err
		}
		mirrored++
	}

	if err := i.st.SetState(ctx, store.StateKeyLastIP, ip); err != nil {
		return err
	}

	i.log.Info("init complete", "zone", zoneName, "ip", ip,
		"created", created, "updated", updated, "skipped", skipped, "mirrored", mirrored)
	return nil
}
