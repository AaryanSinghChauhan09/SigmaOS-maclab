CREATE TABLE people (
  account_id INTEGER PRIMARY KEY CHECK (account_id > 0),
  handle TEXT NOT NULL,
  maintainer INTEGER NOT NULL DEFAULT 0 CHECK (maintainer IN (0, 1)),
  added INTEGER NOT NULL,
  last_seen INTEGER
) STRICT;
CREATE TABLE browser_sessions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  hash TEXT NOT NULL UNIQUE,
  account_id INTEGER NOT NULL REFERENCES people(account_id),
  created INTEGER NOT NULL,
  last_used INTEGER NOT NULL
) STRICT;
CREATE INDEX browser_sessions_account ON browser_sessions(account_id);
