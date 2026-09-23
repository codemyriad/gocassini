---
shaping: true
---

# Optional transcription and persistent models — Frame

## Source

Silvio, 2026-09-21:

> Read /tmp/nextcloud-storage-research.txt
>
> We bundle parakeet model in the image currently.
> So people upgrading gocassini will redownload the same model in a new image.
> We wish to change this.
>
> 1. Transcriptions should be optional - gocassini should work even with no parakeet/transcription step
> 2. Users should be able to download the model at configure time, and observe/monitor download progress
> 3. upgrading to a new gocassini version should not require you to donwload more than necessary
>
> Research and shape up how to fix this.
> Use skilld from /home/silvio/.local/share/shaping-skills

Silvio's rollout clarification, 2026-09-21:

> We're the only users so far. No nee to support that case. Aim for simplicity

This answers the question of preserving bundled weights during the first upgrade. Supporting that transition is out of scope; the existing installation can configure with a download or local import once.

Silvio's distribution requirement, 2026-09-21:

> Note: I wish to host parakeet models that gocassini downloads on dist.gocassini.com, powered by bunny.net or cloudflare (I'm taking care of that decision - but it will be a fast CDN)

Connected installations download from `dist.gocassini.com`. CDN provider selection and setup belong to Silvio. The proposal specifies the artifact and HTTP contract without choosing a provider.

Silvio's offline installation requirement, 2026-09-21:

> We also need to support air gapped systems.
> Meaning it should be possible (it can be not UI straightforward - it's ok to need to use a terminal) for people installing cassini on a server with no outgoing internet access, by copying manually the model files.

Manual model provisioning on an air-gapped server is a required installation path. A documented terminal import is sufficient for provisioning; it must validate supported models without contacting the CDN or fetching missing dependencies. Settings can then enable them locally. This is distinct from the excluded automatic migration of models out of old application images.

Silvio's preparation-command suggestion, 2026-09-21:

> We could also have a `cassini models pack` that could download and prepare models for `cassini models import` to use

The recommended offline handoff is a single complete model package prepared with `models pack` on a connected machine, manually transferred, and consumed by local-only `models import`. Direct import of supported manually copied files remains available.

The supplied research is preserved verbatim in [nextcloud-storage-research.txt](nextcloud-storage-research.txt). It suggests AppAPI persistent storage, Nextcloud llm2, LocalAI download jobs, Ollama resumable transfers, and Hugging Face's separation of files from model revisions.

## Problem

Recording and publishing a meeting currently entail transcription. Default model weights arrive with the application image, even for an installation that only needs recordings. Other models download during a meeting build, when the administrator has little visibility into progress.

Model acquisition, application releases, and meeting processing have coupled lifecycles. An image update should not determine whether an already acquired model is available or needs downloading again.

An installation with no internet egress cannot use configure-time downloads. Removing bundled models therefore also requires a supported way to copy the complete model and its dependencies onto that server and register them locally.

The upgrade premise needs qualification: Docker reuses identical layers, and Cassini already caches its CUDA model in a shared base image. This research has not measured redundant transfer between released image digests. The design objective remains useful: reuse models independently of application images, and let installations choose whether to acquire them at all.

## Outcome

A fresh installation can record, publish, and play meetings without acquiring or running a speech model. An administrator can install transcription from configuration, see what is happening, and recover from an interruption. A connected machine can prepare one complete model package; an air-gapped installation can import that package or manually copied model files and dependencies through the terminal, then transcribe with no internet access. Future application upgrades reuse the same validated model files. Favor a small extension of the recorder model commands and operator; a bridge release and automatic bundled-model migration are out of scope.

## Scope of this work

Research and a shaped proposal, not an implementation or a release. [README.md](README.md) holds requirements, alternatives, the recommended shape, and its breadboard. [spike-current-system.md](spike-current-system.md) records evidence and remaining release validation. Defaults and rollout choices identified as proposed have not been agreed by Silvio.
