-- Package sets: frozen lists of packages, each from a repository version,
-- an uploaded file or a Builder build, with every file's sha256. A row is
-- written once and never changed.
CREATE TABLE package_sets (
  name TEXT PRIMARY KEY,
  created INTEGER NOT NULL,
  data TEXT NOT NULL
) STRICT;
