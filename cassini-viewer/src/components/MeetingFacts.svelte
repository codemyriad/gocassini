<script lang="ts">
  import { tick } from "svelte";
  import { Calendar, Clock, MessageSquare, Users } from "@lucide/svelte";

  import { formatClockTime } from "../core/transcript";
  import { formatMeetingDateShort, formatMeetingDateWithDay, hasMeetingDate } from "../viewer/catalog";
  import { popover } from "./tags/popover";

  export let dateLabel = "";
  export let room: string | null = null;
  export let durationMs = 0;
  export let speakerNames: string[] = [];

  let open = false;
  let anchor: HTMLButtonElement;
  let panel: HTMLDivElement;

  $: dated = hasMeetingDate(dateLabel);
  $: hasAny = dated || Boolean(room) || durationMs > 0 || speakerNames.length > 0;

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
              <dd class="font-medium text-base-content">{formatMeetingDateWithDay(dateLabel)}</dd>
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
    max-height: min(420px, calc(100vh - 16px));
    overflow: hidden;
    font-size: 13px;
    color: color-mix(in oklch, var(--color-base-content) 75%, transparent);
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
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  .mf-fact dd {
    margin: 0;
    min-width: 0;
  }

  .mf-people {
    display: flex;
    flex-direction: column;
    min-height: 0;
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
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
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
    color: var(--color-base-content);
  }
  .mf-avatar {
    flex: none;
    display: inline-grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border-radius: 999px;
    background-color: color-mix(in oklch, var(--color-base-content) 12%, transparent);
    font-size: 10px;
    font-weight: 600;
    color: color-mix(in oklch, var(--color-base-content) 75%, transparent);
  }
</style>
