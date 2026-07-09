### Added
- `cassini-microsite/package.json`, lockfile, and `tsconfig.json` so the site is installable, buildable, and type-checked (previously the microsite had none of these, so it could not be built or deployed by anyone from a clean checkout).

### Changed
- Microsite `site` config set to `https://gocassini.codemyriad.io` (was a TODO placeholder), so sitemap and canonical URLs are correct. The site is deployed to Cloudflare at that domain.

### Fixed
- Removed the repo-wide `*.json` `.gitignore` rule that silently swallowed committed config files (`package.json`, `tsconfig.json`, schemas) and required a growing list of `!` exceptions. Recorder JSON artifacts remain ignored via their runtime/cache/media directories. Also restores `cassini-control-panel/tsconfig.json`, which the rule had been hiding.
