import type { Job, SourceAudioUse } from "./types";

export function sourceAudioProgress(job: Job, ingestEnabled: boolean): string {
  if (!ingestEnabled) return "Using participant uploads is disabled. Stored audio is kept for a later build.";
  if (job.source_audio_rebuild?.pending) {
    return job.stage === "done"
      ? "New audio arrived after the previous build began. Waiting for uploads to settle before rebuilding."
      : "New audio has arrived. This build may finish first; Cassini will check whether another build is needed.";
  }
  if (job.stage === "record") return "Participant audio uploads when Talk recording stops or a participant leaves the call.";
  if (job.state === "failed" || job.state === "interrupted" || job.state === "blocked") {
    return "Processing has not completed. Received uploads alone do not confirm that the audio was used.";
  }
  if (job.stage !== "done") return "Processing the recording. Audio use will appear below when the build writes its evidence.";
  return "Processing finished. The build results below show which uploads were used.";
}

export function transcriptUseLabel(use: SourceAudioUse): string {
  if (use.spliced_ms <= 0) return "Not used";
  if (use.transcript_source === "per-participant") return "Used for this participant’s transcript";
  if (use.transcript_source === "merged-mix") return "Used in the transcribed mix";
  return "Use not confirmed";
}

export function audioUseLabel(use: SourceAudioUse): string {
  return use.mix_spliced && use.spliced_ms > 0 ? "Included in meeting audio" : "Recorded call audio retained";
}
