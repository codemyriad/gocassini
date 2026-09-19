### Fixed

- Tagging a selection of meetings now uses one atomic database batch and displays all updated rows together. Safe retries reuse the batch request ID; recordings continue syncing individually across the archive workers.
