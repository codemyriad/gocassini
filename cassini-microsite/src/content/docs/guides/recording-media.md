---
title: Recording media and publication
description: Choose which recording media to retain, what to publish, and how to enable transcription.
source: docs/proposals/source-media-disposal/tutorial.md
copied: "2026-10-08"
---

As an administrator, open **Operator → Publish pipeline → Recording media and
publication**. First choose what stays on the server after processing, then choose
what people receive. Save applies these choices to newly accepted recordings;
existing and active recordings keep their saved policy.

## Choose what to keep

| Keep after each recording | What happens after processing | Rerun transcription? |
| --- | --- | --- |
| **Nothing** | Delete recording media, including audio, video and raw packet logs. Keep transcription artifacts, including published JSON. | No |
| **Full audio + video** | Keep captured audio, available camera video and associated recording files under Storage policies. | While the source exists |
| **Audio-only** | Keep captured audio and associated recording files under Storage policies, without camera video. | While the source exists |

**Nothing** captures audio temporarily for one processing run. It deletes source
media after publication succeeds or processing permanently fails. Temporary retries
within that run remain possible. If transcription permanently fails, there may be
no usable transcript. Transcripts, meeting information and diagnostic logs follow
their existing retention policies.

```text
Temporary audio -> transcription -> JSON publication -> delete recording media
                         |
                   terminal failure -----------------> delete recording media
```

This removes Cassini-managed recording media; it does not mean audio never touches
disk. External backups and snapshots are outside this policy.

## Choose what to publish

- **Include audio (.opus):** playable audio with the transcript and meeting
  information. Captured video stays on the server; it is not published.
- **Transcription only (.json):** transcript, speaker blocks, summaries, metadata,
  tags and annotations without audio playback.

Choosing **Nothing** selects JSON automatically and disables audio publication.
Both retained-media choices allow either publication format. Publishing JSON alone
does not delete retained source media. Reruns keep the original publication format.

## Enable transcription before selecting Nothing

Transcription is optional and starts off. Keeping no recording media requires
enabled transcription with a ready model.

1. Select **Set up transcription** in the setup notice. The link scrolls to the
   **Transcript** step and moves keyboard focus there.
2. The step is one list: **Off**, then a row per quality tier showing that tier's
   model, its size and whether it is installed. A tier whose model is not ready
   is greyed. Select **Download** on the tier you want, or **Check** if the model
   is installed but not yet checked on this build.
3. When a download you started finishes and the model is ready, that tier is
   picked and transcription turns on. A tier that is already ready can be picked
   directly.
4. Select **Save** to apply the model and recording choices together.

If downloads are disabled, follow the step's offline pack/import instructions.
With transcription off, audio publication can still work without a model, but the
**Nothing** option cannot be saved.

## Confirm deletion

After a new recording finishes, open its job details and wait for **Media deleted**.
A JSON meeting without playback is not proof that server cleanup has completed.
If cleanup fails, the operator shows the error and retries automatically. After
fixing the reported problem, **Retry media deletion** requests another cleanup
attempt; it does not rerun transcription.

Use **Operator → Storage** to configure how long retained recordings, output
archives, attempt history, logs and published Nextcloud meetings remain. Those
age-based policies are separate from deleting recording media after processing.
