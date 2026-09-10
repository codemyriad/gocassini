<script lang="ts">
  import type { JobDetailResponse } from "./operator/types";
  import { audioUseLabel, sourceAudioProgress, transcriptUseLabel } from "./operator/sourceAudio";
  export let detail: JobDetailResponse;
  $: audio = detail.source_audio;
  $: waiting = audio?.wait_until && detail.job.stage === "build" && detail.job.state === "queued";
  const bytes = (value: number) => value < 1024 * 1024
    ? `${(value / 1024).toFixed(1)} KB` : `${(value / (1024 * 1024)).toFixed(1)} MB`;
  const time = (value?: string) => value ? new Date(value).toLocaleString() : "Time unavailable";
</script>

{#if audio}
  <section class="grid gap-4 rounded-box border border-base-content/15 bg-base-100 p-4" aria-label="Participant audio">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h3 class="text-base font-semibold">Participant audio</h3>
      <span class="text-xs text-base-content/60">Updates every 2 seconds while this recording is open</span>
    </div>
    <p class="text-sm text-base-content/75">{waiting ? "Processing will start when registered uploads arrive, or when the upload deadline passes." : sourceAudioProgress(detail.job, audio.ingest_enabled)}</p>
    {#if !audio.collection_enabled}
      <p class="text-sm text-warning">New participant uploads are disabled on this installation.</p>
    {/if}
    {#if audio.error}
      <p role="alert" class="text-sm text-error">{audio.error}</p>
    {/if}

    <div class="grid gap-2">
      <h4 class="text-sm font-semibold">1. Capture and upload</h4>
      {#if waiting}
        <p class="text-sm" role="status">Waiting for registered captures, until {time(audio.wait_until)} at the latest.</p>
      {/if}
      {#each audio.expected ?? [] as capture}
        <p class="text-sm">{capture.owner} · Capture {capture.capture_id?.slice(0, 8) ?? new Date(capture.call_start_ms).toLocaleTimeString()} · {capture.status === "recording" ? "Browser reports recording locally" : capture.status === "stored" ? "Registered capture received" : capture.status === "timed_out" ? "Upload deadline passed; audio has not arrived" : "Waiting for upload"}<span class="text-xs text-base-content/60"> · Last report {time(capture.updated_at)}</span></p>
      {/each}
      {#each audio.receiving as upload}
        <p class="flex items-center gap-2 text-sm" role="status">
          <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
          Receiving {upload.owner}’s audio — {bytes(upload.bytes)} received; validation pending
        </p>
      {/each}
      {#if audio.uploads.length}
        <div class="overflow-x-auto">
          <table class="table table-sm">
            <thead><tr><th>Participant</th><th>Upload</th><th>Received</th></tr></thead>
            <tbody>
              {#each audio.uploads as upload}
                <tr>
                  <td class="font-medium">{upload.owner}{#if upload.call_start_ms}<div class="text-xs font-normal">Capture {upload.capture_id?.slice(0, 8) ?? new Date(upload.call_start_ms).toLocaleTimeString()}</div>{/if}</td>
                  <td>
                    <span class:text-success={upload.complete} class:text-warning={!upload.complete}>
                      {upload.complete ? "Stored" : "Audio file missing"}
                    </span>
                    <span class="text-base-content/60"> · {upload.segments} segment{upload.segments === 1 ? "" : "s"} · {bytes(upload.bytes)}</span>
                    {#if upload.exclusion_reason}<p class="text-xs text-warning">Currently excluded: {upload.exclusion_reason}</p>{/if}
                  </td>
                  <td class="text-xs">{time(upload.received_at)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {:else if !audio.error}
        <p class="text-sm text-base-content/60">No stored uploads matched to this recording.</p>
      {/if}
      <p class="text-xs text-base-content/50">Receiving shows bytes reaching Cassini after Nextcloud’s proxy. A fast upload may appear directly as stored.</p>
    </div>

    <div class="grid gap-3">
      <h4 class="text-sm font-semibold">2. Use in the meeting</h4>
      {#if detail.job.source_audio_rebuild?.rebuild_count}
        <p class="text-xs text-base-content/65">{detail.job.source_audio_rebuild.rebuild_count} automatic rebuild{detail.job.source_audio_rebuild.rebuild_count === 1 ? "" : "s"} scheduled for late audio.</p>
      {/if}
      {#each audio.attempts as evidence}
        {@const attempt = detail.attempts.find(item => item.attempt_number === evidence.attempt)}
        <details open={evidence.attempt === detail.job.current_attempt_number} class="rounded-lg border border-base-content/10 p-3">
          <summary class="cursor-pointer text-sm font-medium">
            Build {evidence.attempt}{evidence.attempt === detail.job.current_attempt_number ? " · current" : " · previous"}
            {#if attempt?.state === "succeeded" && attempt.publish_finished_at} · Published {time(attempt.publish_finished_at)}
            {:else if attempt?.state === "failed" || attempt?.state === "interrupted"} · Processing failed
            {:else if attempt?.build_finished_at} · Built, publication not complete
            {:else if attempt?.build_started_at} · Building
            {:else} · Not built yet{/if}
          </summary>
          {#if evidence.error}
            <p class="mt-3 text-sm text-error">{evidence.error}</p>
          {:else if !evidence.available}
            <p class="mt-3 text-sm text-base-content/60">{attempt?.build_finished_at ? "Build evidence is no longer available." : "Waiting for the build’s audio-use evidence."}</p>
          {:else if !evidence.participants.length}
            <p class="mt-3 text-sm text-base-content/60">No participant-audio use was reported by this build.</p>
          {:else}
            <div class="mt-3 grid gap-3">
              {#each evidence.participants as use}
                <div class="grid gap-1 border-t border-base-content/10 pt-3 text-sm">
                  <div class="flex flex-wrap justify-between gap-2">
                    <strong>{use.owner}{#if use.session_id}<span class="ml-2 text-xs font-normal" title={use.session_id}>Session {use.session_id.slice(0, 12)}</span>{/if}</strong>
                    <span>{(use.spliced_ms / 1000).toFixed(1)} seconds used · {use.placed}/{use.segments} segments placed</span>
                  </div>
                  <p><span class="text-base-content/60">Meeting audio:</span> {audioUseLabel(use)}</p>
                  <p><span class="text-base-content/60">Transcription:</span> {transcriptUseLabel(use)}</p>
                  {#if use.transcript_source === "merged-mix"}
                    <p class="text-xs text-base-content/60">Transcribed from the combined audio; this does not establish individual word attribution.</p>
                  {/if}
                  {#if use.skipped > 0}<p class="text-warning">{use.skipped} segment{use.skipped === 1 ? "" : "s"} skipped.</p>{/if}
                  {#if use.mix_skip_reason}<p class="text-warning">{use.mix_skip_reason}</p>{/if}
                  {#each use.rejections ?? [] as reason}<p class="text-xs text-warning">{reason}</p>{/each}
                </div>
              {/each}
            </div>
          {/if}
        </details>
      {/each}
    </div>
  </section>
{/if}
