-- Every line labd has for a Signal group, and what became of it. channel is
-- the Lab group ('lab') or the owner's "Agents" ('agents'). A line is sent
-- at most once: 'not-posted' had no group id when it was recorded, 'failed'
-- says why in error. No column holds a group id or a number.
CREATE TABLE lines (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at INTEGER NOT NULL,
  channel TEXT NOT NULL CHECK (channel IN ('lab', 'agents')),
  text TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('waiting', 'sending', 'sent', 'failed', 'not-posted')),
  settled INTEGER,
  error TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX lines_state ON lines(state, id);
-- What a line was last sent about, so it goes on a change and never twice:
-- a trouble "Agents" was told of, the day of the last daily summary.
CREATE TABLE line_marks (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  at INTEGER NOT NULL
) STRICT;
