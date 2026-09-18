# D-793 follow-ups

- The vocabulary returns one deterministic representative appearance for a tag used differently across visible recordings. This is appropriate for picker/manager UI, but does not replace the per-recording appearance used when rendering a meeting or list row.
- Previously written `tag-styles.json` files are no longer authoritative and are intentionally not migrated by scanning archives. A future explicit maintenance migration could materialize them where desired.
