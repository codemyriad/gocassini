# Quick start

This walkthrough is the fastest way to see Cassini working end to end on your machine.

Goal:

- start a local Nextcloud Talk environment
- start the Cassini deployment bundle
- create and join a meeting
- submit that meeting to the operator
- watch the job run through record -> build -> publish
- open the final result in the viewer

## Before you begin

Assumptions:

- you are at the repo root
- Docker is available
- Go is available

Why Go? In this checkout, `./bin/cassini` builds a temporary Cassini binary before running it.

> Command note: these docs use `./bin/cassini` from the repo root. If you have an equivalent installed wrapper named `cassini`, you can use that instead.

Important notes for this harness-based quickstart:

- use `./bin/cassini dev stack up`, not raw harness `docker compose`, because the wrapper runs the harness scripts and additional setup after Compose starts
- use `127.0.0.1`, not `localhost`, for local harness URLs, including in the browser
- this harness-based quickstart currently does not work on macOS because of networking issues in the harness stack

## 1. Start the local Talk harness

From the repo root:

```bash
./bin/cassini dev stack up
```

This starts the local Nextcloud Talk stack used for development.

Do not swap this for raw harness `docker compose` unless you are intentionally debugging harness internals.

If you want to confirm it is up, open:

- `http://127.0.0.1:28080/`

Use `127.0.0.1` here, not `localhost`.

The local harness uses the default Nextcloud admin credentials:

- username: `admin`
- password: `admin`

## 2. Create a Talk room

Create a room and capture the returned URL:

```bash
CALL_URL="$(./bin/cassini dev room create --name "Cassini local demo" | tail -n1)"
echo "$CALL_URL"
```

You can use that URL in two places:

- in your browser, to join the meeting
- in the Cassini control panel, so the operator can join and record it

If you prefer, you can also create or inspect rooms through the Nextcloud/Talk UI, but the command above is the shortest path.

## 3. Start the Cassini deployment bundle

In another terminal:

```bash
cd deployment
docker compose up --build
```

On later runs, once images already exist, `docker compose up` is usually enough.

This brings up three services:

- operator: `http://127.0.0.1:4000/`
- control panel: `http://127.0.0.1:4173/`
- viewer: `http://127.0.0.1:8765/`

## 4. Open the browser surfaces

Open these in your browser:

- the Talk room URL from `CALL_URL`
- control panel: `http://127.0.0.1:4173/`
- viewer: `http://127.0.0.1:8765/`

At this point the viewer may still be empty. That is expected until a publish succeeds.

## 5. Join the meeting and create some signal

In the Talk room:

- join the room
- speak for 20–60 seconds
- if you want, add a second participant or browser tab for a slightly more realistic test

The goal here is just to produce a small, obvious meeting for Cassini to capture.

## 6. Submit the meeting to Cassini

In the control panel:

1. paste the same `CALL_URL`
2. start the job

The operator will join the room as its recorder participant and start executing the pipeline.

## 7. Watch the job run

In the control panel, you should see the job move through states like:

```text
record/queued
record/running
build/queued
build/running
publish/queued
publish/running
done/succeeded
```

How the recording ends:

- easiest path: leave the room and wait for the room-empty grace period
- alternate path: use the control panel stop action while the job is `record/running`

A stop request ends recording. It does **not** necessarily cancel the whole job. If Cassini finalized a usable recording, the job can still continue into build and publish.

## 8. Open the result in the viewer

Once the job reaches `done / succeeded`, refresh the viewer:

- `http://127.0.0.1:8765/`

You should now be able to open the published meeting and inspect the output.

## What you just exercised

You just ran the full operator-managed flow:

1. the **harness** provided a local Nextcloud Talk environment
2. the **control panel** sent a job to the **operator**
3. the **operator** ran record, build, and publish
4. the **viewer** served the published static site

If you want the architecture behind that flow, read:

- [Mental model](./mental-model.md)
- [Running the local developer stack](./local-developer-stack.md)
- [Operator stack](./operator-stack.md)

## Common first-run notes

### Ports

The default deployment ports come from `deployment/.env`:

- operator: `4000`
- control panel: `4173`
- viewer: `8765`

If you need different ports or host-visible storage paths, see:

- [Configuration reference](./reference/configuration.md)

### If the viewer is empty

That usually means one of these is true:

- the job has not reached `publish` yet
- the job failed before publish
- there are no successful `.meeting` artifacts available to publish

See:

- [Troubleshooting](./reference/troubleshooting.md)
- [Operator stack](./operator-stack.md)

### If you want a deeper pipeline-first view

After this quick start, the next best page is:

- [Core pipeline](./core-pipeline.md)
