# Local development

Cassini local development usually involves two separate stacks:

1. the **harness** for local Nextcloud Talk
2. the **deployment bundle** for operator, control panel, and viewer

The deployment bundle can be taught with normal `docker compose` commands.
The harness should usually be taught through `cassini dev ...` entrypoints instead.

## Preferred harness entrypoints

From the repo root, prefer:

```bash
./bin/cassini dev stack up
./bin/cassini dev stack down
./bin/cassini dev stack status
./bin/cassini dev room create --name "Local room"
./bin/cassini dev smoke
```

Use those instead of raw harness `docker compose` as the normal documented path.

Why this matters:

- the `cassini dev` path runs the harness scripts rather than only starting containers
- startup includes waiting for services and bootstrap/setup work after Compose comes up
- room creation and smoke flows are part of the documented local product path

Raw harness `docker compose` commands are still useful when debugging harness internals directly, but they are not the preferred first path.

## Local addresses

For the local Nextcloud/Talk harness, use `127.0.0.1`, not `localhost`, including when opening the local UI in the browser.

Important examples:

- Nextcloud/Talk UI: `http://127.0.0.1:28080/`
- local call URLs returned by the harness should also stay on `127.0.0.1`

This should be called out explicitly anywhere a doc asks someone to open the local harness in the browser.

## Current macOS limitation

The local harness currently does not work on macOS because of networking issues in the harness stack.

Consequences for docs:

- any quickstart that depends on the local harness should say this near the first harness step
- do not leave this only to troubleshooting sections
- deployment-only instructions can still be documented separately from harness-dependent end-to-end flows

## Relationship between harness and deployment bundle

The easiest end-to-end local story usually combines both stacks:

1. start the harness to get a local Talk room
2. start the deployment bundle to get operator, control panel, and viewer
3. submit the local Talk room to the operator
4. open the published result in the viewer

That combined story is useful, but it should still preserve the caveats above:

- prefer `cassini dev` for harness lifecycle
- use `127.0.0.1`, not `localhost`, even in the browser
- call out the current macOS limitation when the harness is part of the path
