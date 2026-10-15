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
npm exec -- playwright install chromium
npm run test
npm run build:all
```

The test command runs unit tests and Vitest browser checks for retention,
its first-run review, eviction, and storage charts. Those browser checks mount
the real Svelte components with synthetic operator responses, so no operator
or Nextcloud server is required.
The installed ExApp browser validation remains part of the harness workflow.

## Profiling installed builds

Production builds include external JavaScript source maps with original source
content. The code remains minified. The embedded bundle references
`viewer.js.map`, served beside `/ui/viewer.js` through the Nextcloud AppAPI
proxy with the same USER access as the UI. Every signed-in UI user can load it;
no administrator or harness-only switch is required.

```text
DevTools -> AppAPI proxy /ui/viewer.js.map
                       -> operator -> dist/embedded/viewer.js.map
```

Rebuild and deploy the image and update the registered AppAPI manifest together
when introducing this route to an existing installation. A frontend rebuild
alone does not add the operator route or proxy permission. The image build
copies the complete dist directory, including maps.

In Chrome, enable **Settings > Preferences > Sources > JavaScript source maps**,
reload the app, then check **More tools > Developer Resources** for successful
map loading before recording in Performance. Preserve the matching bundle and
map when saving release artifacts; rebuilding a different revision cannot
reliably decode an old trace. The standalone build serves maps alongside its
hashed assets. Maps are separate downloads used by debugging tools, rather
than inline additions to the executable bundle.

`build` produces the standalone app. `build:embedded` produces the single JS/CSS
bundle served by the ExApp on Nextcloud’s embedded page and checks its shape.
`npm run preview` serves the standalone build; configure the same operator proxy
variables as for development. A non-Vite host needs its own API reverse proxy.
