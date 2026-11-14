-- Who a person is beyond Maintainer. m_team is the M team's mark and
-- leave_to_lend an Invite or an approved Request to lend; both hang on the
-- account id, so a handle can be marked before its first sign-in (last_seen
-- is NULL until then). blocked_at is set while the account is Blocked, with
-- who blocked it and the optional reason they typed.
ALTER TABLE people ADD COLUMN m_team INTEGER NOT NULL DEFAULT 0 CHECK (m_team IN (0, 1));
ALTER TABLE people ADD COLUMN leave_to_lend INTEGER NOT NULL DEFAULT 0 CHECK (leave_to_lend IN (0, 1));
ALTER TABLE people ADD COLUMN blocked_at INTEGER;
ALTER TABLE people ADD COLUMN blocked_by INTEGER;
ALTER TABLE people ADD COLUMN blocked_reason TEXT NOT NULL DEFAULT '';
-- What the actor typed beside an action, such as the reason for a Block:
-- apart from the line's words, so it can come off a line as a handle can.
ALTER TABLE people_record ADD COLUMN note TEXT NOT NULL DEFAULT '';
