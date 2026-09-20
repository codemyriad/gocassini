---
title: Transcription can run on an NVIDIA GPU
date: 2026-08-28
version: 0.2.0-beta.4
---

Install the matching `-cuda` image on a deploy daemon with a GPU and transcription runs there instead of on the CPU. Readiness failures are now reported explicitly rather than discovered later, and every generated file records which device transcribed it.

If you already run Cassini, this one asks something of you. Capture and preservation of Talk audio carry on as before, but transcription enters a blocked state instead of quietly running on the CPU. Install the `-cuda` image, then choose Rerun for each blocked job in Cassini Admin. Nothing is lost while you wait.
