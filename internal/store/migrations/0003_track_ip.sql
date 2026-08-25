-- Per-record opt-in flag: A records with track_ip=1 follow the home public
-- IP (cfddns update-ip owns them). sync never touches this flag; the
-- `cfddns track` command and `cfddns init` manage it. Default 0 is the safe
-- state: nothing is ever rewritten on Cloudflare unless explicitly tracked.

ALTER TABLE `dns` ADD COLUMN IF NOT EXISTS `track_ip` tinyint(1) NOT NULL DEFAULT 0 AFTER `priority`;

-- Backfill: A records currently holding the last-known home IP were almost
-- certainly already managed by update-ip, so they become tracked. Records
-- pointing elsewhere (e.g. other servers) stay untracked.
UPDATE `dns` d
JOIN `app_state` a ON a.k = 'last_known_ip' AND a.v <> ''
SET d.track_ip = 1
WHERE d.type = 'A' AND d.status = 'on' AND d.content = a.v;
