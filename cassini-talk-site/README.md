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

The host serves ordinary files and directory indexes from object storage.
Astro's generated `index.html` files provide page routes. Static Assets-specific
`_headers` and `_redirects` files are unsupported; discuss routing or HTTP header
changes with the administrators.
