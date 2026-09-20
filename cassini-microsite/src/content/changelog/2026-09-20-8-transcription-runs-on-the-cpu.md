---
title: Transcription runs on the CPU when there is no GPU
date: 2026-09-20
version: 0.2.0-beta.7
---

Each build works out its own device: CUDA when the image carries the CUDA runtime and an NVIDIA device is visible, and the CPU otherwise. An installation on a plain image transcribes again.

This reverses the behaviour of 28 August, where transcription on a plain image waited in a blocked state until the `-cuda` image was installed. If you have blocked jobs from that period, they still rerun.
