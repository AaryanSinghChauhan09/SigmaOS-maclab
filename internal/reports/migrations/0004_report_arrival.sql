-- How a report arrived. One the lab published from a job's run names the
-- job, and who asked for it to be published: nobody when the lab published
-- it by itself. One that came by its Mac's own tool names neither.
ALTER TABLE reports ADD COLUMN lab_job TEXT;
ALTER TABLE reports ADD COLUMN asked_handle TEXT;
ALTER TABLE reports ADD COLUMN asked_account_id INTEGER;
