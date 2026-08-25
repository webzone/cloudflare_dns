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
                       Cloudflare first, then the local mirror
  purge [zone]         Purge edge cache of one zone or all mirrored zones
  inspect zones        List Cloudflare zones
  inspect records <z>  List DNS records of one Cloudflare zone
  help                 Show this help

GLOBAL
  --dry-run            Log planned changes without applying them
  Config comes from the environment (see README.md)
  Migrations run automatically on startup
`

// Run dispatches a subcommand.
func (a *App) Run(ctx context.Context, args []string) error {
	dryRun := a.cfg.DryRun
	rest := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--dry-run" || arg == "-n" {
			dryRun = true
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
	_, err = service.NewSync(client, st, a.log, dryRun).Run(ctx)
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
	for _, r := range recs {
		proxied := "-"
		if r.Proxied {
			proxied = "proxied"
		}
		fmt.Printf("%s\t%s\t%s\t%d\t%s\n", r.Type, r.Name, r.Content, r.TTL, proxied)
	}
	return nil
}

func cfOnly(ctx context.Context, cfg *config.Config) (*cf.Client, error) {
	if cfg.CloudflareToken != "" {
		return cf.New(ctx, cfg.CloudflareToken)
	}
	return cf.NewWithAPIKey(cfg.CloudflareEmail, cfg.CloudflareKey), nil
}
