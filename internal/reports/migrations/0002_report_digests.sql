-- A digest of every report the lab has stored, so that the same report is
-- not stored twice. It stays when its report is deleted, with report_id
-- empty: a deleted report cannot be posted back by whoever kept the file.
CREATE TABLE report_digests (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  digest TEXT NOT NULL,
  report_id INTEGER,
  created_at INTEGER NOT NULL CHECK (created_at BETWEEN 1000000000000000 AND 99999999999999999)
) STRICT;
CREATE UNIQUE INDEX report_digests_digest ON report_digests (digest);
CREATE INDEX report_digests_report_id ON report_digests (report_id);
