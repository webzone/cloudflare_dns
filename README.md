# cfddns — Cloudflare domain management & dynamic IP update

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/webzone/cloudflare_dns)](https://goreportcard.com/report/github.com/webzone/cloudflare_dns)
[![Go Reference](https://pkg.go.dev/badge/github.com/webzone/cloudflare_dns.svg)](https://pkg.go.dev/github.com/webzone/cloudflare_dns)

## Why cfddns?

* **Effortless Dynamic DNS**: Host websites or services on your home server
  or mobile laptop on the go. Automatically detects your public IP (2-of-3
  consensus) and keeps your site online wherever you move. Zero IP change =
  zero API calls.
* **Batch & Multi-Domain Ready**: Manage, track, and sync DNS records across
  all your Cloudflare zones with unified, intuitive CLI commands.
* **Zero Dependencies**: A static, single Go binary with an embedded SQLite
  state cache—no Docker, MariaDB, or config files required. Run
  `cfddns token set` and you're set.
* **Safe & Transparent**: Native `--dry-run` previews, non-destructive soft
  deletes, and automatic conflict detection for multiple A records.

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
  independent sources, 2 must agree), then compares **every `track=1` A
  record straight against that IP** and updates each one that differs
  (Cloudflare first, local DB after each success) — including records
  flagged tracked mid-IP-cycle. Records already at the IP are skipped, so
  nothing changed means **zero API calls**. A record Cloudflare refuses
  because an identical one already exists (dual-A pairs) is untracked
  automatically. Also reconciles the zone set: new zones get initialized,
  zones that vanished get deregistered.
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
sudo mkdir -p /var/log/cfddns
sudo install -m 0644 deploy/logrotate/cfddns /etc/logrotate.d/cfddns
# install units (no env file needed — defaults + token set take care of it):
sudo install -m 0644 deploy/systemd/*.service deploy/systemd/*.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now cfddns-update.timer cfddns-sync.timer
# one-time token (as the user the units run as):
cfddns token set
tail -f /var/log/cfddns/cfddns.log
```

- `cfddns-update.timer`: dynamic-IP update every 5 minutes (2 min after boot).
- `cfddns-sync.timer`: daily 04:00 (`Persistent`, catches up after downtime).

The units run `cfddns update-ip` / `cfddns sync`, as the `user` account by
default — the SQLite database and token live in that user's `~/.cfddns/`, so
no configuration file is required. Adjust `User=`/`Group=` if you run under a
different account.

Other schedulers work identically (cron, launchd, Task Scheduler calling
`cfddns update-ip` every 5 minutes + `cfddns sync` daily); the binary has its
own overlap lock.

### macOS / Windows

```sh
cfddns token set            # one-time; prompts for the token
cfddns status               # verify auth + local DB
cfddns --dry-run sync       # preview before the first real sync
cfddns sync
# schedule cfddns update-ip (launchd / Task Scheduler)
```

Everything (token + SQLite database) lives under `~/.cfddns/`
(`%USERPROFILE%\.cfddns\` on Windows) — nothing else needs to be created
manually.

## Configure

### Cloudflare API token — managed by cfddns

You do not need to edit any file. On first run (or when the token is missing
or invalid) cfddns prints guidance and, in a terminal, asks you to paste the
token; it validates it against Cloudflare and stores it owner-only:

```sh
cfddns token set <the-token>         # or just `cfddns token set` (prompts)
cfddns token                         # show state (masked) + source
cfddns token test                    # validate the current token
cfddns token rm                      # remove the stored token
```

It is stored at `~/.cfddns/token` (Windows: `%USERPROFILE%\.cfddns\token`;
override with `CFDDNS_TOKEN_FILE`) with 0600 permissions. The SQLite database
defaults to the same directory (`~/.cfddns/cfddns.db`; override with
`CFDDNS_DB`). Environment variables are still honored if set — explicit
conventions: `CLOUDFLARE_API_TOKEN` beats the stored token, which beats the
legacy `CLOUDFLARE_API_EMAIL` + `CLOUDFLARE_API_KEY`.

Create a token at dash.cloudflare.com → My Profile → API Tokens → Create
Token with permissions `Zone.Zone:Read`, `Zone.DNS:Edit`, `Zone.Cache:Purge`,
`Zone.Settings:Edit`, resource "All zones".

### Other settings (all optional)

```
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

#### List

```sh
cfddns dns example.com               # live records (type/name/content/ttl/proxy/track)
cfddns dns example.com --type A      # A records only
cfddns dns example.com --all         # include soft-disabled (deleted) history
```

#### Add

```sh
cfddns dns add example.com A www             # home IP, track=on, proxied
cfddns dns add example.com A '*'             # wildcard — quote the star
cfddns dns add example.com A mail 10.0.0.5   # explicit IP, track=off
cfddns dns add example.com A lb 203.0.113.10 --no-proxy
cfddns dns add example.com MX @ mail.example.com --prio 10
cfddns dns add example.com TXT _dmarc "v=DMARC1; p=none"
```

An A record without an IP uses the current public IP and is born `track=on`
(it follows home, proxied); an explicit IP is used verbatim and born
`track=off`. A private IP that Cloudflare refuses to proxy falls back to
direct automatically.

#### Update

```sh
cfddns dns update example.com www --content 5.6.7.8            # single-record name
cfddns dns update example.com www --ttl 300 --no-proxy
cfddns dns update example.com <record-id> --content 5.6.7.8    # dual-A names
```

For a name carrying several records, `--content` is the new value and cannot
double as a selector; pass the Cloudflare record id instead (shown by the rm
picker and in ambiguity errors).

#### Delete

```sh
cfddns dns rm example.com www -y         # single record
cfddns dns rm example.com @ -y           # apex: several records → pick one
                                         # (Enter=1, digit, 'a'=all)
cfddns dns rm example.com @ 10.0.0.5 -y  # pick one of several by content
cfddns dns rm example.com '*' --all -y   # delete every record at '*'
```

Deletes hit Cloudflare first, then soft-disable the local row (hidden from
later listings unless `--all`).

#### Track

```sh
cfddns track example.com on              # all A records follow home
cfddns track example.com www off         # one record (@, *, www or FQDN)
cfddns track example.com @ 10.0.0.5 on   # dual records: by content
```

`track=on` means update-ip keeps the record at the current public IP;
`track=off` leaves it exactly as it is. A second A at the same host as the
home-IP record can never stay `track=on` — pointing it home would duplicate
the existing record (Cloudflare forbids it, API 81058), so update-ip
untracks it again and logs the reason.

### Automation

```sh
cfddns sync              # align the local DB with Cloudflare (idempotent)
cfddns update-ip         # dynamic DNS + zone reconcile (usually scheduled)
cfddns init example.com [--wildcard]         # base @/www (+ *) at home IP
cfddns purge [example.com]                   # edge-cache purge (managed zones)
cfddns status            # overview: detected IP, token, tracked/excepted counts
# `track` examples live in the "DNS records" section above.
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
- The Cloudflare token is stored owner-only (`~/.cfddns/token`, mode 0600);
  if you set it via the `CLOUDFLARE_API_TOKEN` environment variable instead,
  keep that variable out of shared scripts and logs.
- The SQLite database contains a copy of your DNS (no Cloudflare secrets);
  it defaults to `~/.cfddns/cfddns.db` and should stay owner-only.
- Never commit real secrets; `*.db` is gitignored.
