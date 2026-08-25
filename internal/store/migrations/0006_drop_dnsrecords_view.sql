-- The dnsrecords view was a legacy-compat projection (dns JOIN domain)
-- created in 0002 for the frozen PHP scripts. Nothing in cfddns queries it
-- and the legacy stack is retired, so it is dropped. The real legacy object
-- with that name is the `dnsRecords` TABLE (kept untouched as history).
DROP VIEW IF EXISTS `dnsrecords`;
