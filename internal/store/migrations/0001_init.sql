-- Baseline schema (fresh install). On existing databases the CREATE TABLE IF
-- NOT EXISTS statements are no-ops and 0002_align.sql applies the deltas.
-- Cloudflare is the single source of truth: this DB is a mirror keyed by the
-- Cloudflare record ID (`recordid`). The old composite unique key was dropped
-- because Cloudflare permits duplicate records and IP updates change `content`.

CREATE TABLE IF NOT EXISTS `domain` (
  `domainID` int(11) NOT NULL AUTO_INCREMENT,
  `domainname` varchar(128) NOT NULL,
  `registrar` enum('enom','cloudflare','other') NOT NULL DEFAULT 'cloudflare',
  `zoneid` varchar(256) NOT NULL DEFAULT '',
  `status` enum('on','off') NOT NULL DEFAULT 'on',
  `updated_at` timestamp NULL DEFAULT NULL ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`domainID`),
  UNIQUE KEY `domainname` (`domainname`),
  KEY `registrar` (`registrar`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_520_ci;

CREATE TABLE IF NOT EXISTS `dns` (
  `dnsid` int(11) NOT NULL AUTO_INCREMENT,
  `domain_id` int(11) NOT NULL,
  `type` enum('A','AAAA','CAA','CERT','CNAME','DNSKEY','DS','HTTPS','LOC','MX','NAPTR','NS','OPENPGPKEY','PTR','SMIMEA','SPF','SRV','SSHFP','SVCB','TLSA','URI','TXT') NOT NULL DEFAULT 'A',
  `name` varchar(255) NOT NULL COMMENT 'FQDN as returned by Cloudflare',
  `content` TEXT NOT NULL,
  `proxied` tinyint(1) NOT NULL DEFAULT 1,
  `ttl` int(11) DEFAULT NULL,
  `priority` int(11) NOT NULL DEFAULT 0,
  `status` enum('on','off') NOT NULL DEFAULT 'on',
  `recordid` varchar(64) NOT NULL DEFAULT '',
  `updated_at` timestamp NULL DEFAULT NULL ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`dnsid`),
  KEY `mi` (`dnsid`,`domain_id`,`status`),
  KEY `recordid` (`recordid`),
  KEY `domain_id` (`domain_id`),
  CONSTRAINT `dns_ibfk_1` FOREIGN KEY (`domain_id`) REFERENCES `domain` (`domainID`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_520_ci;

CREATE TABLE IF NOT EXISTS `app_state` (
  `k` varchar(64) NOT NULL PRIMARY KEY,
  `v` varchar(255) NOT NULL,
  `updated_at` timestamp NOT NULL DEFAULT current_timestamp() ON UPDATE current_timestamp()
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_520_ci;

-- The dnsrecords view is created in 0002_align.sql after domain.status exists
-- (it references that column, which existing databases get via the 0002 ALTER).
