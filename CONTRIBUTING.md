# Contributing

Thanks for helping with cfddns. Please keep contributions small, focused and
reviewable.

## Ground rules

- Cloudflare is the single source of truth; the SQLite store only records
  state after Cloudflare writes succeed.
- Zones (domains) are managed on the Cloudflare website only — cfddns never
  adds or removes zones itself. Do not add commands that do.
- No hard deletes: anything absent from Cloudflare is soft-disabled in the
  local DB.
- Never commit real credentials or tokens. `.env*` and `*.db` are ignored.
- Keep the binary standalone: no external database, pure-Go dependencies
  preferred (CI builds with `CGO_ENABLED=0` on Linux/macOS/Windows).

## Development

```sh
go build ./... && go vet ./... && go test ./...
```

Migrations live in `internal/store/migrations/*.sql` (SQLite) and apply
automatically at startup, at most once each (`schema_migrations`).

## Testing

- Pure logic (IP agreement, tracked-record splitting, base-record completion,
  flag parsing) has unit tests — add tests with meaningful behavior.
- Everything else verifies against a live Cloudflare account; use
  `--dry-run` and `CF_DDNS_TEST_IP` for safe scenarios.
