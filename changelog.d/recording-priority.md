### Changed

- Add opt-in recording-first scheduling: defer builds/sealing/publication while
  recorders are active, and yield running builds without spending resource retry
  attempts or losing captured media.
- Serialize recorder finalization across subprocesses under recording priority.
- Credit conservatively reclaimable cgroup file cache during memory admission,
  fixing waits caused by clean cache rather than memory pressure.
