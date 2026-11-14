-- The Mac's record: one line for each event of lending a Mac. It Joined,
-- its first baseline, a change of its weekly window, Pause, Open now,
-- Resume, a change of its Access level or of publishing, and its leaving.
-- A line names the Mac only (by name and by enrollment), never a person.
CREATE TABLE mac_record (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at INTEGER NOT NULL,
  mac TEXT NOT NULL,
  device_id TEXT NOT NULL,
  event TEXT NOT NULL,
  line TEXT NOT NULL
) STRICT;
CREATE INDEX mac_record_device ON mac_record(device_id, id);
-- A Retired Mac: the record of a Mac whose enrollment ended, kept for its
-- history. It is out of the fleet, its name is free, and it counts toward
-- nobody's cap. how is who ended it (volunteer, maintainer, blocked);
-- volunteer_id is whose Mac it was, 0 for a Lab-owned Mac; data is the
-- Mac's record as it stood.
CREATE TABLE retired_macs (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  retired INTEGER NOT NULL,
  how TEXT NOT NULL,
  volunteer_id INTEGER NOT NULL DEFAULT 0,
  data TEXT NOT NULL
) STRICT;
CREATE INDEX retired_macs_name ON retired_macs(name, retired);
CREATE INDEX retired_macs_volunteer ON retired_macs(volunteer_id);
