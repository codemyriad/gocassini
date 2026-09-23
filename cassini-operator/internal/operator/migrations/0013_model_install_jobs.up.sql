CREATE TABLE model_install_jobs (
 id TEXT PRIMARY KEY,
 model TEXT NOT NULL,
 revision TEXT NOT NULL,
 device TEXT NOT NULL,
 state TEXT NOT NULL,
 progress TEXT NOT NULL DEFAULT '{}',
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
