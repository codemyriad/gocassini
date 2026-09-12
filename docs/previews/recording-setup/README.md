# Recording Setup walkthrough

Open [the standalone HTML](../../recording-setup-preview.html) directly in a browser.
It renders the production `RecordingSetup.svelte` component using a simulated
operator client. The sidebar selects 19 fixture states; preview controls simulate
Talk and processing events. No operator API requests are sent.

The surrounding app navigation and storage placeholder are presentation context.
The existing storage wizard is not rendered. Check again preserves the selected
fixture. Example links do not open real rooms or recordings. External documentation
links remain usable.

Rebuild from the repository root after installing the workspace dependencies:

```sh
node docs/previews/recording-setup/build.mjs
```

The generated file contains its JavaScript and CSS and needs no web server.

The environment examples cover unknown, provider-managed, console-only, confirmed
AIO, Compose and host access. These are fixture choices, not automatic detection.
The generated commands have not been executed by the walkthrough.
