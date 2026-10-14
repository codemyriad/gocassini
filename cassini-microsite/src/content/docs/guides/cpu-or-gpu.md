---
title: CPU or GPU
description: CPU is the default and a GPU is optional; what the GPU changes, and how to move an existing install onto one.
source: docs/README.md
copied: "2026-09-17"
---

CPU is the default. The portable image runs transcription on amd64 and arm64
with no GPU at all, and that is what an install gets unless you ask for
something else.

A GPU is optional: an NVIDIA card with the Container Toolkit, on x86_64, for
faster transcription.

Transcription runs inside your own infrastructure either way: sherpa-onnx with
Moondream's Parakeet Ultra (NVIDIA's small Parakeet model on the CPU's Fast
tier) and Silero VAD, in-process, with no audio and no transcript leaving the
host.

## Two images, one setting

| | CPU | GPU |
|---|---|---|
| Tag | `X.Y.Z` | `X.Y.Z-cuda` |
| Architectures | `linux/amd64` and `linux/arm64` | x86_64 only |
| Needs | any Docker engine | NVIDIA driver + Container Toolkit |
| Default model | int8 Parakeet Ultra 0.6B | fp32 Parakeet Ultra 0.6B |

You do not pick the tag by hand. **The compute device is a property of the
deploy daemon**: set its **Compute device** to CUDA and AppAPI tries
`<image-tag>-cuda` first. A CPU install and a GPU install therefore differ by
that one setting and nothing else — you do not re-register the app to move
between them.

If a CUDA daemon falls back to the plain image, Cassini says so rather than
quietly decoding on the CPU: capture still works, `/operator/status` reports CUDA
unavailable, and builds enter `build/blocked` with instructions to install the
matching `-cuda` image.

## The three quality tiers

The tier is an administrator's setting in the app, under **Operator › Settings**.
It asks for an outcome; the recorder picks the model for the device it is
actually running on.

| Tier | On a CPU | On a GPU |
|---|---|---|
| Fast | a small English model (110M CTC) | fp32 Parakeet Ultra 0.6B |
| **Balanced** (the default) | int8 Parakeet Ultra 0.6B | fp32 Parakeet Ultra 0.6B |
| Best | fp32 Parakeet Ultra 0.6B | fp32 Parakeet Ultra 0.6B |

fp32 is the only precision the CUDA execution provider runs without falling back
to the CPU for parts of the graph, so every GPU tier resolves to the same fp32
model.

Since October 2026 the 0.6B model is
[Parakeet Ultra](https://huggingface.co/moondream/parakeet-ultra): Moondream's
post-training of NVIDIA's Parakeet 0.6B v3, with the same architecture and
languages. It replaced v3 on every tier. It matched or beat v3 on all eleven
public test sets we ran, and on recorded meetings it dropped far fewer passages;
on a CPU its int8 build was also more accurate than v3's on our own meetings, at
slightly less CPU time. An install that used v3 moves to Ultra when it is
upgraded: it downloads the Ultra model for its tier (about 465 MB on a CPU,
1.5 GB on a GPU), recordings made meanwhile wait for it, and v3's files are
deleted once Ultra has passed its check. Meetings already transcribed keep their
v3 transcripts.

Models download into the model cache on the persistent volume when an
administrator installs them in Settings. On a host with no outbound network
access, set `CASSINI_DISALLOW_MODEL_DOWNLOAD=1` and import the model files with
`cassini models import`: a build whose model is missing keeps the audio and
names the model, rather than starting and failing at the network.

## What the GPU buys, and what it does not

- **The device changes the precision as well as the speed.** A GPU runs fp32
  Parakeet Ultra at every tier; a CPU runs its int8 build at Balanced and fp32
  at Best. It is the same model, with the same languages.
- **It accelerates transcription only.** Live capture is CPU-bound either way,
  so recording a call is not the part a GPU helps with.
- **arm64 is CPU only.** There is no ARM CUDA image; a 64-bit ARM server runs
  the portable image.

## Moving an existing install to the GPU

A device change is not a data migration.

1. Install the NVIDIA driver and Container Toolkit on the engine that runs the
   app, and check `docker run --rm --gpus all nvidia/cuda:12.4.1-base-ubuntu22.04 nvidia-smi`.
2. Set the deploy daemon's **Compute device** to CUDA (or register a second
   daemon for a GPU node and register Cassini against it).
3. Confirm `/operator/status` shows `"device": "cuda"` with
   `"device_usable": true`.
4. Use **Rerun** in the app to re-transcribe an existing recording on the GPU.
   The preserved recording is reprocessed; nothing is re-recorded.

On a CUDA image, temporary RAM or VRAM pressure is retried with exponential
backoff rather than failed immediately; after enough unsuccessful deferrals the
job moves to `build/blocked`, and **Rerun** creates a fresh attempt once capacity
is back.

If the Nextcloud host has no GPU, HaRP can drive a **remote Docker engine** over
its tunnel. The steps, including the Docker-in-LXC caveat, are in
[`docs/exapp-install.md`](https://github.com/codemyriad/gocassini/blob/main/docs/exapp-install.md#remote-gpu-node).

## Related

- [Install on Nextcloud](/docs/getting-started/install) — image tags and the GPU
  prerequisites.
- [Privacy and data processing](/docs/guides/privacy) — why neither device
  sends anything anywhere.
