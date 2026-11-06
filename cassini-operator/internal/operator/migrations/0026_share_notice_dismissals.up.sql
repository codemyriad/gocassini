-- The Browse notices a person dismissed (D-885).
--
-- Browse tells a person, one meeting at a time, when Nextcloud refused to
-- share a recording with them, or refused them as the starter so it was not
-- published. Each person may dismiss each notice; once dismissed it is never
-- shown to them again, on any device — so it is kept here, by person and
-- meeting, rather than in one browser.
CREATE TABLE share_notice_dismissals (
  user_id      TEXT NOT NULL,
  job_id       TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  dismissed_at TEXT NOT NULL,
  PRIMARY KEY (user_id, job_id)
);
