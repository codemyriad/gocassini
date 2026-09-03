<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import {
    formatInsightCreated,
    formatInsightStatus,
    insightHeadline,
    type InsightRecord,
  } from "../viewer/insights";

  // An insight in the browse list (D-721). A card, not a row: same list,
  // visibly a different kind of thing — which is the prototype's own rule, and
  // the reason insights are not in a library of their own. An insight belongs
  // beside the conversations it summarises.
  //
  // Presentational. The shell owns which insights exist, which one is open, and
  // which of an insight's sources this caller may see.

  export let insight: InsightRecord;
  // How many of the run's source meetings this caller can actually read,
  // resolved against their own catalog by the shell. It is not
  // insight.meetingIds.length: a source they may not read is absent, and a
  // count is a disclosure that it existed.
  export let sourceCount = 0;
  // Whether this insight is the one the sheet is holding.
  export let selected = false;

  const dispatch = createEventDispatcher<{ open: void }>();

  $: headline = insightHeadline(insight);
  // A finished run says what it is by having a document to open, so the badge
  // is for the three states where the card is not yet — or never will be — a
  // readable answer. Without it a queued run is a card with nothing behind it,
  // and a failed one is indistinguishable from a good one.
  $: pending = insight.status !== "succeeded";
</script>

<div class="insight-row">
  <button
    type="button"
    class="insight-card"
    aria-current={selected ? "page" : undefined}
    on:click={() => dispatch("open")}
  >
    <span class="insight-eyebrow">
      <span class="insight-kind">Insight</span>
      {#if pending}
        <span class="insight-status" data-status={insight.status}>
          {formatInsightStatus(insight.status)}
        </span>
      {/if}
    </span>
    <span class="insight-title">{headline}</span>
    <span class="insight-meta">
      <span>{formatInsightCreated(insight)}</span>
      <!-- Nothing at all when no source is readable: an insight whose meetings
           this caller cannot see does not report how many there were. -->
      {#if sourceCount > 0}
        <span class="dot" aria-hidden="true"></span>
        <span>{sourceCount} {sourceCount === 1 ? "meeting" : "meetings"}</span>
      {/if}
    </span>
  </button>
</div>

<style>
  /* Inset from the list's full-bleed rows, so the card reads as an object
     sitting in the stream rather than another row of it. */
  .insight-row {
    padding: 6px 20px;
  }

  /* Secondary, not primary: the open row and the active-narrowing chips are
     already primary, and an insight has to read as a different KIND of thing
     rather than as a selected meeting. Secondary is this theme's amber — the
     colour the prototype gives insights — and, unlike primary, it is not
     remapped to the Nextcloud accent in the embedded build, so the distinction
     survives whatever the instance is themed. */
  .insight-card {
    display: flex;
    flex-direction: column;
    gap: 3px;
    width: 100%;
    padding: 10px 12px;
    text-align: left;
    cursor: pointer;
    background-color: var(--color-base-100);
    border: 1px solid var(--color-base-300);
    border-left: 3px solid var(--color-secondary);
    border-radius: var(--radius-field, 0.5rem);
    color: var(--color-base-content);
  }
  .insight-card:hover {
    background-color: var(--color-base-200);
  }
  .insight-card[aria-current="page"] {
    background-color: color-mix(in oklch, var(--color-secondary) 15%, transparent);
    border-color: color-mix(in oklch, var(--color-secondary) 45%, transparent);
    border-left-color: var(--color-secondary);
  }

  .insight-eyebrow {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    min-width: 0;
  }
  .insight-kind {
    font-size: 10px;
    font-weight: 650;
    line-height: 1;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: color-mix(in oklch, var(--color-secondary) 80%, var(--color-base-content));
  }

  .insight-status {
    padding: 2px 7px;
    border-radius: 20px;
    font-size: 0.6875rem;
    font-weight: 550;
    line-height: 1.3;
    background-color: color-mix(in oklch, var(--color-base-content) 10%, transparent);
    color: color-mix(in oklch, var(--color-base-content) 75%, transparent);
  }
  /* A run that failed is not a quieter version of one that worked. */
  .insight-status[data-status="failed"] {
    background-color: color-mix(in oklch, var(--color-error) 22%, transparent);
    color: var(--color-base-content);
  }

  .insight-title {
    font-weight: 550;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .insight-meta {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    font-size: 0.75rem;
    font-variant-numeric: tabular-nums;
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  .insight-meta .dot {
    flex: none;
    width: 3px;
    height: 3px;
    border-radius: 50%;
    background-color: currentColor;
  }
</style>
