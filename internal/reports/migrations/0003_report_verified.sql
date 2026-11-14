-- Whether a report is a Verified run: 1 when its Mac's Machine key was signed
-- in as an M team member when the report came. Stamped then, never recomputed.
ALTER TABLE reports ADD COLUMN verified INTEGER NOT NULL DEFAULT 0 CHECK (verified IN (0, 1));
