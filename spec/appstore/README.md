# Vendored Nextcloud App Store schema

These two files are vendored **unmodified** from upstream Nextcloud so
`scripts/validate-appstore-tarball.sh` can validate `appinfo/info.xml` the way
apps.nextcloud.com does — offline and reproducibly, without depending on a live
download from a moving `master` branch.

| File | Source | License |
|---|---|---|
| `pre-info.xslt` | <https://raw.githubusercontent.com/nextcloud/appstore/master/nextcloudappstore/api/v1/release/pre-info.xslt> | AGPL-3.0-or-later |
| `info.xsd` | <https://apps.nextcloud.com/schema/apps/info.xsd> | AGPL-3.0-or-later |

Retrieved 2026-07-07.

## Why both, in this order

The store first runs `info.xml` through `pre-info.xslt`, which **drops elements
outside the base schema** (for Cassini that includes the whole
`<external-app>` … `<routes>` block), then validates the result against
`info.xsd`. Validating `info.xml` against `info.xsd` directly false-fails an
ExApp manifest — e.g. `Element 'routes': This element is not expected`.

## Updating

Re-download both from the URLs above if the store schema changes. The validator
also accepts `APPSTORE_XSLT` / `APPSTORE_XSD` env overrides and, as a last
resort, downloads from upstream when these files are absent.
