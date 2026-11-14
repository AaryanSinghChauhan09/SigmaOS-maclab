-- The lab's opening (the Cutover's step 25): one row, written once, never
-- changed. Before it the lab is in Preview wherever labd is started with
-- --preview; from it the lab is Live whatever labd is started with. m_team
-- is how many people were marked M team at that moment, release the Lab
-- release labd ran.
CREATE TABLE live (
  one INTEGER PRIMARY KEY CHECK (one = 1),
  at INTEGER NOT NULL,
  m_team INTEGER NOT NULL,
  release TEXT NOT NULL
) STRICT;
