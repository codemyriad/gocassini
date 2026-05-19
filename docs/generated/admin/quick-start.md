# Quick start

This walkthrough brings up the packaged stack and verifies the main runtime surfaces.

## 1. Start the deployment bundle

From the repo root:

```bash
cd deployment
docker compose up --build
```

On later runs, `docker compose up` is usually enough.

## 2. Open the three surfaces

With the checked-in deployment env, the default addresses are:

- control panel: `http://127.0.0.1:4173/`
- operator API: `http://127.0.0.1:4000/`
- viewer: `http://127.0.0.1:8765/`

## 3. Run quick checks

From another terminal:

```bash
curl -s http://127.0.0.1:4000/jobs
curl -s http://127.0.0.1:4173/jobs
curl -s http://127.0.0.1:8765/catalog.json
```

What to expect:

- operator API returns the jobs list
- control panel proxy path returns the same kind of data
- viewer may return an empty catalog before any publish succeeds

## 4. Optional: exercise a real local meeting flow

If you want to verify record -> build -> publish end to end, start the local Talk harness from the repo root:

```bash
./bin/cassini dev stack up
CALL_URL="$(./bin/cassini dev room create --name \"Cassini local demo\" | tail -n1)"
echo "$CALL_URL"
```

Important notes for this optional harness path:

- prefer `./bin/cassini dev stack up` over raw harness `docker compose`, because the wrapper runs the harness scripts and additional setup after Compose starts
- use `127.0.0.1`, not `localhost`, for local harness URLs, including in the browser
- the local harness currently does not work on macOS because of networking issues in the harness stack

Then:

1. open the Talk room URL in the browser
2. open the control panel
3. paste the same `CALL_URL`
4. start the job
5. watch it move through `record -> build -> publish -> done`

## 5. Verify viewer behavior

Once a publish succeeds, refresh:

- `http://127.0.0.1:8765/`

The viewer should serve the promoted live site from shared storage.

## What you just verified

- the operator is reachable
- the control panel can proxy operator requests
- the viewer can serve the seeded or promoted live site
- the packaged stack can host a full operator-managed flow

## Common first-run notes

### Empty viewer on first start

That is normal. The operator entrypoint seeds an empty site so the viewer can start before any meetings exist.

### Port conflicts

If one of the default ports is already in use, change the relevant values in `deployment/.env`.

### Job state after restart

If the operator restarts during work, queued or running jobs are marked `interrupted` on next startup.

## Where to go next

- Service and storage boundaries: [System overview](./system-overview.md)
- Deployment shape: [Deployment stack](./deployment-stack.md)
- Runtime behavior: [Operator runtime](./operator-runtime.md)
