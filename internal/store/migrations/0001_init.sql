-- cfddns mirror schema (SQLite). Cloudflare is the single source of truth:
-- this database is a denormalized copy keyed by the Cloudflare record ID.
-- Applied once, automatically, on first open.

CREATE TABLE IF NOT EXISTS `domain` (
  `domainID`  INTEGER PRIMARY KEY AUTOINCREMENT,
  `domainname` TEXT NOT NULL UNIQUE,
  `registrar` TEXT NOT NULL DEFAULT 'cloudflare'
              CHECK (registrar IN ('enom', 'cloudflare', 'other')),
  `zoneid`    TEXT NOT NULL DEFAULT '',
  `status`    TEXT NOT NULL DEFAULT 'on' CHECK (status IN ('on', 'off')),
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS `dns` (
  `dnsid`    INTEGER PRIMARY KEY AUTOINCREMENT,
  `domain_id` INTEGER NOT NULL REFERENCES `domain`(`domainID`) ON DELETE CASCADE,
  `type`     TEXT NOT NULL DEFAULT 'A',
  `name`     TEXT NOT NULL,
  `content`  TEXT NOT NULL,
  `proxied`  INTEGER NOT NULL DEFAULT 1,
  `ttl`      INTEGER,
  `priority` INTEGER NOT NULL DEFAULT 0,
  `status`   TEXT NOT NULL DEFAULT 'on' CHECK (status IN ('on', 'off')),
  `recordid` TEXT NOT NULL DEFAULT '',
  `track_ip` INTEGER NOT NULL DEFAULT 0,
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS `idx_dns_domain` ON `dns`(`domain_id`);
CREATE INDEX IF NOT EXISTS `idx_dns_recordid` ON `dns`(`recordid`);

CREATE TABLE IF NOT EXISTS `app_state` (
  `k` TEXT NOT NULL PRIMARY KEY,
  `v` TEXT NOT NULL,
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
