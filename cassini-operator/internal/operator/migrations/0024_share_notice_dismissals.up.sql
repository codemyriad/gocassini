-- The Browse notices a person dismissed (D-885).
--
-- When Nextcloud refuses to share a recording with the person who started it,
-- the recording is not published, and Browse tells that person so, one
-- meeting at a time. Each person may dismiss each notice; once dismissed it
-- is never shown to them again, on any device — so it is kept here, by
-- person and meeting, rather than in one browser.
CREATE TABLE share_notice_dismissals (
  user_id      TEXT NOT NULL,
  job_id       TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  dismissed_at TEXT NOT NULL,
  PRIMARY KEY (user_id, job_id)
);
