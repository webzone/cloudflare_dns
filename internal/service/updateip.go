package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"

	"github.com/webzone/cloudflare_dns/internal/cf"
	"github.com/webzone/cloudflare_dns/internal/store"
)

// IPDetector returns the current public IPv4 address.
type IPDetector func(ctx context.Context) (netip.Addr, error)

// Updater keeps A records tracking the home public IP in sync. Cloudflare is
// updated first; the local mirror follows only after each Cloudflare call
// succeeds, per the "Cloudflare is the single source of truth" rule.
type Updater struct {
	cf        *cf.Client
	st        *store.Store
	log       *slog.Logger
	detect    IPDetector
	dryRun    bool
	autoTTL   int // TTL sent to Cloudflare when the mirror has none (1 = auto)
}

// NewUpdater builds an IP updater.
func NewUpdater(c *cf.Client, s *store.Store, log *slog.Logger, detect IPDetector, dryRun bool) *Updater {
	return &Updater{cf: c, st: s, log: log, detect: detect, dryRun: dryRun, autoTTL: 1}
}

// Run performs one update-ip pass. It is conservative: no record is touched
// on Cloudflare unless a fresh, multi-source IP was detected, and the mirror
// only follows a Cloudflare write that already succeeded.
func (u *Updater) Run(ctx context.Context) error {
	addr, err := u.detect(ctx)
	if err != nil {
		// Fail safe: never write anything when detection is uncertain.
		return fmt.Errorf("skip run: %w", err)
	}
	ip := addr.String()

	last, known, err := u.st.GetState(ctx, store.StateKeyLastIP)
	if err != nil {
		return err
	}
	if !known || last == "" {
		u.log.Info("bootstrapping IP state (no previous known IP)", "ip", ip)
		if u.dryRun {
			return nil
		}
		return u.st.SetState(ctx, store.StateKeyLastIP, ip)
	}
	if last == ip {
		u.log.Info("public IP unchanged", "ip", ip)
		return nil
	}

	recs, err := u.st.ListHomeARecords(ctx, last)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		u.log.Info("no mirror records tracked the previous IP; adopting new state",
			"from", last, "to", ip)
		if u.dryRun {
			return nil
		}
		return u.st.SetState(ctx, store.StateKeyLastIP, ip)
	}

	updated, failed := 0, 0
	for _, r := range recs {
		ttl := r.TTL
		if ttl <= 0 {
			ttl = u.autoTTL
		}
		u.log.Info("updating A record to new public IP",
			"record", r.RecordID, "zone", r.ZoneID, "name", r.Name, "from", last, "to", ip, "dry_run", u.dryRun)
		if u.dryRun {
			updated++
			continue
		}
		if err := u.cf.UpdateARecord(ctx, cf.Record{
			ID: r.RecordID, ZoneID: r.ZoneID, Type: "A", Name: r.Name,
			Proxied: r.Proxied, TTL: ttl,
		}, ip); err != nil {
			u.log.Error("Cloudflare update failed; mirror left unchanged, will retry",
				"record", r.RecordID, "err", err)
			failed++
			continue
		}
		// Cloudflare accepted the change: only now mirror it locally.
		if err := u.st.UpdateRecordContent(ctx, r.ID, ip); err != nil {
			u.log.Error("Cloudflare updated but mirror write failed",
				"record", r.RecordID, "err", err)
			failed++
			continue
		}
		updated++
	}

	u.log.Info("IP update finished", "updated", updated, "failed", failed,
		"total", len(recs), "dry_run", u.dryRun)
	if failed > 0 {
		// Keep last_known_ip at the old value so failed records retry next run.
		return fmt.Errorf("%d of %d records updated", updated, len(recs))
	}
	if u.dryRun {
		return nil
	}
	return u.st.SetState(ctx, store.StateKeyLastIP, ip)
}
