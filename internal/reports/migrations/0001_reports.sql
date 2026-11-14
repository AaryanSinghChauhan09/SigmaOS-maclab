-- The old site's reports and tester_bindings tables (site/db/schema.rb). Its
-- tester_login is handle here, github_login and github_id are handle and
-- account_id, and tester_bindings is mac_sign_ins.
--
-- Every time is an integer count of microseconds since 1970: 16 or 17
-- digits from 2001 to the year 5138, so seconds, milliseconds and
-- nanoseconds are refused. An id, once given, is never given again.
CREATE TABLE reports (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  public_id TEXT NOT NULL,
  deletion_token_digest TEXT NOT NULL,
  schema_version INTEGER NOT NULL,
  body TEXT NOT NULL CHECK (json_valid(body)),
  created_at INTEGER NOT NULL CHECK (created_at BETWEEN 1000000000000000 AND 99999999999999999),
  updated_at INTEGER NOT NULL CHECK (updated_at BETWEEN 1000000000000000 AND 99999999999999999),
  hidden_at INTEGER CHECK (hidden_at BETWEEN 1000000000000000 AND 99999999999999999),
  machine_id TEXT,
  handle TEXT,
  account_id INTEGER
) STRICT;
CREATE UNIQUE INDEX reports_public_id ON reports (public_id);

CREATE TABLE mac_sign_ins (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  machine_id TEXT NOT NULL,
  account_id INTEGER NOT NULL,
  handle TEXT NOT NULL,
  created_at INTEGER NOT NULL CHECK (created_at BETWEEN 1000000000000000 AND 99999999999999999),
  updated_at INTEGER NOT NULL CHECK (updated_at BETWEEN 1000000000000000 AND 99999999999999999)
) STRICT;
CREATE UNIQUE INDEX mac_sign_ins_machine_id ON mac_sign_ins (machine_id);
CREATE INDEX mac_sign_ins_handle ON mac_sign_ins (handle);
