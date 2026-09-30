# Doctor usability previews

The review gallery renders `RecordingSetup.svelte`, the same Doctor component
used by the live app, with example `RecordingReadiness` responses. It requires
no broken services, real recordings or administrator privileges. Nextcloud's
embedded page still requires a signed-in user.

Build the app with `VITE_CASSINI_DOCTOR_PREVIEWS=true npm run build:all --workspace cassini-app`
to include the gallery. Ordinary builds omit it. This flag belongs on review
deployments; it does not add an operator endpoint or change server diagnostics.

Append `#doctor-preview=` to the app URL to open the scenario index, or
`#doctor-preview=host-tools` to open a specific case. The index contains links
to every case. It works with the standalone app and the Nextcloud embedded page.

Checks, credential edits, test preparation and re-indexing run only in tab-local
memory. The gallery does not mount the live shell or call its APIs. Fixture links
and advanced provisioning instructions use `preview.invalid`. Reload resets
edits; starting a simulated re-index shows progress and then completion.

The gallery labels its data as simulated and offers light and dark themes.
Example messages follow the backend response shape, but fixtures are examples,
not a replacement for integration tests of actual probe outcomes. When changing
Doctor response codes or wording, update the fixtures in
`cassini-app/src/preview/doctorScenarios.ts` alongside the producing code.
