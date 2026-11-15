-- A person's request to be Forgotten, made on their own page: when they
-- asked. It goes with the person's row when the forgetting is carried out,
-- and no list of past forgettings is kept.
ALTER TABLE people ADD COLUMN forget_asked INTEGER;
