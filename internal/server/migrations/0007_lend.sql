-- Lending a Mac. A Request to lend is one person asking for leave to lend:
-- one waits at a time, and it stays as answered or withdrawn. contact and
-- note are what the person typed, for Maintainers only. lend_cap is a cap a
-- Maintainer raised for one person; NULL is the lab's own.
CREATE TABLE requests_to_lend (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  account_id INTEGER NOT NULL REFERENCES people(account_id),
  model TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  contact TEXT NOT NULL DEFAULT '',
  asked INTEGER NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('waiting', 'withdrawn', 'approved', 'declined')),
  answered INTEGER,
  answered_by INTEGER,
  reason TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE UNIQUE INDEX requests_to_lend_waiting ON requests_to_lend(account_id) WHERE state = 'waiting';
CREATE INDEX requests_to_lend_account ON requests_to_lend(account_id, id);
ALTER TABLE people ADD COLUMN lend_cap INTEGER;
-- A Join command's token. made_by is the account that made it and made
-- when; volunteer_id is whose Mac it enrolls, 0 for a Lab-owned Mac.
-- bound_to is the hash of the activation key of the first Mac that used it:
-- no other Mac uses it after that.
ALTER TABLE tokens ADD COLUMN made_by INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tokens ADD COLUMN made INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tokens ADD COLUMN volunteer_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tokens ADD COLUMN bound_to TEXT NOT NULL DEFAULT '';
