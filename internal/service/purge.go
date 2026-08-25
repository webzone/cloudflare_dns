package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/webzone/cloudflare_dns/internal/cf"
	"github.com/webzone/cloudflare_dns/internal/store"
)

// Purger purges Cloudflare edge caches for one zone or all mirrored zones.
type Purger struct {
	cf     *cf.Client
	st     *store.Store
	log    *slog.Logger
	dryRun bool
}

// NewPurger builds a cache purger.
func NewPurger(c *cf.Client, s *store.Store, log *slog.Logger, dryRun bool) *Purger {
	return &Purger{cf: c, st: s, log: log, dryRun: dryRun}
}

// Run purges the given zone name (exact) or every active mirrored zone.
func (p *Purger) Run(ctx context.Context, zoneName string) error {
	zones, err := p.st.ListZones(ctx)
	if err != nil {
		return err
	}
	var targets []store.Zone
	if zoneName != "" {
		found := false
		for _, z := range zones {
			if z.Name == zoneName {
				targets = []store.Zone{z}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("zone %q not found in mirror", zoneName)
		}
	} else {
		for _, z := range zones {
			if z.Status == store.StatusOn && z.ZoneID != "" {
				targets = append(targets, z)
			}
		}
	}

	n := 0
	for _, z := range targets {
		p.log.Info("purge cache", "zone", z.Name, "zone_id", z.ZoneID, "dry_run", p.dryRun)
		if p.dryRun {
			n++
			continue
		}
		if err := p.cf.PurgeCache(ctx, z.ZoneID); err != nil {
			p.log.Error("purge failed", "zone", z.Name, "err", err)
			continue
		}
		n++
	}
	p.log.Info("purge finished", "purged", n, "total", len(targets), "dry_run", p.dryRun)
	if n < len(targets) {
		return fmt.Errorf("%d of %d zones purged", n, len(targets))
	}
	return nil
}
