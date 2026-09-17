# Cassini

![Browsing recorded meetings inside Nextcloud](img/screenshots/gocassini-browse.png)

**Cassini is a recording backend for Nextcloud Talk that puts you in control of your meeting data.**

- **Record and transcribe on your own infrastructure**, on the hardware you give
  it. Speech-to-text runs in-process with sherpa-onnx and NVIDIA Parakeet
  models, from a CPU image or a CUDA image.
- **AI is opt-in.** Summaries and insights run only once you configure a model —
  local or third-party — in the app.
- **A self-contained, portable meeting file**: audio, transcript, summary and
  tags in one file, so you can take a meeting with you.

Cassini is built for the span between full control and full service. It runs on
the hardware you have, and you decide where it runs, which model it uses, and
whether a language model is involved at all.

## What you get

- Any Nextcloud Talk room, group calls and 1:1 calls. Press Record; Cassini is
  the recording server behind the same button.
- Transcripts with synced audio playback and the right name on every line. Names
  come from Talk's signalling, one stream per participant.
- Who can see a recording is Nextcloud's decision: two audiences, an
  administrator's setting, enforced by Nextcloud Files.
- One portable meeting file per meeting.
- Search, tags and insights across meetings.
- In-app AI providers for per-meeting summaries and cross-meeting insights.
- A CLI and a skill, so an external harness reads the same meetings with the
  same permissions a person has.

## Caveats

- **No live transcription or captions.** Cassini does its work after the call.
- **Audio only.** Cassini does not record video.

## Requirements and install

- Nextcloud 32 to 35, with AppAPI and a HaRP deploy daemon.
- Install from the App Store: <https://apps.nextcloud.com/apps/gocassini>
- Two images: `ghcr.io/codemyriad/gocassini:X.Y.Z` (amd64 and arm64, CPU) and
  `ghcr.io/codemyriad/gocassini:X.Y.Z-cuda` (x86_64). Set the deploy daemon's
  **Compute device** to CUDA and AppAPI pulls the `-cuda` image; the device is a
  property of the daemon, so a CPU install and a GPU install differ by that one
  setting.
- Two secrets have to match Nextcloud's own:
  `CASSINI_TALK_RECORDING_SECRET` (the secret in Talk's `recording_servers`
  setting) and `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` (the signalling
  server's `internalsecret`).
- A `cassini` service account owns the archive. Nextcloud 34.0.2 and later will
  not let an app create it, so an administrator does.
- Pointing Talk at Cassini is one `recording_servers` setting, and it is
  reversible: back up the current value before you switch, and you can put it
  back.

The full walkthrough — deploy daemon, registration, the verification checklist,
the Talk handoff and GPU setup — is in
[docs/exapp-install.md](docs/exapp-install.md).

## What leaves your server

Recording and transcription run inside your own infrastructure: no audio and no
transcript leaves the host for those steps. When you switch a language model on,
the transcript text goes to that endpoint — that is the only step that sends
anything anywhere, and with no endpoint configured nothing does.

The full note is in [docs/privacy.md](docs/privacy.md).

## CLI and skills

`cassini meetings` reads published recordings back out of Nextcloud as a
Nextcloud user, authenticated with an app password. It sees exactly what that
account may see; a recording you may not read reports as not found.

```text
cassini meetings list | rooms | search | fetch | context | summarize | tags | annotations | annotate
cassini insight run
```

A Claude skill ships in the repo at
[`.claude/skills/cassini-meetings/SKILL.md`](.claude/skills/cassini-meetings/SKILL.md).

Setup, the environment variables and worked examples are in
[docs/agent-meeting-access.md](docs/agent-meeting-access.md).

## The meeting file

Each published meeting is an ordinary Ogg Opus file. The transcript, the summary
and the tags travel in the file's OpusTags header, so any player plays the
audio and any reader that knows the format gets the rest.

The format is specified at <https://format.gocassini.com>; what Cassini writes
is described in
[docs/portable-meeting-format.md](docs/portable-meeting-format.md).

## Development

Go 1.24. `./bin/cassini` builds the CLI from this checkout and runs it from your
current working directory.

```bash
./bin/cassini --help
./bin/cassini doctor
```

- [docs/README.md](docs/README.md) — the documentation index for contributors
- [docs/cli.md](docs/cli.md) — the `cassini` CLI from a checkout: record, build,
  publish, serve, inspect
- [harness/README.md](harness/README.md) — the local Talk lab, driven by
  `cassini dev`
- [deployment/README.md](deployment/README.md) — the Docker Compose bundle for
  development and staging, not the Nextcloud app install

## Contributing

Issues and pull requests are welcome. Read
[CONTRIBUTING.md](CONTRIBUTING.md) first; it covers the branch and changelog
conventions this repo uses.

## Governance

- License: [GNU AGPLv3](LICENSE)
- Changelog: [CHANGELOG.md](CHANGELOG.md)
- Security reporting: [SECURITY.md](SECURITY.md)
- Contribution notes: [CONTRIBUTING.md](CONTRIBUTING.md)
