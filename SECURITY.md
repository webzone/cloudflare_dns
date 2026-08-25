# Security Policy

## Supported versions

The current `main` branch is the only supported line. Releases are tagged
when they happen; until then, pin to a specific commit if you need stability.

## Reporting a vulnerability

Please do **not** open a public issue for security problems.

- Report privately via GitHub's private vulnerability reporting (Security tab
  → "Report a vulnerability"), or
- Email the repository owner if you know a direct address.

Include, if possible: the affected version/commit, a minimal reproduction,
and your suggested fix. Acknowledgment within 5 business days.

## Scope & notes

- This tool stores Cloudflare API credentials only via environment variables /
  env files; never hard-code or commit tokens.
- The local SQLite store records DNS state (no secrets), but treat it as
  private data — default to owner-only file permissions.
- The Cloudflare API token should be a **scoped** token
  (`Zone.Zone:Read`, `Zone.DNS:Edit`, `Zone.Cache:Purge`, `Zone.Settings:Edit`),
  never the account Global API Key.
