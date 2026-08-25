// Package cli wires configuration, the Cloudflare client and the store into
// the service layer behind the cfddns subcommands.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/webzone/cloudflare_dns/internal/cf"
	"github.com/webzone/cloudflare_dns/internal/config"
	"github.com/webzone/cloudflare_dns/internal/ip"
	"github.com/webzone/cloudflare_dns/internal/service"
	"github.com/webzone/cloudflare_dns/internal/store"
)

// Version is stamped at build time (-ldflags
// "-X github.com/webzone/cloudflare_dns/internal/cli.Version=v0.1.0").
var Version = "dev"

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
var Usage = `cfddns — Cloudflare domain management & dynamic IP update

Zones (domains) are added/removed ONLY on the Cloudflare website
(dash.cloudflare.com). cfddns reads the zone list and manages the DNS
records inside zones; it never adds or removes zones itself.

USAGE
  cfddns [--dry-run] <command> [args]

ZONES (read-only view)
  zones                       list zones from the local DB (name, zone id,
                              registrar, status, record count)
  zones <zone>                zone detail (registrar, status, record counts)

DNS RECORDS
  dns <zone> [--type TYPE]    list records of a zone from the local DB, with
                              the track column (run sync first if stale)
  dns add <zone> <TYPE> <name> <content> [--ttl N] [--proxy|--no-proxy] [--prio N]
                              create a record of type A/AAAA/CNAME/MX/TXT.
                              A records of managed zones are tracked by default
  dns update <zone> <name> [--content X] [--ttl N] [--proxy|--no-proxy] [--prio N]
                              change fields of an existing record
  dns rm <zone> <name> -y     delete a record (Cloudflare + local soft-disable)

TOKEN
  token                       show token state (masked)
  token set [<token>]         store a token (prompts if not given); validates
                              against Cloudflare first; stored owner-only
  token rm                    remove the stored token
  token test                  validate the current token

AUTOMATION
  sync                        record Cloudflare zones/records into the local DB;
                              mark present zones registrar=cloudflare;
                              auto-create any missing @/www/* A record
  update-ip                   reconcile zone set (init new, deregister zones
                              that vanished), then move tracked A records to
                              the home IP (Cloudflare first, local DB after)
  init <zone> [--wildcard]    initialize a zone the owner added on CF:
                              base A records (@, www [+ *]) at the home IP
  track <zone> [name] [ip] on|off
                              mark A record(s) as following (on) or not
                              following (off) the home IP; name can be
                              @, *, www or any FQDN (no name = whole zone);
                              an IP disambiguates same-name dual A records
  purge [zone]                purge edge cache of all/one managed zone
  status                      overview: home IP, local DB state, track counts

LEGACY
  inspect zones | records <z> aliases of ` + "`zones` / `dns <z>`" + `

GLOBAL
  --dry-run / -n              log planned changes without applying them
  -y / --yes                  confirm destructive operations (dns rm)
  Config from the environment (see README.md)
  Migrations run automatically on startup
`

// cmdOpts holds flags parsed from the command line.
type cmdOpts struct {
	dryRun, wildcard, yes, proxy, noProxy, all, version bool
	ttl, prio                                           int
	typ, content                                        string
}

// parseOpts splits flag tokens (any position) from positional args.
func parseOpts(args []string) (rest []string, o cmdOpts, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--dry-run", "-n":
			o.dryRun = true
		case "--wildcard":
			o.wildcard = true
		case "-y", "--yes":
			o.yes = true
		case "--proxy":
			o.proxy = true
		case "--no-proxy":
			o.noProxy = true
		case "--all":
			o.all = true
		case "--version", "-V":
			o.version = true
		case "--ttl", "--prio", "--type", "--content":
			if i+1 >= len(args) {
				return nil, o, fmt.Errorf("%s needs a value", a)
			}
			i++
			v := args[i]
			switch a {
			case "--ttl":
				n, e := strconv.Atoi(v)
				if e != nil || n <= 0 {
					return nil, o, fmt.Errorf("invalid --ttl %q", v)
				}
				o.ttl = n
			case "--prio":
				n, e := strconv.Atoi(v)
				if e != nil || n < 0 {
					return nil, o, fmt.Errorf("invalid --prio %q", v)
				}
				o.prio = n
			case "--type":
				o.typ = strings.ToUpper(v)
			case "--content":
				o.content = v
			}
		default:
			rest = append(rest, a)
		}
	}
	return rest, o, nil
}

// Run dispatches a subcommand.
func (a *App) Run(ctx context.Context, args []string) error {
	rest, o, err := parseOpts(args)
	if err != nil {
		return err
	}
	if o.version {
		fmt.Println(Version)
		return nil
	}
	if len(rest) == 0 {
		fmt.Print(Usage)
		return nil
	}

	switch rest[0] {
	case "sync":
		return a.runSync(ctx, o.dryRun)
	case "update-ip":
		return a.runUpdateIP(ctx, o.dryRun)
	case "init":
		if len(rest) < 2 {
			return errors.New("usage: cfddns init <zone> [--wildcard]")
		}
		return a.runInit(ctx, rest[1], o.wildcard, o.dryRun)
	case "track":
		return a.runTrack(ctx, rest[1:], o.dryRun)
	case "token":
		return a.runToken(ctx, rest[1:])
	case "purge":
		zone := ""
		if len(rest) > 1 {
			zone = rest[1]
		}
		return a.runPurge(ctx, zone, o.dryRun)
	case "zones":
		return a.runZones(ctx, rest[1:])
	case "dns":
		return a.runDNS(ctx, rest[1:], o)
	case "status":
		return a.runStatus(ctx)
	case "inspect":
		// legacy alias: inspect zones | inspect records <zone>
		if len(rest) < 2 {
			return errors.New("usage: cfddns inspect zones | records <zone>")
		}
		switch rest[1] {
		case "zones":
			return a.runZones(ctx, nil)
		case "records":
			return a.runDNS(ctx, rest[2:], cmdOpts{})
		default:
			return errors.New("usage: cfddns inspect zones | records <zone>")
		}
	case "help", "-h", "--help":
		fmt.Print(Usage)
		return nil
	case "version":
		fmt.Println(Version)
		return nil
	default:
		fmt.Print(Usage)
		return fmt.Errorf("unknown command %q", rest[0])
	}
}

func (a *App) open(ctx context.Context) (*cf.Client, *store.Store, func(), error) {
	if missing := a.cfg.CheckDB(); len(missing) > 0 {
		return nil, nil, nil, fmt.Errorf("missing required env %v", missing)
	}
	st, err := store.Open(ctx, a.cfg.DBLocation(), a.log)
	if err != nil {
		return nil, nil, nil, err
	}
	closeFn := func() { _ = st.Close() }

	client, err := a.cfClient(ctx)
	if err != nil {
		closeFn()
		return nil, nil, nil, err
	}
	return client, st, closeFn, nil
}

// openStore opens only the SQLite store (commands that never call the
// Cloudflare API: status, zones, track).
func (a *App) openStore(ctx context.Context) (*store.Store, func(), error) {
	if missing := a.cfg.CheckDB(); len(missing) > 0 {
		return nil, nil, fmt.Errorf("missing required env %v", missing)
	}
	st, err := store.Open(ctx, a.cfg.DBLocation(), a.log)
	if err != nil {
		return nil, nil, err
	}
	return st, func() { _ = st.Close() }, nil
}

// cfClient validates/renews the Cloudflare client. When the token is missing
// or rejected, an interactive prompt (TTY) captures a new one and stores it;
// non-interactive runs fail with guidance.
func (a *App) cfClient(ctx context.Context) (*cf.Client, error) {
	if a.cfg.CloudflareToken != "" {
		client, err := cf.New(ctx, a.cfg.CloudflareToken)
		if err != nil {
			return a.promptToken(ctx, err)
		}
		return client, nil
	}
	if a.cfg.CloudflareEmail != "" && a.cfg.CloudflareKey != "" {
		return cf.NewWithAPIKey(a.cfg.CloudflareEmail, a.cfg.CloudflareKey), nil
	}
	return a.promptToken(ctx, nil)
}

// promptToken prints guidance and, on an interactive terminal, accepts a new
// token, validates it against Cloudflare and stores it for future runs.
func (a *App) promptToken(ctx context.Context, invalidErr error) (*cf.Client, error) {
	if !isTTY() {
		if invalidErr != nil {
			return nil, fmt.Errorf("cloudflare auth: %w\n%s", invalidErr, tokenGuidance)
		}
		return nil, fmt.Errorf("cloudflare auth is not configured.\n%s", tokenGuidance)
	}
	fmt.Println("\nCloudflare API token is missing or invalid.")
	fmt.Print(tokenGuidance)
	fmt.Print("Paste the token and press Enter (Ctrl-C aborts): ")
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	tok := strings.TrimSpace(line)
	if tok == "" {
		return nil, errors.New("no token entered; aborting")
	}
	client, err := cf.New(ctx, tok)
	if err != nil {
		return nil, fmt.Errorf("token rejected by Cloudflare: %w\n%s", err, tokenGuidance)
	}
	if err := config.SaveTokenFile(tok); err != nil {
		return nil, err
	}
	a.cfg.CloudflareToken = tok
	fmt.Printf("token validated and saved to %s (owner-only)\n", config.TokenFilePath())
	return client, nil
}

func isTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// tokenGuidance explains how to create and store a Cloudflare API token.
const tokenGuidance = `Create a scoped Cloudflare API token:
  1) open https://dash.cloudflare.com/profile/api-tokens
  2) Create Token (or start from the "Edit zone DNS" template)
  3) Permissions:
       Zone - Zone - Read
       Zone - DNS - Edit
       Zone - Cache Purge - Purge
       Zone - Zone Settings - Read
     Zone Resources: Include -> All zones
  4) Create Token, copy the value.
Store it once:  cfddns token set <the-token>
`

func maskToken(tok string) string {
	if len(tok) <= 8 {
		return "••••••"
	}
	return tok[:6] + "••••" + tok[len(tok)-4:]
}

func (a *App) runToken(ctx context.Context, args []string) error {
	sub := "show"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "", "show", "status":
		if a.cfg.CloudflareToken != "" {
			src := "environment (CLOUDFLARE_API_TOKEN)"
			if !hasEnvToken() {
				src = config.TokenFilePath()
			}
			fmt.Printf("cloudflare token: %s (set)\n", maskToken(a.cfg.CloudflareToken))
			fmt.Printf("source: %s\n", src)
		} else {
			fmt.Printf("cloudflare token: not set (%s)\n", config.TokenFilePath())
			fmt.Print(tokenGuidance)
		}
		return nil
	case "set":
		tok := ""
		if len(args) > 1 {
			tok = strings.TrimSpace(args[1])
		} else if isTTY() {
			fmt.Print("Paste the Cloudflare API token (see guidance in `cfddns token`) and press Enter: ")
			reader := bufio.NewReader(os.Stdin)
			line, _ := reader.ReadString('\n')
			tok = strings.TrimSpace(line)
		} else {
			in, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			tok = strings.TrimSpace(string(in))
		}
		if tok == "" {
			return errors.New("empty token; usage: cfddns token set <token> (or pipe it on stdin)")
		}
		if _, err := cf.New(ctx, tok); err != nil {
			return fmt.Errorf("token rejected by Cloudflare: %w\n%s", err, tokenGuidance)
		}
		if err := config.SaveTokenFile(tok); err != nil {
			return err
		}
		a.cfg.CloudflareToken = tok
		fmt.Printf("token validated and saved to %s (owner-only)\n", config.TokenFilePath())
		return nil
	case "rm", "remove", "clear":
		if err := config.RemoveTokenFile(); err != nil {
			return err
		}
		if hasEnvToken() {
			fmt.Println("stored token removed; note: CLOUDFLARE_API_TOKEN is still set in the environment")
		} else {
			fmt.Println("token removed")
		}
		return nil
	case "test":
		if a.cfg.CloudflareToken == "" {
			fmt.Println("no token configured")
			fmt.Print(tokenGuidance)
			return nil
		}
		if _, err := cf.New(ctx, a.cfg.CloudflareToken); err != nil {
			return fmt.Errorf("token invalid: %w\n%s", err, tokenGuidance)
		}
		fmt.Println("token valid")
		return nil
	default:
		return fmt.Errorf("unknown token subcommand %q (want set, show, rm, test)", sub)
	}
}

func hasEnvToken() bool {
	return os.Getenv("CLOUDFLARE_API_TOKEN") != ""
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
	// forms: track <zone> on|off | track <zone> <name> on|off |
	//        track <zone> <name> <content> on|off (disambiguate dual records)
	if len(args) < 2 || len(args) > 4 {
		return errors.New("usage: cfddns track <zone> [name] [content] on|off")
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
	var content string
	if len(args) >= 3 {
		name = args[1]
	}
	if len(args) == 4 {
		content = args[2]
	}

	st, closeFn, err := a.openStore(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	dz, ok, err := st.ZoneByName(ctx, zone)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("zone %q not in the local DB; run `cfddns sync` first", zone)
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
	recs, err := st.ListRecords(ctx, dz.ID)
	if err != nil {
		return err
	}
	var matches []store.Record
	for _, r := range recs {
		if strings.EqualFold(r.Name, target) {
			if content != "" && r.Content != content {
				continue
			}
			matches = append(matches, r)
		}
	}
	if len(matches) == 0 {
		if content != "" {
			return fmt.Errorf("no record %q with content %q in zone %s", target, content, zone)
		}
		return fmt.Errorf("no record %q in zone %s", target, zone)
	}
	if len(matches) > 1 {
		return fmt.Errorf("multiple records at %q in %s; disambiguate by content: cfddns track <zone> <name> <content> on|off", target, zone)
	}
	rec := matches[0]
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

// runZones lists zones (optionally a single zone's detail). Zone membership
// itself is managed on the Cloudflare website; this is a read-only view.
func (a *App) runZones(ctx context.Context, args []string) error {
	st, closeFn, err := a.openStore(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	if len(args) == 1 {
		dz, ok, err := st.ZoneByName(ctx, args[0])
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("zone %q not in the local DB; run `cfddns sync` or add it in the Cloudflare dashboard", args[0])
		}
		recs, err := st.ListRecords(ctx, dz.ID)
		if err != nil {
			return err
		}
		byType := map[string]int{}
		tracked, off := 0, 0
		for _, r := range recs {
			if r.Status != store.StatusOn {
				off++
				continue
			}
			byType[r.Type]++
			if r.Type == "A" && r.TrackIP {
				tracked++
			}
		}
		types := make([]string, 0, len(byType))
		for t, n := range byType {
			types = append(types, fmt.Sprintf("%s:%d", t, n))
		}
		sort.Strings(types)
		fmt.Printf("zone:        %s\n", dz.Name)
		fmt.Printf("zone id:     %s\n", dz.ZoneID)
		fmt.Printf("registrar:   %s\n", dz.Registrar)
		fmt.Printf("status:      %s\n", dz.Status)
		fmt.Printf("records:     %s\n", strings.Join(types, " "))
		if off > 0 {
			fmt.Printf("(plus %d disabled records in the local DB)\n", off)
		}
		fmt.Printf("tracked A:   %d\n", tracked)
		return nil
	}
	if len(args) > 1 {
		return errors.New("usage: cfddns zones [<zone>]")
	}

	zones, err := st.ListZones(ctx)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(zones))
	for _, z := range zones {
		names = append(names, z.Name)
	}
	sort.Strings(names)
	fmt.Printf("%-32s %-34s %-10s %-4s %s\n", "NAME", "ZONEID", "REGISTRAR", "ST", "RECORDS")
	zoneByID := make(map[string]store.Zone, len(zones))
	for _, z := range zones {
		zoneByID[z.Name] = z
	}
	for _, name := range names {
		z := zoneByID[name]
		recs, err := st.ListRecords(ctx, z.ID)
		if err != nil {
			return err
		}
		on := 0
		for _, r := range recs {
			if r.Status == store.StatusOn {
				on++
			}
		}
		fmt.Printf("%-32s %-34s %-10s %-4s %d\n", z.Name, z.ZoneID, z.Registrar, z.Status, on)
	}
	return nil
}

// runDNS manages DNS records of a zone. Both "dns <zone> ..." and
// "dns add <zone> ..." spellings are accepted.
func (a *App) runDNS(ctx context.Context, args []string, o cmdOpts) error {
	if len(args) == 0 {
		return errors.New("usage: cfddns dns <zone> [add|update|rm ...]")
	}
	var sub string
	zone := args[0]
	switch args[0] {
	case "add", "update", "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: cfddns dns %s <zone> <name> [...]", args[0])
		}
		sub, zone = args[0], args[1]
	default:
		if len(args) > 1 {
			switch args[1] {
			case "add", "update", "rm":
				sub = args[1]
			}
		}
	}

	client, st, closeFn, err := a.open(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	dz, ok, err := st.ZoneByName(ctx, zone)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("zone %q not in the local DB; run `cfddns sync` first", zone)
	}
	if dz.ZoneID == "" {
		return fmt.Errorf("zone %q has no Cloudflare zone id; run `cfddns sync`", zone)
	}

	switch sub {
	case "":
		return a.listZoneRecords(ctx, st, dz, o.typ, o.all)
	case "add":
		return a.dnsAdd(ctx, client, st, dz, args[len(args)-3:], o)
	case "update":
		return a.dnsUpdate(ctx, client, st, dz, args[len(args)-1:], o)
	case "rm":
		return a.dnsRm(ctx, client, st, dz, args[len(args)-1:], o)
	}
	return nil
}

func (a *App) listZoneRecords(ctx context.Context, st *store.Store, dz store.Zone, typ string, all bool) error {
	recs, err := st.ListRecords(ctx, dz.ID)
	if err != nil {
		return err
	}
	if !all {
		filtered := recs[:0]
		for _, r := range recs {
			if r.Status == store.StatusOn {
				filtered = append(filtered, r)
			}
		}
		recs = filtered
	}
	if typ != "" {
		filtered := recs[:0]
		for _, r := range recs {
			if strings.EqualFold(r.Type, typ) {
				filtered = append(filtered, r)
			}
		}
		recs = filtered
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Name < recs[j].Name })
	fmt.Printf("%-6s %-42s %-24s %-6s %-7s %s\n", "TYPE", "NAME", "CONTENT", "TTL", "PROXY", "TRACK")
	for _, r := range recs {
		proxy := "-"
		if r.Proxied {
			proxy = "proxied"
		}
		tv := "-"
		if r.Type == "A" {
			tv = "off"
			if r.TrackIP {
				tv = "on"
			}
		}
		fmt.Printf("%-6s %-42s %-24s %-6d %-7s %s\n", r.Type, r.Name, r.Content, r.TTL, proxy, tv)
	}
	return nil
}

func (a *App) dnsAdd(ctx context.Context, client *cf.Client, st *store.Store, dz store.Zone, args []string, o cmdOpts) error {
	if len(args) != 3 {
		return errors.New("usage: cfddns dns add <zone> <TYPE> <name> <content> [--ttl N] [--proxy|--no-proxy] [--prio N]")
	}
	typ := strings.ToUpper(args[0])
	if !isSupportedType(typ) {
		return fmt.Errorf("unsupported type %q (supported: %s)", typ, strings.Join(cf.SupportedRecordTypes(), ", "))
	}
	name := service.FQDNHost(args[1], dz.Name)
	content := args[2]

	proxied := typ == "A" || typ == "AAAA" || typ == "CNAME"
	switch {
	case o.proxy:
		proxied = true
	case o.noProxy:
		proxied = false
	}
	if proxied && (typ == "MX" || typ == "TXT") {
		return fmt.Errorf("%s records cannot be proxied", typ)
	}
	ttl := o.ttl
	if ttl == 0 {
		ttl = 1
	}
	rec := cf.Record{ZoneID: dz.ZoneID, Type: typ, Name: name, Content: content,
		Proxied: proxied, TTL: ttl, Priority: o.prio}

	if o.dryRun {
		fmt.Printf("would create %s %s %s (%s ttl=%d)%s\n", typ, name, content,
			proxiedLabel(proxied), ttl, prioSuffix(o.prio))
		return nil
	}
	created, err := client.CreateDNSRecord(ctx, dz.ZoneID, rec)
	if err != nil {
		return err
	}

	// Mirror the new record; A records of managed zones are tracked by default.
	mr := store.Record{
		Type: created.Type, Name: created.Name, Content: created.Content,
		Proxied: created.Proxied, TTL: created.TTL, Priority: created.Priority, RecordID: created.ID,
	}
	if err := upsertRecord(ctx, st, dz.ID, mr); err != nil {
		return err
	}
	managed := dz.Status == store.StatusOn && dz.Registrar == "cloudflare"
	if managed && created.Type == "A" {
		dr, ok, err := st.FindRecordByName(ctx, dz.ID, created.Name)
		if err != nil {
			return err
		}
		if ok {
			if err := st.SetRecordTrack(ctx, dr.ID, true); err != nil {
				return err
			}
		}
	}
	fmt.Printf("created %s %s %s (record %s)\n", created.Type, created.Name, created.Content, created.ID)
	if managed && created.Type == "A" {
		fmt.Println("tracked: on")
	}
	return nil
}

func (a *App) dnsUpdate(ctx context.Context, client *cf.Client, st *store.Store, dz store.Zone, args []string, o cmdOpts) error {
	if len(args) != 1 {
		return errors.New("usage: cfddns dns update <zone> <name> [--content X] [--ttl N] [--proxy|--no-proxy] [--prio N]")
	}
	target := service.FQDNHost(args[0], dz.Name)
	dr, ok, err := st.FindRecordByName(ctx, dz.ID, target)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no record %q in zone %s", target, dz.Name)
	}

	rec := cf.Record{ID: dr.RecordID, ZoneID: dz.ZoneID, Type: dr.Type, Name: dr.Name,
		Content: dr.Content, Proxied: dr.Proxied, TTL: dr.TTL, Priority: dr.Priority}
	changed := false
	if o.ttl > 0 && o.ttl != rec.TTL {
		rec.TTL = o.ttl
		changed = true
	}
	if o.proxy || o.noProxy {
		p := o.proxy
		if p != rec.Proxied {
			rec.Proxied = p
			changed = true
		}
	}
	if o.prio != 0 && o.prio != rec.Priority {
		rec.Priority = o.prio
		changed = true
	}
	if o.content != "" && o.content != rec.Content {
		rec.Content = o.content
		changed = true
	}
	if !changed {
		return errors.New("nothing to change (give --content / --ttl / --proxy / --no-proxy / --prio with a new value)")
	}

	if o.dryRun {
		fmt.Printf("would update %s %s -> %s ttl=%d proxied=%v prio=%d\n",
			rec.Type, rec.Name, rec.Content, rec.TTL, rec.Proxied, rec.Priority)
		return nil
	}
	updated, err := client.UpdateDNSRecord(ctx, rec)
	if err != nil {
		return err
	}
	mr := store.Record{
		Type: updated.Type, Name: updated.Name, Content: updated.Content,
		Proxied: updated.Proxied, TTL: updated.TTL, Priority: updated.Priority, RecordID: updated.ID,
	}
	if err := st.UpdateRecord(ctx, dr.ID, mr); err != nil {
		return err
	}
	fmt.Printf("updated %s %s -> %s ttl=%d proxied=%v (track preserved)\n",
		updated.Type, updated.Name, updated.Content, updated.TTL, updated.Proxied)
	return nil
}

func (a *App) dnsRm(ctx context.Context, client *cf.Client, st *store.Store, dz store.Zone, args []string, o cmdOpts) error {
	if len(args) != 1 {
		return errors.New("usage: cfddns dns rm <zone> <name> -y")
	}
	if !o.yes {
		return errors.New("this deletes a Cloudflare record; pass -y to confirm")
	}
	target := service.FQDNHost(args[0], dz.Name)
	dr, ok, err := st.FindRecordByName(ctx, dz.ID, target)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no record %q in zone %s", target, dz.Name)
	}
	if o.dryRun {
		fmt.Printf("would delete %s %s (%s)\n", dr.Type, dr.Name, dr.Content)
		return nil
	}
	if err := client.DeleteDNSRecord(ctx, dz.ZoneID, dr.RecordID); err != nil {
		return err
	}
	if err := st.SetRecordStatus(ctx, dr.ID, store.StatusOff); err != nil {
		return err
	}
	fmt.Printf("deleted %s %s (%s); local row soft-disabled\n", dr.Type, dr.Name, dr.Content)
	return nil
}

func (a *App) runStatus(ctx context.Context) error {
	st, closeFn, err := a.openStore(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	home, err := a.detectIP(ctx)
	homeStr := "detection failed"
	if err == nil {
		homeStr = home.String()
	}
	last, known, err := st.GetState(ctx, store.StateKeyLastIP)
	if err != nil {
		return err
	}
	zones, err := st.ListZones(ctx)
	if err != nil {
		return err
	}
	managed, off := 0, 0
	for _, z := range zones {
		if z.Status == store.StatusOn && z.Registrar == "cloudflare" {
			managed++
		} else if z.Status == store.StatusOff {
			off++
		}
	}
	tracked, untracked, err := st.CountManagedATrack(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("home IP (detected):  %s\n", homeStr)
	fmt.Printf("last_known_ip:       %s\n", knownLastIP(last, known))
	fmt.Printf("cloudflare token:    %s\n", a.tokenState())
	fmt.Printf("managed zones:       %d  (registrar=cloudflare, status=on)\n", managed)
	fmt.Printf("other/off zones:     %d\n", off)
	fmt.Printf("A records tracked:   %d  (managed zones, track=on)\n", tracked)
	fmt.Printf("A records excepted:  %d  (managed zones, track=off)\n", untracked)
	return nil
}

// tokenState describes where the API token currently comes from.
func (a *App) tokenState() string {
	if a.cfg.CloudflareToken == "" {
		return "missing (run `cfddns token set`)"
	}
	if hasEnvToken() {
		return "set (environment)"
	}
	if config.HasTokenFile() {
		return "set (stored: " + config.TokenFilePath() + ")"
	}
	return "set"
}

// --- helpers ---

func upsertRecord(ctx context.Context, st *store.Store, domainID int64, r store.Record) error {
	recs, err := st.ListRecords(ctx, domainID)
	if err != nil {
		return err
	}
	for _, dr := range recs {
		if dr.RecordID == r.RecordID {
			return st.UpdateRecord(ctx, dr.ID, r)
		}
	}
	_, err = st.InsertRecord(ctx, domainID, r)
	return err
}

func isSupportedType(typ string) bool {
	for _, t := range cf.SupportedRecordTypes() {
		if t == typ {
			return true
		}
	}
	return false
}

func proxiedLabel(p bool) string {
	if p {
		return "proxied"
	}
	return "direct"
}

func prioSuffix(p int) string {
	if p != 0 {
		return fmt.Sprintf(" prio=%d", p)
	}
	return ""
}

func knownLastIP(v string, known bool) string {
	if !known || v == "" {
		return "unknown"
	}
	return v
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
