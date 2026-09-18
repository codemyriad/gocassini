### Changed

- Tag edits appear immediately after saving, while recordings update in the background. Delayed recording updates remain visible and can be retried.
- Pending tag edits now survive operator restarts. Keep the annotation database on durable storage; rebuilding it from recordings cannot recover edits that have not yet reached those recordings.
- Repairing a recording with the same audio restores saved tags. Replacing its audio clears the old annotations.
