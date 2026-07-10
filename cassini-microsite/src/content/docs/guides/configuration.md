---
title: Configuration
description: Environment variables, flags, and options for tuning Cassini's behaviour.
---

Cassini is configured through environment variables and command-line flags. There is no config file — every option can be set inline or exported in your shell profile.

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CASSINI_CACHE_ROOT` | `~/.cache/cassini` | Where Cassini stores downloaded models and temporary data |
| `CALL_URL` | — | Nextcloud Talk room URL. Can be passed as `--call` instead |
| `CASSINI_LOG_LEVEL` | `info` | Log verbosity: `debug`, `info`, `warn`, `error` |

## Command flags

### `cassini record`

```
--call <url>       Nextcloud Talk room URL (required unless $CALL_URL is set)
--out <path>       Output path for the .opus file (required)
--simulate         Run in simulate mode — no real call, writes a debug .run bundle
```

### `cassini build`

```
<input>            Path to a .run bundle
--out <path>       Output path for the .meeting artifact (required)
```

### `cassini serve`

```
<dir>              Directory of .opus files to serve
--port <n>         Port to listen on (default: 8080)
--host <addr>      Address to bind (default: 127.0.0.1)
```

### `cassini inspect`

```
<path>             Path to any Cassini artifact (.run, .meeting, .opus)
```

## Cache directory

Cassini downloads speech-to-text models on first use and caches them at `CASSINI_CACHE_ROOT`. The default is `~/.cache/cassini`. On a shared machine, you may want to point multiple users at the same directory to avoid downloading the model multiple times:

```bash
export CASSINI_CACHE_ROOT="/opt/cassini/cache"
```

Make sure the directory is writable before running. `cassini doctor` will report if it is not.

## Model selection

Cassini uses a local Whisper model for transcription. The default model is chosen to balance accuracy and speed. If you have a GPU available, Cassini will use it automatically.

To check what model is in use and whether GPU acceleration is active:

```bash
./bin/cassini doctor
```

## Logging

Set `CASSINI_LOG_LEVEL=debug` to see verbose output during recording. This is useful for diagnosing capture or transcription issues:

```bash
CASSINI_LOG_LEVEL=debug ./bin/cassini record --call "$CALL_URL" --out ./meetings/debug.opus
```

## Nextcloud credentials

When running in ExApp mode, Cassini receives credentials from Nextcloud via the AppAPI handshake. For CLI use, Cassini joins Talk rooms without authentication — it connects as an external participant. The room must allow guests, or you must supply a valid session token via the call URL.
