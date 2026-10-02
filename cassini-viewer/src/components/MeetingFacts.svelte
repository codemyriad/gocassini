<script lang="ts">
  import { tick } from "svelte";
  import { slide } from "svelte/transition";
  import { cubicOut } from "svelte/easing";
  import { Calendar, Check, ChevronRight, Clock, Copy, MessageSquare, Users } from "@lucide/svelte";

  import { formatClockTime } from "../core/transcript";
  import { formatMeetingDateShort, formatMeetingDateWithDay, hasMeetingDate } from "../viewer/catalog";
  import { popover } from "./tags/popover";
  import type { ArtifactRecordingFacts, ArtifactTimingPrecision } from "../viewer/loadArtifact";

  export let dateLabel = "";
  export let room: string | null = null;
  export let durationMs = 0;
  export let speakerNames: string[] = [];
  export let recording: ArtifactRecordingFacts | null = null;
  export let timing: ArtifactTimingPrecision | null = null;

  let open = false;
  let anchor: HTMLButtonElement;
  let panel: HTMLDivElement;

  $: dated = hasMeetingDate(dateLabel);
  $: hasAny = dated || Boolean(room) || durationMs > 0 || speakerNames.length > 0;
  $: transcribed = formatProcessed(recording?.processedAtUtc ?? null);
  $: model = recording?.model ?? "";
  $: size = [
    recording?.words != null ? `${recording.words.toLocaleString()} words` : "",
    recording?.passages != null ? `${recording.passages.toLocaleString()} passages` : "",
  ].filter(Boolean).join(" · ");
  $: roughTiming = timing && timing.level !== "word" ? timing : null;
  $: meetingId = recording?.meetingId ?? "";
  $: hasAbout = Boolean(transcribed || model || size || roughTiming || meetingId);

  let aboutOpen = false;
  let copied = false;
  let copiedTimer: ReturnType<typeof setTimeout> | undefined;

  const PROCESSED_FORMAT = new Intl.DateTimeFormat("en-GB", {
    day: "numeric",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });

  function formatProcessed(iso: string | null): string {
    if (!iso) return "";
    const date = new Date(iso);
    return Number.isNaN(date.getTime()) ? "" : PROCESSED_FORMAT.format(date);
  }

  function shortId(id: string): string {
    return id.length > 16 ? `${id.slice(0, 8)}…${id.slice(-4)}` : id;
  }

  async function copyId() {
    try {
      await navigator.clipboard.writeText(meetingId);
      copied = true;
      clearTimeout(copiedTimer);
      copiedTimer = setTimeout(() => (copied = false), 1500);
    } catch {
      copied = false;
    }
  }

  function initials(name: string): string {
    const parts = name.trim().split(/\s+/).filter(Boolean);
    return ((parts[0]?.[0] ?? "") + (parts.length > 1 ? (parts.at(-1)?.[0] ?? "") : "")).toUpperCase() || "?";
  }

  async function toggle() {
    open = !open;
    if (open) {
      await tick();
      panel?.focus({ preventScroll: true });
    }
  }
</script>

{#if hasAny}
  <button
    bind:this={anchor}
    type="button"
    class="mf-chip"
    aria-haspopup="dialog"
    aria-expanded={open}
    aria-label="Meeting details"
    on:click={toggle}
  >
    {#if dated}
      <span class="mf-chip-part">
        <Calendar size={12} aria-hidden="true" />
        {formatMeetingDateShort(dateLabel.slice(0, 10))}
      </span>
    {:else if room}
      <span class="mf-chip-part min-w-0">
        <MessageSquare size={12} aria-hidden="true" />
        <span class="truncate">{room}</span>
      </span>
    {:else if durationMs > 0}
      <span class="mf-chip-part tabular-nums">
        <Clock size={12} aria-hidden="true" />
        {formatClockTime(durationMs)}
      </span>
    {/if}
    {#if speakerNames.length > 0}
      <span class="mf-chip-part tabular-nums" title={`${speakerNames.length} ${speakerNames.length === 1 ? "participant" : "participants"}`}>
        <Users size={12} aria-hidden="true" />
        {speakerNames.length}
      </span>
    {/if}
  </button>

  {#if open}
    <div
      bind:this={panel}
      use:popover={{ anchor, close: () => (open = false) }}
      class="tag-popover mf-panel"
      role="dialog"
      aria-label="Meeting details"
      tabindex="-1"
    >
      {#if dated || room || durationMs > 0}
        <dl class="mf-facts">
          {#if dated}
            <div class="mf-fact">
              <dt><Calendar size={14} aria-hidden="true" /><span class="sr-only">Date</span></dt>
              <dd>{formatMeetingDateWithDay(dateLabel)}</dd>
            </div>
          {/if}
          {#if durationMs > 0}
            <div class="mf-fact">
              <dt><Clock size={14} aria-hidden="true" /><span class="sr-only">Duration</span></dt>
              <dd class="tabular-nums">{formatClockTime(durationMs)}</dd>
            </div>
          {/if}
          {#if room}
            <div class="mf-fact">
              <dt><MessageSquare size={14} aria-hidden="true" /><span class="sr-only">Room</span></dt>
              <dd class="truncate">{room}</dd>
            </div>
          {/if}
        </dl>
      {/if}
      {#if speakerNames.length > 0}
        <div class="mf-people">
          <p class="mf-heading">
            {speakerNames.length} {speakerNames.length === 1 ? "participant" : "participants"}
          </p>
          <ul>
            {#each speakerNames as name}
              <li>
                <span class="mf-avatar" aria-hidden="true">{initials(name)}</span>
                <span class="truncate">{name}</span>
              </li>
            {/each}
          </ul>
        </div>
      {/if}
      {#if hasAbout}
        <div class="mf-about" class:open={aboutOpen}>
          <button
            type="button"
            class="mf-about-toggle"
            aria-expanded={aboutOpen}
            aria-controls="mf-about-rows"
            on:click={() => (aboutOpen = !aboutOpen)}
          >
            <span class="mf-about-label">About this recording</span>
            <ChevronRight size={14} class="mf-about-caret" aria-hidden="true" />
          </button>
          {#if aboutOpen}
            <dl id="mf-about-rows" class="mf-about-rows" transition:slide={{ duration: 180, easing: cubicOut }}>
              {#if transcribed}
                <dt>Transcribed</dt>
                <dd>{transcribed}</dd>
              {/if}
              {#if model}
                <dt>Model</dt>
                <dd>{model}</dd>
              {/if}
              {#if size}
                <dt>Length</dt>
                <dd class="tabular-nums">{size}</dd>
              {/if}
              {#if roughTiming}
                <dt>Timing</dt>
                <dd title={roughTiming.detail}>{roughTiming.label}</dd>
              {/if}
              {#if meetingId}
                <dt>ID</dt>
                <dd class="mf-id">
                  <code title={meetingId}>{shortId(meetingId)}</code>
                  <button type="button" class="mf-copy" aria-label={copied ? "Meeting ID copied" : "Copy meeting ID"} title={copied ? "Copied" : "Copy meeting ID"} on:click={copyId}>
                    {#if copied}<Check size={12} aria-hidden="true" />{:else}<Copy size={12} aria-hidden="true" />{/if}
                  </button>
                </dd>
              {/if}
            </dl>
          {/if}
        </div>
      {/if}
    </div>
  {/if}
{/if}

<style>
  .mf-chip {
    display: inline-flex;
    flex: none;
    align-items: center;
    gap: 9px;
    max-width: 100%;
    margin-right: 6px;
    min-width: 0;
    box-sizing: border-box;
    height: 24px;
    padding: 0 8px;
    border: 1px solid var(--color-base-300);
    border-radius: 5px;
    font-size: 12px;
    font-weight: 550;
    line-height: 1;
    white-space: nowrap;
    color: color-mix(in oklch, var(--color-base-content) 75%, var(--color-base-200));
    cursor: pointer;
  }
  .mf-chip:hover,
  .mf-chip[aria-expanded="true"] {
    background-color: color-mix(in oklch, var(--color-base-content) 8%, transparent);
    color: var(--color-base-content);
  }
  .mf-chip:focus-visible {
    outline: 2px solid var(--color-primary);
    outline-offset: 2px;
  }
  .mf-chip-part {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }

  .mf-panel {
    display: flex;
    flex-direction: column;
    width: min(300px, calc(100vw - 16px));
    max-height: min(520px, calc(100vh - 16px));
    overflow-y: auto;
    overscroll-behavior: contain;
    font-size: 13px;
    --mf-strong: var(--color-base-content);
    --mf-muted: color-mix(in oklch, var(--color-base-content) 60%, transparent);
    --mf-tint: color-mix(in oklch, var(--color-base-content) 10%, transparent);
    color: var(--mf-strong);
  }
  .mf-panel:focus {
    outline: none;
  }

  .mf-facts {
    flex: none;
    display: grid;
    gap: 6px;
    margin: 0;
    padding: 10px 12px;
  }
  .mf-fact {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }
  .mf-fact dt {
    flex: none;
    display: inline-flex;
    color: var(--mf-muted);
  }
  .mf-fact dd {
    margin: 0;
    min-width: 0;
  }

  .mf-people {
    flex: none;
    display: flex;
    flex-direction: column;
    padding-top: 8px;
  }
  .mf-facts + .mf-people {
    border-top: 1px solid var(--color-base-300);
  }
  .mf-heading {
    flex: none;
    margin-bottom: 6px;
    padding-inline: 12px;
    font-size: 12px;
    font-weight: 600;
    color: var(--mf-muted);
  }
  .mf-people ul {
    display: grid;
    gap: 6px;
    min-height: 0;
    max-height: 220px;
    padding: 0 12px 10px;
    overflow-y: auto;
    overscroll-behavior: contain;
  }
  .mf-people li {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    color: var(--mf-strong);
  }
  .mf-avatar {
    flex: none;
    display: inline-grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border-radius: 999px;
    background-color: var(--mf-tint);
    font-size: 10px;
    font-weight: 600;
    color: var(--mf-strong);
  }

  .mf-about {
    flex: none;
    border-top: 1px solid var(--color-base-300);
  }
  .mf-about-toggle {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 8px 12px;
    border: 0;
    background: none;
    font: inherit;
    font-size: 12px;
    font-weight: 600;
    text-align: left;
    color: var(--mf-muted);
    cursor: pointer;
    transition: color 150ms ease;
  }
  .mf-about-toggle:hover {
    color: var(--mf-strong);
  }
  .mf-about-toggle:focus-visible {
    outline: 2px solid var(--color-primary);
    outline-offset: -2px;
  }
  .mf-about :global(.mf-about-caret) {
    flex: none;
  }
  .mf-about-label {
    flex: 1;
  }
  .mf-about :global(.mf-about-caret) {
    transition: rotate 180ms ease;
  }
  .mf-about.open :global(.mf-about-caret) {
    rotate: 90deg;
  }
  .mf-about-rows {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: 6px 12px;
    margin: 0;
    padding: 0 12px 10px;
  }
  .mf-about-rows dt {
    font-size: 12px;
    line-height: 20px;
    color: var(--mf-muted);
  }
  .mf-about-rows dd {
    margin: 0;
    min-width: 0;
    overflow-wrap: anywhere;
    line-height: 20px;
    color: var(--mf-strong);
  }
  .mf-id {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .mf-id code {
    font-size: 12px;
  }
  .mf-copy {
    display: inline-grid;
    flex: none;
    place-items: center;
    width: 20px;
    height: 20px;
    border: 0;
    border-radius: 5px;
    background: none;
    color: var(--mf-muted);
    cursor: pointer;
  }
  .mf-copy:hover {
    background-color: var(--mf-tint);
    color: var(--mf-strong);
  }
</style>
