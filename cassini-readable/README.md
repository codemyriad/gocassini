# Cassini Readable

`cassini-readable` is the readable-transcript cleanup component of the Cassini
suite.

It takes a canonical `transcript.words.v1.json` transcript and produces a
`transcript.readable.v1.json` presentation transcript.

This component was originally a standalone wrapper around the old Python
transcriber package.

That Python package has been retired. Readable transcript generation now lives
inside the native Go build pipeline in `cassini-go-recorder/internal/transcribe`
and is exercised through `cassini build`.

## Current status

- product flow: supported through `./bin/cassini build`
- standalone readable-only CLI in this worktree: not currently exposed
- old shell wrappers in `bin/`: retained only as deprecation stubs

## Recommended usage

Build a meeting artifact or portable meeting through the product CLI:

```bash
./bin/cassini build /path/to/meeting.mkv --out /tmp/meeting.meeting
```

If you specifically need a standalone readable-transcript CLI again, it should
be reintroduced on top of the native Go pipeline rather than revived from the
removed `cassini-transcriber` package.

## Scope note

The local model training harness is currently being developed separately in the
`gocassini-second` worktree. This directory is no longer the runtime-facing
readable tool in the main repo.
