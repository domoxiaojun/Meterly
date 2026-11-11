package store

const AnalyticsSchema = `
CREATE TABLE IF NOT EXISTS ips(
 id INTEGER PRIMARY KEY, address TEXT NOT NULL UNIQUE, packed BLOB, family INTEGER NOT NULL DEFAULT 0,
 country TEXT NOT NULL DEFAULT '', country_name TEXT NOT NULL DEFAULT '', asn INTEGER NOT NULL DEFAULT 0,
 org TEXT NOT NULL DEFAULT '', geo_at INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS ips_network ON ips(family,packed);
CREATE INDEX IF NOT EXISTS ips_geo ON ips(country,asn);
CREATE TABLE IF NOT EXISTS sites(site TEXT PRIMARY KEY, label TEXT NOT NULL DEFAULT '', first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS site_minute(
 host_id INTEGER NOT NULL,ts INTEGER NOT NULL,site TEXT NOT NULL,ip_id INTEGER NOT NULL,status INTEGER NOT NULL,
 rx_known INTEGER NOT NULL,ip_source TEXT NOT NULL,rx INTEGER NOT NULL,tx INTEGER NOT NULL,requests INTEGER NOT NULL,
 PRIMARY KEY(host_id,ts,site,ip_id,status,rx_known,ip_source)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS site_hour(
 host_id INTEGER NOT NULL,ts INTEGER NOT NULL,site TEXT NOT NULL,ip_id INTEGER NOT NULL,status INTEGER NOT NULL,
 rx_known INTEGER NOT NULL,ip_source TEXT NOT NULL,rx INTEGER NOT NULL,tx INTEGER NOT NULL,requests INTEGER NOT NULL,
 PRIMARY KEY(host_id,ts,site,ip_id,status,rx_known,ip_source)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS site_minute_site ON site_minute(site,ts,host_id,ip_id);
CREATE INDEX IF NOT EXISTS site_minute_ip ON site_minute(ip_id,ts,site);
CREATE INDEX IF NOT EXISTS site_hour_site ON site_hour(site,ts,host_id,ip_id);
CREATE INDEX IF NOT EXISTS site_hour_ip ON site_hour(ip_id,ts,site);
CREATE INDEX IF NOT EXISTS site_hour_time ON site_hour(ts);
CREATE TABLE IF NOT EXISTS site_day(
 host_id INTEGER NOT NULL,ts INTEGER NOT NULL,site TEXT NOT NULL,status INTEGER NOT NULL,rx_known INTEGER NOT NULL,
 rx INTEGER NOT NULL,tx INTEGER NOT NULL,requests INTEGER NOT NULL,
 PRIMARY KEY(host_id,ts,site,status,rx_known)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS site_day_site ON site_day(site,ts);
CREATE TABLE IF NOT EXISTS source_coverage(host_id INTEGER NOT NULL,site TEXT NOT NULL,first_ts INTEGER NOT NULL,last_ts INTEGER NOT NULL,PRIMARY KEY(host_id,site));
CREATE TABLE IF NOT EXISTS collection_coverage(host_id INTEGER NOT NULL,kind TEXT NOT NULL,first_ts INTEGER NOT NULL,last_ts INTEGER NOT NULL,
 PRIMARY KEY(host_id,kind));
CREATE TABLE IF NOT EXISTS data_issues(host_id INTEGER NOT NULL,from_ts INTEGER NOT NULL,to_ts INTEGER NOT NULL,reason TEXT NOT NULL,
 PRIMARY KEY(host_id,from_ts,to_ts,reason));
`

const ControlSchema = `
CREATE TABLE IF NOT EXISTS host_config(host_id INTEGER PRIMARY KEY,version INTEGER NOT NULL DEFAULT 0,policy TEXT NOT NULL DEFAULT '{}');
CREATE TABLE IF NOT EXISTS upgrade_tasks(id INTEGER PRIMARY KEY AUTOINCREMENT,host_id INTEGER NOT NULL,version TEXT NOT NULL,url TEXT NOT NULL,
 sha256 TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'pending',message TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_upgrade ON upgrade_tasks(host_id) WHERE status IN ('pending','running');
CREATE TABLE IF NOT EXISTS maintenance_runs(id INTEGER PRIMARY KEY AUTOINCREMENT,kind TEXT NOT NULL,status TEXT NOT NULL,message TEXT NOT NULL DEFAULT '',started_at INTEGER NOT NULL,finished_at INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS storage_samples(ts INTEGER PRIMARY KEY,db_bytes INTEGER NOT NULL,backup_bytes INTEGER NOT NULL,free_bytes INTEGER NOT NULL,total_bytes INTEGER NOT NULL);
`
