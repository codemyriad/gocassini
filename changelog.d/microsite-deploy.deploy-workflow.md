### Added
- CI workflow (`deploy-microsite.yml`) that builds `cassini-microsite/` and publishes a per-branch preview to Cloudflare R2, reusing the existing `R2_*` secrets.
- `cassini-microsite/package.json` and lockfile so the site is installable and buildable (previously the microsite had no manifest).
