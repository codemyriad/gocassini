# Cassini app

`cassini-app` is the unified Nextcloud app shell. It hosts the meeting browser
from `cassini-viewer`, insight generation, and an administrator-only **Operator**
section for jobs, recording access, pipeline settings and AI providers.
Nextcloud’s AppAPI permissions enforce the API boundary; hiding navigation is
only a UI convenience.

## Development

From the repository root:

```bash
npm ci
cd cassini-app
CASSINI_OPERATOR_URL=http://127.0.0.1:4000 npm run dev
```

Open the address Vite prints. `CASSINI_OPERATOR_URL` is the upstream operator
origin. `CASSINI_OPERATOR_BASE_PATH` defaults to `/` and must match the path the
operator serves; set it to `/operator` when using an operator with that prefix.
Vite proxies API requests so the browser can use the same origin.

The standalone Compose bundle starts the operator and viewer, without an app
server. Its operator is enough for developing job controls. For Nextcloud
provisioning, permissions and the full meeting workflow, use the
[installed ExApp quick start](../docs/quick-start.md).

## Build and verify

```bash
npm run test
npm run build:all
```

`build` produces the standalone app. `build:embedded` produces the single JS/CSS
bundle served by the ExApp on Nextcloud’s embedded page and checks its shape.
`npm run preview` serves the standalone build; configure the same operator proxy
variables as for development. A non-Vite host needs its own API reverse proxy.
