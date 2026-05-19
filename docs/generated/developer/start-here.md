# Start here

If you are new to Cassini, the easiest way to think about it is:

> Cassini takes a meeting URL, records the meeting, turns it into a structured meeting artifact, and makes that result viewable in the browser.

You do **not** need to understand WebRTC, RTP, codecs, or transcript formats before you start. Those details matter later, but they are not the first thing you need.

## The short version

Cassini has three core stages:

1. **Record** — join a Nextcloud Talk room and capture source media
2. **Build** — turn that source media into a structured meeting artifact
3. **Publish** — turn one or more built meetings into a static viewer site

In the browser, Cassini has two separate surfaces:

- the **control panel** for starting and watching jobs
- the **viewer** for reading the published results

## What you should do first

Start with the happy path:

- bring up the local Talk harness
- bring up the deployment bundle
- start a room
- paste the room URL into the control panel
- wait for the job to finish
- open the viewer

That walkthrough lives here:

- [Quick start](./quick-start.md)

## The main system picture

At a high level, Cassini is a file-driven pipeline:

```text
Nextcloud Talk room
  -> .run bundle
  -> .meeting bundle
  -> .site bundle
  -> viewer
```

There is also a portable single-file output mode:

```text
Nextcloud Talk room
  -> capture/build pipeline
  -> one .opus file
```

That `.opus` file is not a separate recording system. It is a packaging form built on top of the same underlying flow.

## The two main ways developers meet Cassini

### 1. Operator-managed stack

This is the easiest way to see the full product shape.

It includes:

- operator
- control panel
- viewer
- shared published-site storage

Use this when you want to understand the deployed runtime and the browser experience.

### 2. Standalone CLI flow

This is the easiest way to understand the raw artifact pipeline.

Typical flow:

```bash
./bin/cassini record --call "$CALL_URL" --out ./runs/demo.run
./bin/cassini build ./runs/demo.run --out ./meetings/demo.meeting
./bin/cassini publish ./meetings --out ./site
./bin/cassini serve ./site
```

Use this when you want to debug stage boundaries directly.

## Where to go next

- Want to get something running now: [Quick start](./quick-start.md)
- Want the high-level architecture first: [Mental model](./mental-model.md)
- Want to understand the local runtime layout: [Running the local developer stack](./local-developer-stack.md)
- Want the record/build/publish details: [Core pipeline](./core-pipeline.md)
