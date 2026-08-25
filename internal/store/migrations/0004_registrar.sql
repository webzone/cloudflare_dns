-- The registrar column means "which registrar manages this domain". A domain
-- whose DNS is on Cloudflare is under Cloudflare's management, so every
-- currently synced zone is marked 'cloudflare'. sync keeps this value current
-- for every zone it ever sees (and only this tool + the mirror gate update-ip
-- on it); status='on' rows without a zone id cannot be synced zones.

UPDATE `domain` SET `registrar` = 'cloudflare'
WHERE `status` = 'on' AND `zoneid` <> '';
