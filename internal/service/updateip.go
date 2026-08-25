package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"

	"github.com/webzone/cloudflare_dns/internal/cf"
	"github.com/webzone/cloudflare_dns/internal/store"
)

// IPDetector returns the current public IPv4 address.
type IPDetector func(ctx context.Context) (netip.Addr, error)

// Updater keeps A records tracking the home public IP in sync. Cloudflare is
// updated first; the local DB follows only after each Cloudflare call
// succeeds, per the "Cloudflare is the single source of truth" rule.
type Updater struct {
	cf      *cf.Client
	st      *store.Store
	log     *slog.Logger
	detect  IPDetector
	dryRun  bool
	autoTTL int // TTL sent to Cloudflare when the mirror has none (1 = auto)
}

// NewUpdater builds an IP updater.
func NewUpdater(c *cf.Client, s *store.Store, log *slog.Logger, detect IPDetector, dryRun bool) *Updater {
	return &Updater{cf: c, st: s, log: log, detect: detect, dryRun: dryRun, autoTTL: 1}
}

// splitTracked separates tracked mirror records into those already holding
// the detected IP (skipped — no Cloudflare call needed) and those that differ
// and therefore need a Cloudflare write.
func splitTracked(recs []store.HomeRecord, ip string) (pending []store.HomeRecord, skipped int) {
	for _, r := range recs {
		if r.Content == ip {
			skipped++
			continue
		}
		pending = append(pending, r)
	}
	return pending, skipped
}

// zoneDiff classifies the zone-set changes update-ip reconciles: zones on
// Cloudflare not in the mirror (initialize them) and managed mirror zones
// that vanished from Cloudflare (deregister: registrar -> other).
func zoneDiff(cfZones []cf.Zone, dbZones map[string]store.Zone) (added []cf.Zone, removed []store.Zone) {
	present := make(map[string]bool, len(cfZones))
	for _, z := range cfZones {
		present[z.ID] = true
		if _, ok := dbZones[z.ID]; !ok {
			added = append(added, z)
		}
	}
	for _, dz := range dbZones {
		if dz.Status != store.StatusOn || dz.Registrar != "cloudflare" {
			continue
		}
		if !present[dz.ZoneID] {
			removed = append(removed, dz)
		}
	}
	return added, removed
}

// reconcileZones keeps the managed-zone set in step with Cloudflare: every
// run it lists zones once, initializes newly seen zones (base @/www/* records
// at the home IP, track=1, mirror) and deregisters managed zones that have
// vanished from Cloudflare (registrar -> 'other', status -> off).
func (u *Updater) reconcileZones(ctx context.Context, ip string) error {
	cfZones, err := u.cf.ListZones(ctx)
	if err != nil {
		return fmt.Errorf("list cloudflare zones: %w", err)
	}
	dbZones, err := u.st.ListZones(ctx)
	if err != nil {
		return fmt.Errorf("list mirror zones: %w", err)
	}
	added, removed := zoneDiff(cfZones, dbZones)
	u.log.Info("zone reconcile", "zones", len(cfZones), "added", len(added),
		"removed", len(removed), "dry_run", u.dryRun)

	for _, z := range added {
		u.log.Info("new zone on Cloudflare; initializing base records",
			"zone", z.Name, "zone_id", z.ID, "dry_run", u.dryRun)
		if u.dryRun {
			continue
		}
		detect := func(context.Context) (netip.Addr, error) { return netip.ParseAddr(ip) }
		init := NewInitiator(u.cf, u.st, u.log, detect, u.dryRun)
		if err := init.Run(ctx, z.Name, true); err != nil {
			u.log.Error("zone initialization failed; retrying next run", "zone", z.Name, "err", err)
			continue
		}
	}
	for _, dz := range removed {
		u.log.Info("zone vanished from Cloudflare; deregistering",
			"zone", dz.Name, "dry_run", u.dryRun)
		if u.dryRun {
			continue
		}
		if err := u.st.SetZoneRegistrar(ctx, dz.ID, "other"); err != nil {
			return err
		}
		if err := u.st.SetZoneStatus(ctx, dz.ID, store.StatusOff); err != nil {
			return err
		}
	}
	return nil
}

// Run performs one update-ip pass. It first reconciles the zone set, then
// compares every tracked (track_ip=1) A record straight against the detected
// public IP from the local DB: records already pointing at it are skipped
// (no Cloudflare call), every other tracked record — wherever its content
// came from — is updated to the home IP, Cloudflare first, mirror after each
// success. last_known_ip is informational bookkeeping only; it never gates
// the scan, so records re-flagged track=on mid-IP-cycle still converge.
func (u *Updater) Run(ctx context.Context) error {
	addr, err := u.detect(ctx)
	if err != nil {
		// Fail safe: never write anything when detection is uncertain.
		return fmt.Errorf("skip run: %w", err)
	}
	ip := addr.String()

	if err := u.reconcileZones(ctx, ip); err != nil {
		// Zone changes must not block IP updates; retry next run.
		u.log.Error("zone reconcile failed; will retry next run", "err", err)
	}

	last, known, err := u.st.GetState(ctx, store.StateKeyLastIP)
	if err != nil {
		return err
	}
	if !known || last == "" {
		u.log.Info("bootstrapping IP state (no previous known IP)", "ip", ip)
		if !u.dryRun {
			if err := u.st.SetState(ctx, store.StateKeyLastIP, ip); err != nil {
				return err
			}
			last, known = ip, true
		}
	}

	recs, err := u.st.ListTrackedARecords(ctx)
	if err != nil {
		return err
	}

	// The comparison is against the detected IP directly — every tracked
	// record, regardless of last_known_ip.
	pending, skipped := splitTracked(recs, ip)
	if len(pending) == 0 {
		u.log.Info("all tracked A records already point at the public IP", "ip", ip,
			"tracked", len(recs), "last_known_ip", lastKnown(last, known), "dry_run", u.dryRun)
		if (!known || last != ip) && !u.dryRun {
			if err := u.st.SetState(ctx, store.StateKeyLastIP, ip); err != nil {
				return err
			}
		}
		return nil
	}

	updated, failed := 0, 0
	for _, r := range pending {
		ttl := r.TTL
		if ttl <= 0 {
			ttl = u.autoTTL
		}
		u.log.Info("updating A record to public IP",
			"record", r.RecordID, "zone", r.ZoneID, "name", r.Name, "from", r.Content, "to", ip, "dry_run", u.dryRun)
		if u.dryRun {
			updated++
			continue
		}
		if err := u.cf.UpdateARecord(ctx, cf.Record{
			ID: r.RecordID, ZoneID: r.ZoneID, Type: "A", Name: r.Name,
			Proxied: r.Proxied, TTL: ttl,
		}, ip); err != nil {
			if isDuplicateRecordErr(err) {
				// Cloudflare already has this name+type+content: this record
				// can never follow the home IP without duplicating another
				// record, so it is structurally excluded — untrack locally
				// and stop retrying it.
				if !u.dryRun {
					if terr := u.st.SetRecordTrack(ctx, r.ID, false); terr != nil {
						u.log.Error("untrack duplicate-blocked record failed", "record", r.RecordID, "err", terr)
					}
				}
				u.log.Warn("record cannot follow the home IP (identical A record already exists); untracked locally",
					"record", r.RecordID, "zone", r.ZoneID, "name", r.Name)
				skipped++
				continue
			}
			u.log.Error("Cloudflare update failed; local DB left unchanged, will retry",
				"record", r.RecordID, "err", err)
			failed++
			continue
		}
		// Cloudflare accepted the change: only now record it locally.
		if err := u.st.UpdateRecordContent(ctx, r.ID, ip); err != nil {
			u.log.Error("Cloudflare updated but local DB write failed",
				"record", r.RecordID, "err", err)
			failed++
			continue
		}
		updated++
	}

	u.log.Info("IP update finished", "updated", updated, "failed", failed,
		"skipped", skipped, "total", len(recs), "dry_run", u.dryRun)
	if failed > 0 {
		return fmt.Errorf("%d of %d records updated", updated, len(pending))
	}
	if u.dryRun {
		return nil
	}
	return u.st.SetState(ctx, store.StateKeyLastIP, ip)
}

func lastKnown(v string, known bool) string {
	if !known || v == "" {
		return "unknown"
	}
	return v
}

// isDuplicateRecordErr reports whether Cloudflare rejected the write because
// an identical record (name+type+content) already exists (API code 81058).
func isDuplicateRecordErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), `"code":81058`)
}
