package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/webzone/cloudflare_dns/internal/cf"
)

// baseLabels are the standard A records every managed zone carries: apex,
// www and the wildcard. init creates apex+www by default and all three with
// --wildcard; sync auto-completes any of them when absent.
var baseLabels = []string{"@", "www", "*"}

// FQDNHost resolves a DNS label to the FQDN Cloudflare expects. A fully
// qualified input (contains a dot) is returned as-is.
func FQDNHost(label, zone string) string {
	switch label {
	case "@":
		return zone
	case "*":
		return "*." + zone
	}
	if strings.Contains(label, ".") {
		return strings.TrimSuffix(label, ".")
	}
	return label + "." + zone
}

// missingBaseLabels returns the @/www/* labels whose host name has no record
// at all in the zone (a record of any type occupies the name, so nothing is
// created next to a CNAME or an A pointing elsewhere).
func missingBaseLabels(existing []cf.Record, zone string) []string {
	occupied := map[string]bool{}
	for _, r := range existing {
		occupied[strings.ToLower(strings.TrimSuffix(r.Name, "."))] = true
	}
	var missing []string
	for _, label := range baseLabels {
		if !occupied[strings.ToLower(FQDNHost(label, zone))] {
			missing = append(missing, label)
		}
	}
	return missing
}

// ensureBaseARecords creates any of the standard @/www/* A records that are
// entirely absent from a managed zone, pointing them at the current home IP
// (proxied, auto TTL). Existing records — whatever they point at — are never
// touched; that is update-ip's job and only for tracked records. Returns the
// records created on Cloudflare so the mirror diff picks them up.
func (s *Sync) ensureBaseARecords(ctx context.Context, z cf.Zone, existing []cf.Record) ([]cf.Record, error) {
	missing := missingBaseLabels(existing, z.Name)
	if len(missing) == 0 {
		return nil, nil
	}
	addr, err := s.detect(ctx)
	if err != nil {
		return nil, fmt.Errorf("detect public IP: %w", err)
	}
	ip := addr.String()
	var created []cf.Record
	for _, label := range missing {
		fqdn := FQDNHost(label, z.Name)
		s.logAction("record", "add-base", fmt.Sprintf("%s A %s %s (managed-zone base record)", z.Name, fqdn, ip))
		if s.dryRun {
			continue
		}
		rec, err := s.cf.CreateARecord(ctx, z.ID, fqdn, ip, true, 1)
		if err != nil {
			return created, err
		}
		created = append(created, rec)
		s.log.Info("base A record created", "zone", z.Name, "name", fqdn, "content", ip)
	}
	return created, nil
}
