<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { ChevronDown, Copy, FileAudio, FileText, NotebookText } from "@lucide/svelte";

  import { popover, stepIndex } from "./tags/popover";

  export let canCopy = true;
  export let canDownloadTranscript = true;
  export let canDownloadAudio = true;
  export let audioLabel = "Download audio";
  export let status = "";
  export let includeContext = false;
  export let canExportContext = true;
  export let plural = false;
  export let label = "Export this meeting";
  // Labels for the two items every caller has, for one whose thing is not a
  // transcript: the insight drawer exports markdown. Empty keeps the transcript
  // wording, singular or plural.
  export let copyLabel = "";
  export let transcriptLabel = "";
  // The audio item is a meeting's; an insight has no audio to offer.
  export let includeAudio = true;
  // The summary item is a meeting's, and only where its notes can be read:
  // callers that have no summary leave it out entirely.
  export let includeSummary = false;
  export let canDownloadSummary = true;
  export let summaryLabel = "Download summary";

  const dispatch = createEventDispatcher<{ copy: void; transcript: void; audio: void; summary: void; copyContext: void; context: void }>();

  let open = false;
  let anchor: HTMLButtonElement;
  let menuEl: HTMLDivElement;

  const focusFirst = (node: HTMLElement) => {
    node.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus();
  };

  function choose(action: "copy" | "transcript" | "audio" | "summary" | "copyContext" | "context") {
    open = false;
    anchor?.focus();
    dispatch(action);
  }

  function onKeydown(event: KeyboardEvent) {
    const items = [...menuEl.querySelectorAll<HTMLButtonElement>("button:not(:disabled)")];
    const next = stepIndex(items.indexOf(event.target as HTMLButtonElement), event.key, items.length);
    if (next !== null) {
      event.preventDefault();
      items[next].focus();
    }
  }
</script>

{#if status}
  <span class="hidden min-[721px]:inline truncate text-xs text-base-content/70" role="status">{status}</span>
{/if}
<button
  bind:this={anchor}
  type="button"
  class="btn btn-sm btn-quiet gap-1.5"
  aria-haspopup="menu"
  aria-expanded={open}
  title={status || undefined}
  on:click={() => (open = !open)}
>
  Export
  <ChevronDown size={12} aria-hidden="true" />
</button>

{#if open}
  <div
    bind:this={menuEl}
    use:popover={{ anchor, close: () => (open = false) }}
    use:focusFirst
    role="menu"
    tabindex="-1"
    aria-label={label}
    class="tag-popover grid w-52 p-1"
    on:keydown={onKeydown}
  >
    {#if includeContext}
      <button type="button" role="menuitem" class="em-item" disabled={!canExportContext} on:click={() => choose("copyContext")}>
        <Copy size={14} aria-hidden="true" />Copy full context
      </button>
      <button type="button" role="menuitem" class="em-item" disabled={!canExportContext} on:click={() => choose("context")}>
        <FileText size={14} aria-hidden="true" />Download full context
      </button>
    {/if}
    <button type="button" role="menuitem" class="em-item" disabled={!canCopy} on:click={() => choose("copy")}>
      <Copy size={14} aria-hidden="true" />{copyLabel || `Copy ${plural ? "transcripts" : "transcript"}`}
    </button>
    <button type="button" role="menuitem" class="em-item" disabled={!canDownloadTranscript} on:click={() => choose("transcript")}>
      <FileText size={14} aria-hidden="true" />{transcriptLabel || `Download ${plural ? "transcripts" : "transcript"}`}
    </button>
    {#if includeSummary}
      <button type="button" role="menuitem" class="em-item" disabled={!canDownloadSummary} on:click={() => choose("summary")}>
        <NotebookText size={14} aria-hidden="true" />{summaryLabel}
      </button>
    {/if}
    {#if includeAudio}
      <button type="button" role="menuitem" class="em-item" disabled={!canDownloadAudio} on:click={() => choose("audio")}>
        <FileAudio size={14} aria-hidden="true" />{audioLabel}
      </button>
    {/if}
  </div>
{/if}

<style>
  .em-item {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
    border-radius: var(--radius-field, 0.25rem);
    font-size: 13px;
    text-align: left;
    color: var(--color-base-content);
    cursor: pointer;
  }
  .em-item :global(svg) {
    flex: none;
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }
  .em-item:hover:not(:disabled),
  .em-item:focus-visible {
    background-color: color-mix(in oklch, var(--color-base-content) 8%, transparent);
    outline: none;
  }
  .em-item:disabled {
    opacity: 0.4;
    cursor: default;
  }
</style>
