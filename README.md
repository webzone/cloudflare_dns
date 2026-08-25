# cfddns — Cloudflare DNS mirror & domain management

A single static Go binary that replaces the legacy PHP scripts
(`updateDNS.php`, `getDomains.php`, `setZoneSettings.php`,
`purge_all_caches.php`). It keeps a local MariaDB mirror of your Cloudflare
DNS and manages your records: dynamic-DNS IP updates, per-record
home-IP tracking, and a full DNS management CLI.

**Cloudflare is the single source of truth.** Zones are added/removed **only
on the Cloudflare website** (dash.cloudflare.com) — cfddns never creates or
deletes zones; it detects zone changes and reacts (initializes new zones,
deregisters zones that vanished).

- Linux / macOS / Windows binaries built from one codebase (static, no runtime
  dependencies beyond network + optional MariaDB).

## Features

- `update-ip` — dynamic DNS. Detects your public IP once per run (3
  independent sources, 2 must agree), moves every tracked A record to it
  (Cloudflare first, mirror after each success), zero API calls when the IP
  is unchanged. Also reconciles the zone set each run.
- `sync` — mirrors all Cloudflare zones/records into MariaDB; marks present
  zones `registrar=cloudflare`; auto-creates any missing `@`/`www`/`*` A
  records at the home IP.
- `track` — per-record "follow the home IP" flag. **Default is managed**:
  every A record of a managed zone follows the home IP unless explicitly
  untracked.
- Management CLI — `zones`, `dns` (list/add/update/rm), `status`, `init`,
  `purge`.

## Install

No package manager yet — build from source (Go ≥ 1.21) or copy a prebuilt
binary. The tool is fully static; only `CF_DDNS_TEST_IP`-style env config and
(optionally) MariaDB are needed.

### Build for your platform

```sh
git clone git@github.com:webzone/cloudflare_dns.git   # private repo
cd cloudflare_dns
go build -o cfddns ./cmd/cfddns                       # matches your OS/arch
```

Cross-compile matrix (each produces a standalone binary):

```sh
GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o cfddns-linux-amd64  ./cmd/cfddns
GOOS=linux   GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-s -w" -o cfddns-linux-arm64  ./cmd/cfddns
GOOS=darwin  GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o cfddns-darwin-amd64 ./cmd/cfddns
GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-s -w" -o cfddns-darwin-arm64 ./cmd/cfddns
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o cfddns.exe           ./cmd/cfddns
```

Place it anywhere on `PATH` (`sudo install -m 0755 cfddns /usr/local/bin/`).

### Linux server (recommended: systemd timers)

```sh
sudo mkdir -p /etc/cfddns /var/log/cfddns
sudo install -m 0600 -o root -g root deploy/cfddns.env.example /etc/cfddns/cfddns.env
sudo install -m 0644 deploy/logrotate/cfddns /etc/logrotate.d/cfddns
# 1. fill /etc/cfddns/cfddns.env with your real credentials (below)
# 2. install units:
sudo install -m 0644 deploy/systemd/*.service deploy/systemd/*.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now cfddns-update.timer cfddns-sync.timer
tail -f /var/log/cfddns/cfddns.log
```

- `cfddns-update.timer`: every 5 minutes (2 min after boot).
- `cfddns-sync.timer`: daily 04:00 (`Persistent`, catches up after downtime).

Scheduling without systemd: cron / launchd / Task Scheduler calling
`cfddns update-ip` every 5 minutes and `cfddns sync` daily work the same way
(the binary has its own overlap lock).

### macOS / Windows (workstation or any always-on box)

```sh
# one-time: configure
cp deploy/cfddns.env.example ~/.cfddns.env      # macOS/Linux
# Windows: %USERPROFILE%\.cfddns.env
# fill with your credentials (below), then:
cfddns status                                    # verify auth + mirror
cfddns --dry-run sync                            # preview before first real run
cfddns sync
# schedule manually (launchd / Task Scheduler / cron): cfddns update-ip
```

`/etc/cfddns/cfddns.env` is Linux-specific; on any OS the env-file lookup is:
`$CFDDNS_ENV_FILE` → `./.env` → `/etc/cfddns/cfddns.env` (if readable) →
`~/.cfddns.env` (`%USERPROFILE%\.cfddns.env` on Windows). Already-set
environment variables always win over the file.

## Configure

Create a scoped Cloudflare API token (dash.cloudflare.com → My Profile → API
Tokens → Create Token): permissions `Zone.Zone:Read`,
`Zone.DNS:Edit`, `Zone.Cache:Purge`, `Zone.Settings:Edit`, resource
"All zones". Also create the MariaDB user/database for the mirror.

```
CLOUDFLARE_API_TOKEN=<scoped token>          # preferred auth
# legacy: CLOUDFLARE_API_EMAIL + CLOUDFLARE_API_KEY
MYSQL_HOST=192.168.2.246                     # mirror database
MYSQL_PORT=3306
MYSQL_USER=domain
MYSQL_PASSWORD=...
MYSQL_DB=domain
LOG_LEVEL=info                               # debug|info|warn|error
# CF_DDNS_DRY_RUN=1                          # force dry-run everywhere
# CF_DDNS_TEST_IP=1.2.3.4                    # testing only (with --dry-run)
```

The mirror schema (and all migrations) are created automatically on first
run; no manual DDL. MariaDB ≥ 10.5 or MySQL ≥ 8 recommended.

## Command reference

```
cfddns [--dry-run] <command> [args]
```

### Zones (read-only)

```sh
cfddns zones                  # name / zone id / registrar / status / record count
cfddns zones example.com      # detail: registrar, status, record counts, tracked A
```

### DNS records

```sh
cfddns dns example.com [--type A] [--all]
#   live records (type/name/content/ttl/proxy/track); --all includes
#   soft-disabled mirror history

cfddns dns add example.com A www 1.2.3.4 [--ttl 300] [--proxy|--no-proxy]
cfddns dns add example.com MX @ mail.example.com --prio 10
cfddns dns add example.com TXT _dmarc "v=DMARC1; p=none"
#   supported types: A, AAAA, CNAME, MX, TXT. Writes Cloudflare first, then
#   mirrors. A records of managed zones are born track=on.

cfddns dns update example.com www --content 5.6.7.8 [--ttl 300] [--no-proxy]
#   change content / ttl / proxy / prio; track flag is preserved

cfddns dns rm example.com www -y
#   deletes the record on Cloudflare; the mirror row is soft-disabled
```

### Automation

```sh
cfddns sync                                  # mirror CF → DB (idempotent)
cfddns update-ip                             # dynamic DNS + zone reconcile
cfddns init example.com [--wildcard]         # base @/www (+ *) at home IP
cfddns track example.com on|off              # whole zone follows home IP
cfddns track example.com www off             # one record (name: @, * or FQDN)
cfddns purge [example.com]                   # edge-cache purge (managed zones)
cfddns status                                # overview
```

## How it works

- **Single source of truth**: every operation reads/writes Cloudflare first;
  the MariaDB mirror only follows writes that already succeeded.
- **`registrar` column** = "which registrar manages this domain". `sync`
  marks every zone seen on Cloudflare as `cloudflare`; all operations
  (`sync`, `update-ip`, `purge`) first select only `registrar=cloudflare`
  zones. Members that vanish from Cloudflare are deregistered
  (`registrar → other`, `status → off`) — by `update-ip` within minutes or by
  the daily `sync`.
- **`track` flag** (`dns.track_ip`): per-record gate within a managed domain.
  Default is ON — records serving another server (e.g. `yin-du.com` →
  50.47.202.72) stay tracked unless you `track ... off`, which is why the
  flag is the explicit exception mechanism.
- **Base records**: every managed zone is guaranteed `@`/`www`/`*` A records
  at the home IP — `init` creates them, `sync` auto-completes any missing
  one, `update-ip` keeps their content current.
- **Safe by default**: `--dry-run` on all mutating commands; `dns rm`
  requires `-y`; no hard deletes (absent things are soft-disabled);
  multi-source IP detection with 2-of-3 agreement; an advisory flock prevents
  overlapping runs; all SQL is parameterized.
- **Migrations** run automatically at startup, each file applied at most
  once (`schema_migrations`).

## Security

- The legacy PHP code carried your Cloudflare **global API key** and MariaDB
  password as plaintext. Rotate to a scoped token + a dedicated,
  low-privilege MariaDB user before exposing this repo further.
- `/etc/cfddns/cfddns.env` must stay root-only; never commit real secrets
  (`.env*` are gitignored).
