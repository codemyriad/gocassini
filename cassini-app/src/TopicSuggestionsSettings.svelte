<script lang="ts">
  // Topic suggestions in Settings (docs/proposals/automatic-tagging): the
  // optional model, the switch that runs it after each meeting, checking past
  // meetings, and what is waiting or blocked. Counts are work counts only;
  // what was suggested is never shown here. Until the preview is unlocked on
  // the server (`cassini-operator topics-preview on`) the operator lists no
  // model and answers 404 here, and there is no card at all.
  import { onDestroy } from "svelte";
  import { OperatorHttpError } from "./operator/client";
  import type { OperatorClient } from "./operator/client";
  import type { SpeechModelInventory, TopicSettingsStatus } from "./operator/types";
  import OptionalModelCard from "./OptionalModelCard.svelte";
  import { topicSuggestionsModel } from "./speechModels";
  import { TOPICS_PREVIEW_NOTE } from "cassini-viewer/topics";
  export let client: OperatorClient | null = null;
  export let inventory: SpeechModelInventory | null = null;
  // How often the counts refresh while the card is open.
  export let refreshMs = 10_000;

  let status: TopicSettingsStatus | null = null;
  // The operator keeps topic suggestions locked: no card.
  let hidden = false;
  let busy = false;
  let error = "";
  let since = "";
  let queuedNote = "";
  let timer: ReturnType<typeof setInterval> | undefined;

  $: model = topicSuggestionsModel(inventory);
  $: modelReady = !!model?.ready;
  // Load once the model card exists, and again when it becomes ready.
  $: if (client && model) void load(modelReady);

  async function load(_ready?: boolean) {
    if (!client) return;
    try {
      status = await client.getTopicSettings();
      hidden = false;
      if (!timer && refreshMs > 0) timer = setInterval(() => void load(), refreshMs);
    } catch (e) {
      if (e instanceof OperatorHttpError && e.status === 404) {
        hidden = true;
        status = null;
        return;
      }
      error = e instanceof Error ? e.message : String(e);
    }
  }
  onDestroy(() => timer && clearInterval(timer));

  async function run(action: () => Promise<unknown>) {
    if (busy) return;
    busy = true;
    error = "";
    try {
      await action();
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  const toggle = (enabled: boolean) => run(() => client!.putTopicSettings({ enabled }));
  const backfill = () => run(async () => {
    const result = await client!.startTopicBackfill(since || undefined);
    queuedNote = result.queued === 0
      ? "Every past meeting is already checked or waiting."
      : `Queued ${result.queued} past ${result.queued === 1 ? "meeting" : "meetings"}. About ${duration(result.estimateSeconds)} of work is waiting.`;
  });
  const retry = () => run(() => client!.retryTopicMeetings());

  function duration(seconds: number): string {
    if (seconds < 90) return "a minute";
    if (seconds < 5400) return `${Math.round(seconds / 60)} minutes`;
    return `${Math.round(seconds / 3600)} hours`;
  }

  $: counts = status?.counts ?? {};
  $: waiting = (counts.queued ?? 0) + (counts.running ?? 0);
  $: notChecked = counts.unavailable ?? 0;
  $: countLine = [
    `Checked ${counts.done ?? 0}`,
    waiting && `${waiting} waiting`,
    notChecked && `${notChecked} could not be checked`,
    counts.blocked && `${counts.blocked} blocked`,
  ].filter(Boolean).join(" · ") + ".";
  $: pausedText = status?.reason === "model-fault"
    ? "Paused: the model's check failed. Check it again above."
    : status?.reason === "model-unavailable" ? "Paused until the model is installed and checked." : "";
  $: switchDisabled = busy || !status?.available || (!status.enabled && !status.ready);
</script>

{#if !hidden}
<OptionalModelCard {client} {inventory} {model} title="Topic suggestions" headingId="topic-suggestions-heading" preview>
  For Cassini to suggest topics for meetings from their transcripts, download this model. It reads transcripts on this server's CPU; no text leaves the server. Downloading it does not switch suggestions on.
  <div slot="after" class="topics-after">
    {#if status && !status.available}
      <p class="set-row-sub">Topic suggestions need Cassini running as a Nextcloud app.</p>
    {:else if status}
      <p class="set-row-sub">{TOPICS_PREVIEW_NOTE}</p>
      <label class="topics-switch">
        <input type="checkbox" class="toggle toggle-sm" checked={status.enabled} disabled={switchDisabled}
          on:change={(e) => toggle(e.currentTarget.checked)} />
        <span>Suggest topics after each meeting</span>
      </label>
      {#if !status.enabled && !status.ready}
        <p class="set-row-sub">Download the model and let its check pass first.</p>
      {/if}
      {#if status.enabled}
        {#if pausedText}<p class="op-error" role="status">{pausedText}</p>{/if}
        <p class="set-row-sub" data-testid="topic-counts">
          {countLine}
          {#if status.backfillSince === "all"}Past meetings are included.{:else if status.backfillSince}Meetings since {status.backfillSince} are included.{:else}Meetings from before suggestions were switched on are not checked yet.{/if}
        </p>
        <div class="topics-backfill">
          <label>From <input type="date" class="input input-xs" bind:value={since} aria-label="Check meetings recorded on or after" /></label>
          <button type="button" class="btn btn-xs" disabled={busy} on:click={backfill}>Check past meetings</button>
        </div>
        {#if queuedNote}<p class="set-row-sub" role="status">{queuedNote}</p>{/if}
        {#if status.blocked.length}
          <div class="topics-blocked">
            <p class="set-row-sub">{status.blocked.length} {status.blocked.length === 1 ? "meeting" : "meetings"} could not be checked after several tries.</p>
            <ul>
              {#each status.blocked.slice(0, 5) as row}<li><code>{row.meeting}</code>: {row.error || row.reason}</li>{/each}
            </ul>
            <button type="button" class="btn btn-xs" disabled={busy} on:click={retry}>Retry</button>
          </div>
        {/if}
      {/if}
    {/if}
    {#if error}<p class="op-error">{error}</p>{/if}
  </div>
</OptionalModelCard>
{/if}

<style>
  .topics-after { display: grid; gap: 8px; }
  .topics-switch { display: flex; gap: 8px; align-items: center; font-weight: 500; }
  .topics-backfill { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
  .topics-backfill label { display: flex; gap: 6px; align-items: center; }
  .topics-blocked ul { margin: 4px 0 8px 1.2em; list-style: disc; font-size: 0.9em; }
</style>
