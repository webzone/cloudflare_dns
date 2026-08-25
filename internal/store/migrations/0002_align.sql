-- Align an existing (PHP-era) database to the mirror schema. MariaDB-only
-- syntax (IF NOT EXISTS / IF EXISTS) — the target database is MariaDB 10.11.
-- All statements are idempotent; the schema_migrations table ensures each file
-- runs at most once.
--
-- Order matters: the legacy composite unique key `ur` is dropped BEFORE the
-- name/content columns are widened, otherwise the index rebuild exceeds the
-- maximum key length.

ALTER TABLE `domain` ADD COLUMN IF NOT EXISTS `status` enum('on','off') NOT NULL DEFAULT 'on' AFTER `zoneid`;
ALTER TABLE `domain` ADD COLUMN IF NOT EXISTS `updated_at` timestamp NULL DEFAULT NULL ON UPDATE CURRENT_TIMESTAMP AFTER `status`;

ALTER TABLE `dns` DROP INDEX IF EXISTS `ur`;

ALTER TABLE `dns` MODIFY COLUMN `name` varchar(255) NOT NULL COMMENT 'FQDN as returned by Cloudflare';
ALTER TABLE `dns` MODIFY COLUMN `content` TEXT NOT NULL;
ALTER TABLE `dns`
  MODIFY COLUMN `type` enum('A','AAAA','CAA','CERT','CNAME','DNSKEY','DS','HTTPS','LOC','MX','NAPTR','NS','OPENPGPKEY','PTR','SMIMEA','SPF','SRV','SSHFP','SVCB','TLSA','URI','TXT') NOT NULL DEFAULT 'A',
  MODIFY COLUMN `priority` int(11) NOT NULL DEFAULT '0';
ALTER TABLE `dns` ADD INDEX IF NOT EXISTS `domain_id` (`domain_id`);
ALTER TABLE `dns` ADD COLUMN IF NOT EXISTS `updated_at` timestamp NULL DEFAULT NULL ON UPDATE CURRENT_TIMESTAMP AFTER `recordid`;

CREATE TABLE IF NOT EXISTS `app_state` (
  `k` varchar(64) NOT NULL PRIMARY KEY,
  `v` varchar(255) NOT NULL,
  `updated_at` timestamp NOT NULL DEFAULT current_timestamp() ON UPDATE current_timestamp()
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_520_ci;

CREATE OR REPLACE VIEW `dnsrecords` AS
SELECT `dns`.`dnsid` AS `dnsid`, `dns`.`domain_id` AS `domain_id`, `dns`.`type` AS `type`,
       `dns`.`name` AS `name`, `dns`.`content` AS `content`, `dns`.`proxied` AS `proxied`,
       `dns`.`ttl` AS `ttl`, `dns`.`priority` AS `priority`, `dns`.`status` AS `status`,
       `dns`.`recordid` AS `recordid`, `domain`.`domainname` AS `domainname`,
       `domain`.`registrar` AS `registrar`, `domain`.`zoneid` AS `zoneid`,
       `domain`.`status` AS `domain_status`
FROM (`dns` LEFT JOIN `domain` ON (`domain`.`domainID` = `dns`.`domain_id`));

-- Clean legacy data: trim stray whitespace on domain names (MySQL collations
-- treat trailing spaces as equal in comparisons, so LENGTH is the signal).
UPDATE `domain` SET `domainname` = TRIM(`domainname`)
WHERE LENGTH(`domainname`) <> LENGTH(TRIM(`domainname`));
