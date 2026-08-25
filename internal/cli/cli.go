// Package cli wires configuration, the Cloudflare client and the store into
// the service layer behind the cfddns subcommands.
package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"

	"github.com/webzone/cloudflare_dns/internal/cf"
	"github.com/webzone/cloudflare_dns/internal/config"
	"github.com/webzone/cloudflare_dns/internal/ip"
	"github.com/webzone/cloudflare_dns/internal/service"
	"github.com/webzone/cloudflare_dns/internal/store"
)

// App is the cfddns command dispatcher.
type App struct {
	cfg *config.Config
	log *slog.Logger
}

// New builds the dispatcher.
func New(cfg *config.Config, log *slog.Logger) *App {
	return &App{cfg: cfg, log: log}
}

// Usage is the top-level help text.
var Usage = `cfddns — Cloudflare DNS mirror & dynamic DNS

USAGE
  cfddns [--dry-run] <command> [args]

COMMANDS
  sync                 Mirror Cloudflare zones/records into the local DB
                       (Cloudflare is the single source of truth)
  update-ip            Update A records tracking the home public IP:
                       Cloudflare first, then the local mirror. Records are
                       compared against the mirror (no Cloudflare calls when
                       nothing changed); only records flagged track=on move
  init <zone>          Initialize a new zone on Cloudflare: create/mirror the
                       base A records (@ and www) at the current public IP and
                       flag them for update-ip (--wildcard adds *)
  track <zone> [name]  Mark A record(s) of a zone as following the home IP:
                         track example.com on|off        (whole zone)
                         track example.com www on|off    (one record; name
                                                          can be @, * or FQDN)
  purge [zone]         Purge edge cache of one zone or all mirrored zones
  inspect zones        List Cloudflare zones
  inspect records <z>  List DNS records of one zone (combined with the
                       mirror, so the track column is visible)
  help                 Show this help

GLOBAL
  --dry-run            Log planned changes without applying them
  --wildcard           init: also create the * wildcard A record
  Config comes from the environment (see README.md)
  Migrations run automatically on startup
`

// Run dispatches a subcommand.
func (a *App) Run(ctx context.Context, args []string) error {
	dryRun := a.cfg.DryRun
	wildcard := false
	rest := make([]string, 0, len(args))
	for _, arg := range args {
		switch arg {
		case "--dry-run", "-n":
			dryRun = true
			continue
		case "--wildcard":
			wildcard = true
			continue
		}
		rest = append(rest, arg)
	}
	if len(rest) == 0 {
		fmt.Print(Usage)
		return nil
	}

	switch rest[0] {
	case "sync":
		return a.runSync(ctx, dryRun)
	case "update-ip":
		return a.runUpdateIP(ctx, dryRun)
	case "init":
		if len(rest) < 2 {
			return errors.New("usage: cfddns init <zone> [--wildcard]")
		}
		return a.runInit(ctx, rest[1], wildcard, dryRun)
	case "track":
		return a.runTrack(ctx, rest[1:], dryRun)
	case "purge":
		zone := ""
		if len(rest) > 1 {
			zone = rest[1]
		}
		return a.runPurge(ctx, zone, dryRun)
	case "inspect":
		return a.runInspect(ctx, rest[1:])
	case "help", "-h", "--help":
		fmt.Print(Usage)
		return nil
	default:
		fmt.Print(Usage)
		return fmt.Errorf("unknown command %q", rest[0])
	}
}

func (a *App) open(ctx context.Context) (*cf.Client, *store.Store, func(), error) {
	if missing := a.cfg.CheckMySQL(); len(missing) > 0 {
		return nil, nil, nil, fmt.Errorf("missing required env %v", missing)
	}
	st, err := store.Open(ctx, a.cfg.MySQLDSN(), a.log)
	if err != nil {
		return nil, nil, nil, err
	}
	closeFn := func() { _ = st.Close() }

	var client *cf.Client
	if a.cfg.CloudflareToken != "" {
		client, err = cf.New(ctx, a.cfg.CloudflareToken)
		if err != nil {
			closeFn()
			return nil, nil, nil, err
		}
	} else {
		client = cf.NewWithAPIKey(a.cfg.CloudflareEmail, a.cfg.CloudflareKey)
	}
	return client, st, closeFn, nil
}

func (a *App) runSync(ctx context.Context, dryRun bool) error {
	client, st, closeFn, err := a.open(ctx)
	if err != nil {
		return err
	}
	defer closeFn()
	_, err = service.NewSync(client, st, a.log, service.IPDetector(a.detectIP), dryRun).Run(ctx)
	return err
}

func (a *App) runUpdateIP(ctx context.Context, dryRun bool) error {
	client, st, closeFn, err := a.open(ctx)
	if err != nil {
		return err
	}
	defer closeFn()
	return service.NewUpdater(client, st, a.log, service.IPDetector(a.detectIP), dryRun).Run(ctx)
}

func (a *App) runInit(ctx context.Context, zone string, wildcard, dryRun bool) error {
	client, st, closeFn, err := a.open(ctx)
	if err != nil {
		return err
	}
	defer closeFn()
	return service.NewInitiator(client, st, a.log, service.IPDetector(a.detectIP), dryRun).Run(ctx, zone, wildcard)
}

func (a *App) runTrack(ctx context.Context, args []string, dryRun bool) error {
	if len(args) < 2 || len(args) > 3 {
		return errors.New("usage: cfddns track <zone> [name] on|off")
	}
	zone := args[0]
	onOff := args[len(args)-1]
	var track bool
	switch onOff {
	case "on":
		track = true
	case "off":
		track = false
	default:
		return fmt.Errorf(`track flag must be "on" or "off", got %q`, onOff)
	}
	name := ""
	if len(args) == 3 {
		name = args[1]
	}

	_, st, closeFn, err := a.open(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	dz, ok, err := st.ZoneByName(ctx, zone)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("zone %q not in the mirror; run `cfddns sync` first", zone)
	}

	if name == "" {
		if dryRun {
			fmt.Printf("would set track=%v on all A records of %s\n", track, zone)
			return nil
		}
		n, err := st.SetZoneTrackARecords(ctx, dz.ID, track)
		if err != nil {
			return err
		}
		fmt.Printf("track=%v on %d A records of %s\n", track, n, zone)
		return nil
	}

	target := service.FQDNHost(name, zone)
	rec, ok, err := st.FindRecordByName(ctx, dz.ID, target)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no A record %q in zone %s", target, zone)
	}
	if dryRun {
		fmt.Printf("would set track=%v on %s (%s)\n", track, target, rec.Content)
		return nil
	}
	if err := st.SetRecordTrack(ctx, rec.ID, track); err != nil {
		return err
	}
	fmt.Printf("track=%v on %s (%s)\n", track, target, rec.Content)
	return nil
}

func (a *App) runPurge(ctx context.Context, zone string, dryRun bool) error {
	client, st, closeFn, err := a.open(ctx)
	if err != nil {
		return err
	}
	defer closeFn()
	return service.NewPurger(client, st, a.log, dryRun).Run(ctx, zone)
}

func (a *App) runInspect(ctx context.Context, args []string) error {
	if len(args) == 0 || (args[0] != "zones" && args[0] != "records") {
		return errors.New("usage: cfddns inspect zones | records <zone>")
	}
	client, err := cfOnly(ctx, a.cfg)
	if err != nil {
		return err
	}
	if args[0] == "zones" {
		zones, err := client.ListZones(ctx)
		if err != nil {
			return err
		}
		for _, z := range zones {
			fmt.Printf("%s\t%s\n", z.ID, z.Name)
		}
		return nil
	}
	if len(args) < 2 {
		return errors.New("inspect records needs a zone name")
	}

	// The records view needs the mirror for the track column.
	client, st, closeFn, err := a.open(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	zones, err := client.ListZones(ctx)
	if err != nil {
		return err
	}
	var zoneID string
	for _, z := range zones {
		if z.Name == args[1] {
			zoneID = z.ID
			break
		}
	}
	if zoneID == "" {
		return fmt.Errorf("zone %q not found on Cloudflare", args[1])
	}
	recs, err := client.ListRecords(ctx, zoneID)
	if err != nil {
		return err
	}

	// Overlay the mirror's track flags so operators can see which records
	// update-ip owns (records not mirrored yet show "-").
	track := map[string]bool{}
	if dz, ok, err := st.ZoneByName(ctx, args[1]); err == nil && ok {
		if drecs, err := st.ListRecords(ctx, dz.ID); err == nil {
			for _, dr := range drecs {
				if dr.RecordID != "" {
					track[dr.RecordID] = dr.TrackIP
				}
			}
		}
	}
	for _, r := range recs {
		proxied := "-"
		if r.Proxied {
			proxied = "proxied"
		}
		tv := "-"
		if v, ok := track[r.ID]; ok {
			tv = "off"
			if v {
				tv = "on"
			}
		}
		fmt.Printf("%s\t%s\t%s\t%d\t%s\ttrack=%s\n", r.Type, r.Name, r.Content, r.TTL, proxied, tv)
	}
	return nil
}

// detectIP returns the network-detected public IP unless the test-only env
// override CF_DDNS_TEST_IP is set (used with --dry-run to exercise the IP
// change path without a real IP change).
func (a *App) detectIP(ctx context.Context) (netip.Addr, error) {
	if t := os.Getenv("CF_DDNS_TEST_IP"); t != "" {
		addr, err := netip.ParseAddr(t)
		if err != nil || !addr.Is4() {
			return netip.Addr{}, fmt.Errorf("invalid CF_DDNS_TEST_IP %q", t)
		}
		return addr, nil
	}
	return ip.Detect(ctx)
}

func cfOnly(ctx context.Context, cfg *config.Config) (*cf.Client, error) {
	if cfg.CloudflareToken != "" {
		return cf.New(ctx, cfg.CloudflareToken)
	}
	return cf.NewWithAPIKey(cfg.CloudflareEmail, cfg.CloudflareKey), nil
}
