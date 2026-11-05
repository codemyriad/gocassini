# cassini.talk website

This directory is the source for the public website. Edit its Astro pages and
components here, review the pull request, and build with:

```sh
npm ci
npm run dev
npm run lint
npm run build
```

Public CI validates the site and keeps the generated `dist/` as a review
artifact. Deployment is managed separately by the administrators in the private
infrastructure repository. No hosting credentials or deployment configuration
are needed in this public repository.

The website build owns its generated pages, including `/download/` and
`/privacy/`. App release pipelines manage the files under `/download/<platform>/`
and the platform policies under `/privacy/<platform>/` independently. Do not put
those files in `public/`: the deployment checks reject them. Link to their stable
URLs instead. The Mac download is
`https://cassini.talk/download/mac/Cassini-talk.dmg`.

Installer buttons and package links stay on `cassini.talk`. Android uses
`/download/android/cassini-android.apk`; Linux uses the native distribution
package aliases under `/download/linux/`. The app release mirror publishes
`release.json` alongside each platform's installers, so the download cards can
show current versions, dates and sizes without rebuilding the website. GitHub
links are reserved for the explicitly labelled source-code section.

The host serves ordinary files and directory indexes from object storage.
Astro's generated `index.html` files provide page routes. Static Assets-specific
`_headers` and `_redirects` files are unsupported; discuss routing or HTTP header
changes with the administrators.

Mac, Android and Linux version/date/size information is read from the published
appcast or mirror metadata during the build, so it is present without JavaScript.
Browser refreshes use the same parser to keep open deployments current. A build
fails if published release metadata is unavailable instead of shipping a stale
version. Linux offers native packages only; Flatpak and Flathub are not offered.

Linux stable installer URLs use the `cassini-talk-<distribution>` name. The
mirror retains original versioned filenames for provenance; starting with Linux
0.4.0, upstream packages are also named `cassini-talk`. Linux 0.4.0 is published
only after its release assets and `SHA256SUMS` are available and verified.
