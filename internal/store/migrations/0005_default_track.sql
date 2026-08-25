-- Default policy: a registrar=cloudflare (managed, active) domain is taken
-- over by cfddns — every A record of it follows the home public IP unless the
-- operator explicitly marks it track_ip=0 (`cfddns track ... off`). The 0003
-- backfill only covered records already at the home IP; this completes the
-- default for all managed zones. Explicit exceptions are applied after this
-- migration with the track command and are never flipped back by sync.

UPDATE `dns` d
JOIN `domain` dm ON dm.domainID = d.domain_id
SET d.track_ip = 1
WHERE dm.registrar = 'cloudflare' AND dm.status = 'on'
  AND d.type = 'A' AND d.status = 'on';
