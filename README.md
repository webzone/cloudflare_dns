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
internal/service/    sync (CF→DB), update-ip (CF first, then DB), purge
internal/cli/        subcommand dispatch + --dry-run
deploy/              systemd units + env example
migrations embedded  internal/store/migrations/
```

cfddns [--dry-run] init <zone> [--wildcard]
                                        create/mirror base A records (@ and www)
                                        at the home IP; --wildcard adds *
cfddns track <zone> [name] on|off       mark A record(s) as following (on) or
                                        not following (off) the home IP; name is
                                        @, *, www or any FQDN (no name = zone-wide)
cfddns [--dry-run] purge [zone]         purge edge cache (all or one zone)
cfddns inspect zones                    list Cloudflare zones
cfddns inspect records <zone>           list a zone's records (with track=…)
```

`--dry-run` (or env `CF_DDNS_DRY_RUN=1`) logs every planned change and writes
nothing — run it before any real run.

## Semantics

- **`sync`**: lists all zones/records from Cloudflare and upserts the mirror
  keyed by Cloudflare record ID. Zones/records absent from Cloudflare are
  marked `status='off'` (never deleted; hard deletion is a manual choice).
  Record names are stored as FQDNs exactly as Cloudflare returns them.
  TTL 1 = "automatic".
- **`track` flag** (`dns.track_ip`, default 0): the distinction between
  records this box owns and records that must never be touched. `update-ip`
  only ever considers A records with `track=1`. `sync` never flips the flag.
  A new zone's records are flagged by `init`; anything else (e.g.
  `changchun.51email.com`, which resolves to another server) stays `off` and
  will never be rewritten to the home IP.
- **`update-ip`**: detects the public IP **once** per run from 3 independent
  HTTPS sources (two must agree, else the run is skipped). When the IP is
  unchanged it exits immediately — **zero Cloudflare API calls**. On a
  change it compares every `track=1` record straight against the local mirror
  (records already at the new IP are skipped), updates **Cloudflare first**,
  and only after each success writes the mirror. `last_known_ip` in
  `app_state` only advances when every differing record succeeded, so
  failures retry next run.
- **`init <zone>`**: for a domain just added to Cloudflare — detects the
  public IP once, creates/updates the base A records (`@`, `www`, and `*`
  with `--wildcard`) proxied with auto TTL, mirrors them and flags exactly
  those records tracked, then seeds `last_known_ip`. Idempotent; safe to
  re-run.
- **`purge`**: purges all cached content for one zone or every active zone.
- Migrations (embedded SQL) run automatically at startup; each file is
  recorded in `schema_migrations` and applied at most once. MariaDB-only
  syntax (`IF NOT EXISTS`); the target DB is MariaDB 10.11.
cfddns [--dry-run] purge [zone]         purge edge cache (all or one zone)
cfddns inspect zones                    list Cloudflare zones
cfddns inspect records <zone>           list a zone's records
```

`--dry-run` (or env `CF_DDNS_DRY_RUN=1`) logs every planned change and writes
nothing — run it before any real run.

## Semantics

- **`sync`**: lists all zones/records from Cloudflare and upserts the mirror
  keyed by Cloudflare record ID. Zones/records absent from Cloudflare are
  marked `status='off'` (never deleted; hard deletion is a manual choice).
  Record names are stored as FQDNs exactly as Cloudflare returns them.
  TTL 1 = "automatic".
- **`update-ip`**: detects the public IP from 3 independent HTTPS sources
  (two must agree, else the run is skipped). On a change it finds A records
  in the mirror whose content equals the previous IP, updates **Cloudflare
  first**, and only after each success writes the mirror. The last-known IP
  is only advanced when every matched record succeeded, so failures retry.
  Records pointing at other IPs (e.g. `bear.365x.com → 173.49.252.60`) are
  never touched.
- **`purge`**: purges all cached content for one zone or every active zone.
- Migrations (embedded SQL) run automatically at startup; each file is
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

`inspect` needs only Cloudflare auth; DB commands need both.

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

`CF_DDNS_TEST_IP=1.2.3.4 cfddns --dry-run update-ip` forces the detector to a
fake address — use with `--dry-run` to exercise the IP-change path without
touching real DNS.

Timers: `update-ip` every 5 minutes, `sync` daily at 04:00. The binary takes
an advisory flock against overlapping runs.

### Replacing the legacy PHP cron

The old stack ran `/usr/bin/php /home/user/cloudflare/updateDNS.php` every
minute and force-rewrote *every* A record to the home IP, including records
that legitimately pointed elsewhere. Cutover:
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
