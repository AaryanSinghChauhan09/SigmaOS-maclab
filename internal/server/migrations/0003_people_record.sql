CREATE TABLE people_record (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at INTEGER NOT NULL,
  actor_id INTEGER NOT NULL,
  actor TEXT NOT NULL,
  what TEXT NOT NULL,
  about_id INTEGER,
  about TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX people_record_about ON people_record(about_id, id);
