-- A Lab token: a person's credential for lab, lab mcp and their agents.
-- Only its hash is kept. period is how many days it lasts unused, 0 for
-- never; expires moves out at each use and is NULL for never. The address
-- of the last use is replaced at the next and goes with the row at revoke.
-- told is 0 until "Agents" has the line for the new token: a token minted
-- by the server's break-glass command is told of at its first use.
CREATE TABLE lab_tokens (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  hash TEXT NOT NULL UNIQUE,
  account_id INTEGER NOT NULL REFERENCES people(account_id),
  name TEXT NOT NULL,
  period INTEGER NOT NULL CHECK (period IN (0, 30, 90, 365)),
  made INTEGER NOT NULL,
  expires INTEGER,
  last_used INTEGER,
  last_used_address TEXT NOT NULL DEFAULT '',
  told INTEGER NOT NULL DEFAULT 0 CHECK (told IN (0, 1))
) STRICT;
CREATE INDEX lab_tokens_account ON lab_tokens(account_id);
