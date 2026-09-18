### Changed

- Tag edits appear immediately after saving, while recordings update in the background. Delayed recording updates remain visible and can be retried.
- Pending tag edits now survive operator restarts. Keep the annotation database on durable storage; rebuilding it from recordings cannot recover edits that have not yet reached those recordings.
- Repairing a recording with the same audio restores saved tags. Replacing its audio clears the old annotations.
- Recordings skipped during initial annotation import retry automatically and when listed or opened.
- Large valid annotation documents can update their recordings; permanent recorder rejections show a blocked status instead of retrying indefinitely.
- An older background status error no longer clears tags saved by a newer edit.
- Rebuilding tag/search rows restores pending edits from the durable annotation database, even while the archive is unavailable.
