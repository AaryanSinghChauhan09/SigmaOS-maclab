-- labd.db as maclab kept it at the fork. A database maclab made has these
-- tables already and keeps them as they are.
CREATE TABLE IF NOT EXISTS devices (id TEXT PRIMARY KEY, name TEXT UNIQUE NOT NULL, secret_hash TEXT NOT NULL, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS tokens (
  token_hash TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  label TEXT,
  model TEXT,
  expires INTEGER NOT NULL,
  volunteer INTEGER NOT NULL DEFAULT 0,
  requester TEXT NOT NULL DEFAULT '',
  volunteer_name TEXT NOT NULL DEFAULT '',
  contact TEXT NOT NULL DEFAULT '',
  activated_by TEXT NOT NULL DEFAULT '',
  volunteer_login TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS jobs (id TEXT PRIMARY KEY, device TEXT NOT NULL, created INTEGER NOT NULL, state TEXT NOT NULL, data TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS jobs_device ON jobs(device, created);
CREATE TABLE IF NOT EXISTS builds (id TEXT PRIMARY KEY, key TEXT NOT NULL, created INTEGER NOT NULL, state TEXT NOT NULL, data TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS builds_key ON builds(key, created);
CREATE TABLE IF NOT EXISTS maintainers (name TEXT PRIMARY KEY, token_hash TEXT UNIQUE NOT NULL, added INTEGER NOT NULL, added_by TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS ended (secret_hash TEXT PRIMARY KEY, name TEXT NOT NULL, maintainer TEXT NOT NULL, at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS configs (device TEXT NOT NULL, kernel TEXT NOT NULL, sha TEXT NOT NULL, PRIMARY KEY(device, kernel));
CREATE TABLE IF NOT EXISTS builders (name TEXT PRIMARY KEY, gen TEXT UNIQUE NOT NULL, token_hash TEXT UNIQUE NOT NULL, kinds TEXT NOT NULL, added INTEGER NOT NULL, added_by TEXT NOT NULL);
