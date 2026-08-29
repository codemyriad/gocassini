### Fixed
- Production ExApp deploys now use collision-safe temporary manifest paths and clean them after settled runs, so a stale file from another operator cannot block an upgrade.
- ExApp startup logs now report the effective publication sink instead of contradicting a resolved Nextcloud Files destination with a later `local` line.
