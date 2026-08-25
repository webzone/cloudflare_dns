# cfddns — Cloudflare domain management & dynamic IP update

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/webzone/cloudflare_dns)](https://goreportcard.com/report/github.com/webzone/cloudflare_dns)
[![Go Reference](https://pkg.go.dev/badge/github.com/webzone/cloudflare_dns.svg)](https://pkg.go.dev/github.com/webzone/cloudflare_dns)

A single static Go binary with two jobs:

1. **Domain management** — `zones`, `dns` (list/add/update/rm), `track`,
   `init`, `sync`, `purge`.
2. **Dynamic IP update** — `update-ip` keeps your A records pointing at
   your current public IP.

Cloudflare is the single source of truth; every command works straight
against the live API, and all state (zones, records, flags, last-known IP)
is recorded in a single local **SQLite file** — fast, offline-friendly, with
no external database server. The binary is standalone (Linux / macOS /
Windows, static, CGO-free) — only network access, a Cloudflare API token,
and an optional SQLite file path are needed.

Zones are added/removed **only on the Cloudflare website**
(dash.cloudflare.com) — cfddns never creates or deletes zones; it detects
zone changes and reacts (initializes new zones, deregisters zones that
vanished).

## Features

- `update-ip` — dynamic DNS. Detects your public IP once per run (3
  independent sources, 2 must agree), moves every tracked A record to it
  (Cloudflare first, local DB after each success), **zero API calls** when
  the IP is unchanged. Also reconciles the zone set: new zones get
  initialized, zones that vanished get deregistered.
- `sync` — records all Cloudflare zones and records into the local SQLite
  store; marks present zones `registrar=cloudflare`; auto-creates any missing
  `@`/`www`/`*` A records at the home IP.
- `track` — per-record "follow the home IP" flag. **Default is managed**:
  every A record of a managed zone follows the home IP unless explicitly
  untracked.
- Management CLI — `zones`, `dns` (list/add/update/rm), `status`, `init`,
  `purge`.

## Install

Build from source (Go ≥ 1.21) — the result is a static standalone binary:

```sh
git clone git@github.com:webzone/cloudflare_dns.git
cd cloudflare_dns
go build -o cfddns ./cmd/cfddns                       # your OS/arch
cfddns version                                       # "dev" unless stamped
```

Or install the tagged release directly (version baked in):

```sh
go install github.com/webzone/cloudflare_dns/cmd/cfddns@v0.1.0
```

To stamp the version manually:

```sh
go build -ldflags "-s -w -X github.com/webzone/cloudflare_dns/internal/cli.Version=v0.1.0" -o cfddns ./cmd/cfddns
```

Cross-compile (each produces a standalone binary):

```sh
GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o cfddns-linux-amd64  ./cmd/cfddns
GOOS=linux   GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-s -w" -o cfddns-linux-arm64  ./cmd/cfddns
GOOS=darwin  GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o cfddns-darwin-amd64 ./cmd/cfddns
GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-s -w" -o cfddns-darwin-arm64 ./cmd/cfddns
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o cfddns.exe           ./cmd/cfddns
```

Put it on `PATH` (`sudo install -m 0755 cfddns /usr/local/bin/`).

### Linux server with systemd timers (recommended)

```sh
sudo mkdir -p /etc/cfddns /var/log/cfddns /var/lib/cfddns
sudo install -m 0600 -o root -g root deploy/cfddns.env.example /etc/cfddns/cfddns.env
sudo install -m 0644 deploy/logrotate/cfddns /etc/logrotate.d/cfddns
# 1. edit /etc/cfddns/cfddns.env: real credentials + CFDDNS_DB path
# 2. install units:
sudo install -m 0644 deploy/systemd/*.service deploy/systemd/*.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now cfddns-update.timer cfddns-sync.timer
tail -f /var/log/cfddns/cfddns.log
```

- `cfddns-update.timer`: dynamic-IP update every 5 minutes (2 min after boot).
- `cfddns-sync.timer`: daily 04:00 (`Persistent`, catches up after downtime).

Other schedulers work identically (cron, launchd, Task Scheduler calling
`cfddns update-ip` every 5 minutes + `cfddns sync` daily); the binary has its
own overlap lock.

### macOS / Windows

```sh
cp deploy/cfddns.env.example ~/.cfddns.env   # Windows: %USERPROFILE%\.cfddns.env
# fill with credentials + CFDDNS_DB (Windows example: C:\Data\cfddns.db)
cfddns status        # verify auth + local DB
cfddns --dry-run sync
cfddns sync
# schedule cfddns update-ip (launchd / Task Scheduler)
```

Env-file lookup order: `$CFDDNS_ENV_FILE` → `./.env` →
`/etc/cfddns/cfddns.env` (Linux; if readable) → `~/.cfddns.env`
(`%USERPROFILE%\.cfddns.env` on Windows). Already-set environment variables
win over the file.

## Configure

Create a scoped Cloudflare API token (dash.cloudflare.com → My Profile → API
Tokens → Create Token): permissions `Zone.Zone:Read`, `Zone.DNS:Edit`,
`Zone.Cache:Purge`, `Zone.Settings:Edit`, resource "All zones".

```
CLOUDFLARE_API_TOKEN=<scoped token>     # preferred auth
# legacy: CLOUDFLARE_API_EMAIL + CLOUDFLARE_API_KEY
CFDDNS_DB=/var/lib/cfddns/cfddns.db     # SQLite database file (default ./cfddns.db)
LOG_LEVEL=info                          # debug|info|warn|error
# CF_DDNS_DRY_RUN=1                     # force dry-run everywhere
# CF_DDNS_TEST_IP=1.2.3.4               # testing only (with --dry-run)
```

The SQLite schema is created automatically on first run — no manual DDL.

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
#   soft-disabled history in the local DB

cfddns dns add example.com A www 1.2.3.4 [--ttl 300] [--proxy|--no-proxy]
cfddns dns add example.com MX @ mail.example.com --prio 10
cfddns dns add example.com TXT _dmarc "v=DMARC1; p=none"
#   supported types: A, AAAA, CNAME, MX, TXT. Writes Cloudflare first, then
#   records them in the local DB. A records of managed zones are born track=on.

cfddns dns update example.com www --content 5.6.7.8 [--ttl 300] [--no-proxy]
#   change content / ttl / proxy / prio; track flag is preserved

cfddns dns rm example.com www -y
#   deletes the record on Cloudflare; the local row is soft-disabled
```

### Automation

```sh
cfddns sync                                  # align the local DB with Cloudflare (idempotent)
cfddns update-ip                             # dynamic DNS + zone reconcile
cfddns init example.com [--wildcard]         # base @/www (+ *) at home IP
cfddns track example.com on|off              # whole zone follows home IP
cfddns track example.com www off             # one record (name: @, * or FQDN)
cfddns purge [example.com]                   # edge-cache purge (managed zones)
cfddns status                                # overview
```

## How it works

- **Single source of truth**: every operation reads/writes Cloudflare first;
  the local SQLite store only records writes that already succeeded.
- **`registrar` column** = "which registrar manages this domain". `sync`
  marks every zone seen on Cloudflare as `cloudflare`; all operations
  (`sync`, `update-ip`, `purge`) first select only `registrar=cloudflare`
  zones. Members that vanish from Cloudflare are deregistered
  (`registrar -> other`, `status -> off`) by `update-ip` within minutes or by
  the daily `sync`.
- **`track` flag** (`dns.track_ip`): per-record gate within a managed domain.
  Default is ON — records serving another server stay tracked unless you
  `track ... off`, which is the explicit exception mechanism.
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

- Use a scoped API token, never the account global key.
- The SQLite store contains a copy of your DNS but no Cloudflare secrets;
  `/etc/cfddns/cfddns.env` must stay root-only, and the database file should
  be owner-only.
- Never commit real secrets (`.env*` and `*.db` are gitignored).
