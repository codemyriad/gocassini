# Cassini Readable

`cassini-readable` is the readable-transcript cleanup component of the Cassini
suite.

It takes a canonical `transcript.words.v1.json` transcript and produces a
`transcript.readable.v1.json` presentation transcript.

This component is intentionally narrower than the full transcriber:

- input: canonical transcript JSON,
- output: readable transcript JSON,
- optional LLM cleanup backends,
- optional passthrough mode when no cleanup backend is available.

The current implementation uses the runtime and prompt logic that also powers
readable transcript generation inside `cassini-transcriber`.

## Tools

- `bin/build-readable-transcript.sh`: build one readable transcript from one
  canonical transcript.
- `bin/compare-models.sh`: compare OpenAI-compatible readable cleanup models on
  an existing canonical transcript.

## Usage

Standalone readable transcript generation:

```bash
./cassini-readable/bin/build-readable-transcript.sh \
  --input-transcript /path/to/transcript.words.v1.json
```

If no cleanup backend is configured, this tool can still emit a readable
transcript in `--readable-backend none` mode by windowing the source transcript
without LLM rewriting.

Model comparison on an existing transcript:

```bash
./cassini-readable/bin/compare-models.sh \
  --input /path/to/transcript.words.v1.json
```

## Scope note

The local model training harness is currently being developed separately in the
`gocassini-second` worktree. This package is the runtime-facing readable tool in
the main repo.
