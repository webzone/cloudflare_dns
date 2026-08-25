# cfddns — Cloudflare DNS mirror & dynamic DNS

Replaces the legacy PHP scripts (`updateDNS.php`, `getDomains.php`,
`setZoneSettings.php`, `purge_all_caches.php`) with a single Go binary.
**Cloudflare is the single source of truth**; a local MariaDB mirror is kept
in line with it so LAN tooling can read DNS state without hitting the API.

## Layout

```
cmd/cfddns/          entry point (flock singleton guard)
internal/config/     env loading + validation
internal/ip/         multi-source public-IP detection
internal/cf/         cloudflare-go v7 wrapper (zones, records, cache purge)
internal/store/      MariaDB mirror + embedded SQL migrations
internal/service/    sync (CF→DB + base-record completion), update-ip, init,
                     track, purge
internal/cli/        subcommand dispatch + flags
deploy/              systemd units + env example + logrotate
migrations embedded  internal/store/migrations/
```

cfddns [--dry-run] update-ip            reconcile zone set (init new zones,
                                        deregister zones that vanished), then
                                        move tracked A records: Cloudflare
                                        first, mirror after success
cfddns [--dry-run] init <zone> [--wildcard]
                                        create/mirror base A records (@ and www,
                                        + * with --wildcard) at the home IP
cfddns track <zone> [name] on|off       mark A record(s) as following (on) or
                                        not following (off) the home IP; name is
                                        @, *, www or any FQDN (no name = zone)
cfddns [--dry-run] purge [zone]         purge edge cache (all managed zones or
                                        one named zone)
cfddns zones [<zone>]                   list zones (or one zone's detail)
cfddns dns <zone> [--type T] [--all]    list a zone's records (track column;
                                        live records only unless --all)
cfddns dns add <zone> <TYPE> <name> <content> [--ttl N] [--proxy|--no-proxy] [--prio N]
                                        create A/AAAA/CNAME/MX/TXT record;
                                        A records of managed zones are tracked
cfddns dns update <zone> <name> [--content X] [--ttl N] [--proxy|--no-proxy] [--prio N]
                                        change fields of an existing record
cfddns dns rm <zone> <name> -y          delete a record (mirror soft-disables)
cfddns status                           overview: home IP, mirror, track counts
cfddns help                             show help
```

Zones (domains) are added/removed **only on the Cloudflare website**
(dash.cloudflare.com) — cfddns reads the zone list and manages DNS records
inside zones; it never creates or deletes zones itself. New zones appear in
the mirror after `sync`, or within 5 minutes via `update-ip`'s reconcile,
which initializes the standard base records automatically.

`--dry-run` (or env `CF_DDNS_DRY_RUN=1`) logs every planned change and writes
nothing — run it before any real run.
cfddns [--dry-run] update-ip            A records tracking the home IP:
                                        Cloudflare first, mirror after success
cfddns [--dry-run] init <zone> [--wildcard]
                                        create/mirror base A records (@ and www,
                                        + * with --wildcard) at the home IP
cfddns track <zone> [name] on|off       mark A record(s) as following (on) or
                                        not following (off) the home IP; name is
                                        @, *, www or any FQDN (no name = zone)
cfddns [--dry-run] purge [zone]         purge edge cache (all managed zones or
                                        one named zone)
cfddns inspect zones                    list Cloudflare zones
cfddns inspect records <zone>           list a zone's records (with track=…)
cfddns help                             show help
```

`--dry-run` (or env `CF_DDNS_DRY_RUN=1`) logs every planned change and writes
nothing — run it before any real run.

## Semantics

- **`registrar` column** — "which registrar manages this domain". Every zone
  seen on Cloudflare is marked `registrar='cloudflare'` by sync; that value is
  the domain-level membership marker, and **all operations (`sync` record
  mirroring, `update-ip`, `purge`) first select only
  `registrar='cloudflare'` zones**. Domains not present on Cloudflare keep
  their old value, get `status='off'` and are never operated on.
- **`sync`** — lists all zones/records from Cloudflare and upserts the mirror
  keyed by Cloudflare record ID; zones/records absent from Cloudflare are
  marked `status='off'` (never deleted). Record names are stored as FQDNs
  exactly as Cloudflare returns them; TTL 1 = "automatic".
- **`track` flag** (`dns.track_ip`, the record-level gate) — **default is
  managed**: every A record of a `registrar='cloudflare'` domain is `track=1`
  (follows the home IP) unless explicitly marked `track=0` via
  `cfddns track ... off`, which is the special-exception case (records serving
  another server are mirrored but never rewritten to the home IP). `sync`
  never flips an existing flag; records it mirrors in a managed domain are
  born `track=1`.
- **Base-record auto-completion** — every managed zone is guaranteed the
  standard `@` / `www` / `*` A records: `init <zone>` creates them for a new
  zone; `sync` detects and creates any of the three that are entirely absent,
  pointing at the current home IP (proxied, auto TTL). Existing records at
  those names — whatever they point at — are never touched by sync.
- **`update-ip`** — detects the public IP **once** per run from 3 independent
  HTTPS sources (two must agree, else the run is skipped), then reconciles the
  zone set: a zone newly seen on Cloudflare is initialized (base `@`/`www`/`*`
  records at the home IP, tracked, mirrored) and a managed zone that vanished
  from Cloudflare is deregistered (`registrar` → `other`, `status` → `off`).
  Unchanged IP → immediate exit with **zero remaining** Cloudflare API calls.
  On a change it compares every `track=1` record (already filtered to
  `registrar='cloudflare'` domains) straight against the local mirror, skips
  records already at the new IP, updates **Cloudflare first**, and only after
  each success writes the mirror. `last_known_ip` in `app_state` only advances
  when every differing record succeeded, so failures retry next run.
- **`init <zone>`** — for a domain just added to Cloudflare: detects the
  public IP once, creates/updates the base A records (`@`, `www`, and `*`
  with `--wildcard`) proxied with auto TTL, mirrors them and flags them
  tracked, then seeds `last_known_ip`. Idempotent; safe to re-run.
- **`purge`** — purges all cached content for one managed zone or every
  managed zone (non-`cloudflare` zones are refused).
- **Migrations** — embedded SQL runs automatically at startup; each file is
  recorded in `schema_migrations` and applied at most once. MariaDB-only
  syntax (`IF NOT EXISTS`); the target DB is MariaDB 10.11.

## Configuration

Values come from the environment, optionally overlaid from the first existing
env file (dotenv syntax, existing environment variables win): `$CFDDNS_ENV_FILE`
→ `./.env` → `/etc/cfddns/cfddns.env` → `~/.cfddns.env`. This makes manual
runs (`cfddns sync`) work without sourcing anything, matching how the systemd
units inject the same file.

| Variable | Required | Purpose |
|---|---|---|
| `CLOUDFLARE_API_TOKEN` | one auth | scoped API token (recommended) |
| `CLOUDFLARE_API_EMAIL` + `CLOUDFLARE_API_KEY` | one auth | legacy global key (dev fallback) |
| `MYSQL_HOST` / `MYSQL_PORT` / `MYSQL_USER` / `MYSQL_PASSWORD` / `MYSQL_DB` | yes | mirror database |
| `LOG_LEVEL` | no | debug \| info \| warn \| error (default info) |
| `CF_DDNS_DRY_RUN` | no | force dry-run |
| `CF_DDNS_TEST_IP` | no | force an IP for testing (use with `--dry-run`) |

`inspect zones` needs only Cloudflare auth; everything else needs both.

## Deployment (Ubuntu — hivediskless)

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o cfddns ./cmd/cfddns
sudo install -m 0755 cfddns /usr/local/bin/cfddns
sudo mkdir -p /etc/cfddns /var/log/cfddns
sudo install -m 0600 -o root -g root deploy/cfddns.env.example /etc/cfddns/cfddns.env
sudo install -m 0644 deploy/logrotate/cfddns /etc/logrotate.d/cfddns
# fill /etc/cfddns/cfddns.env (real token/password), then:
sudo install -m 0644 deploy/systemd/*.service deploy/systemd/*.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now cfddns-update.timer cfddns-sync.timer
tail -f /var/log/cfddns/cfddns.log
```

Logs go to `/var/log/cfddns/cfddns.log` (journald stdout capture is
unreliable on hivediskless); logrotate rotates daily, 14 copies kept.

Timers: `update-ip` every 5 minutes, `sync` daily at 04:00. The binary takes
an advisory flock against overlapping runs.

### Replacing the legacy PHP cron

The old stack ran `/usr/bin/php /home/user/cloudflare/updateDNS.php` every
minute and force-rewrote *every* A record of `registrar='cloudflare'` zones
to the home IP, including records that legitimately pointed elsewhere.
Cutover:
1. Disable the old cron (backup at `/home/user/crontab.bak`).
2. Install the Go binary + timers above.
3. Keep `/home/user/cloudflare` (PHP) as a frozen reference until the new
   stack has run for a full cycle, then retire it.

## Security notes

- The legacy PHP code carried the Cloudflare **global API key** and MariaDB
  password as plaintext in five files and in git history. Rotate to a scoped
  API token (`Zone.Zone:Read`, `Zone.DNS:Edit`, `Zone.Cache:Purge`,
  `Zone.Settings:Edit`) and a dedicated low-privilege MariaDB user before
  exposing this repo further. `/etc/cfddns/cfddns.env` must stay root-only.
- All SQL in the Go code is parameterized; the only multi-statement DSN flag
  exists for the embedded migrations.
