-- The Nextcloud file id of an insight's delivered document (D-792).
--
-- The Files app since Nextcloud 28 addresses a file by its numeric id in the
-- route (/apps/files/files/<id>?dir=...), and only that selects the file and
-- opens its details sidebar, where sharing is. The path alone opens the folder.
-- Nextcloud answers the delivery PUT with the id (OC-FileId), so it is kept
-- beside the path; empty for a run delivered before this, which still opens
-- the folder.
ALTER TABLE insight_runs ADD COLUMN document_file_id TEXT NOT NULL DEFAULT '';
