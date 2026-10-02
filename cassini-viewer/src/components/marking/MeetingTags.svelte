<script lang="ts">
  import { Plus, TriangleAlert } from "@lucide/svelte";

  import {
    WHOLE_MEETING,
    markRequest,
    plural,
    removeRequest,
    untagMeetingRequest,
    type VocabularyTag,
  } from "../../viewer/annotations";
  import TagChip from "../tags/TagChip.svelte";
  import TagPicker from "../tags/TagPicker.svelte";
  import { viewMarks, type MarksSession } from "./session";

  // The meeting header's tags: on the whole meeting, and what went wrong with any.
  export let session: MarksSession;
  export let vocabulary: readonly VocabularyTag[] = [];

  let adding = false;
  let addButton: HTMLButtonElement;

  $: view = viewMarks($session, vocabulary);
  $: lost = view.lost.length;
</script>

{#if $session.status !== "off" && $session.status !== "loading"}
  <div class="flex flex-wrap items-center gap-1.5 text-xs" role="group" aria-label="Tags on the whole meeting">
    {#if $session.status === "ready"}
      {#each view.whole as look (look.tag.id)}
        <TagChip
          label={look.tag.label}
          color={look.color}
          icon={look.icon}
          removable={$session.editable && !$session.busy}
          on:remove={() => session.write(untagMeetingRequest(look.tag.id))}
        />
      {/each}
      {#if $session.editable}
        <button
          bind:this={addButton}
          type="button"
          class="add-tag"
          aria-haspopup="dialog"
          aria-expanded={adding}
          disabled={$session.busy}
          on:click={() => (adding = !adding)}
        >
          <Plus size={11} aria-hidden="true" />Add tag
        </button>
      {/if}
      {#if lost > 0}
        <span class="inline-flex items-center gap-1.5 rounded-field bg-warning/15 px-2 py-0.5">
          <TriangleAlert size={12} class="text-warning" aria-hidden="true" />
          {plural(lost, "mark")} can't be placed on this recording
          {#if $session.editable}
            <button type="button" class="link" disabled={$session.busy} on:click={() => session.write(removeRequest(view.lost.map((item) => item.id)))}>
              Remove {lost === 1 ? "it" : "them"}
            </button>
          {/if}
        </span>
      {/if}
      {#if $session.saving}
        <span class="text-base-content/60" role="status">{$session.retryable ? "Annotation updates waiting to retry" : "Saving annotations…"}</span>
      {:else if $session.annotations?.sync && $session.annotations.sync.state !== "saved"}
        <span class="text-base-content/60" role="status">
          {#if $session.annotations.sync.state === "pending"}
            Tags saved · updating recording…
          {:else if $session.annotations.sync.state === "delayed"}
            Tags saved · recording update delayed
          {:else}
            Tags saved · recording needs attention
          {/if}
        </span>
        {#if $session.annotations.sync.state === "delayed" || $session.annotations.sync.state === "blocked"}
          <span class="text-base-content/60">{$session.annotations.sync.error}</span>
          <button type="button" class="link" disabled={$session.busy} on:click={() => session.retrySync()}>Retry recording update</button>
        {/if}
      {/if}
      {#if $session.error}
        <span class="text-error" role="alert">{$session.error}</span>
        {#if $session.retryable}
          <button type="button" class="link" on:click={() => session.retryWrite()}>Retry saving annotations</button>
        {:else}
          <button type="button" class="link" on:click={() => session.dismissError()}>Dismiss</button>
        {/if}
      {/if}
    {:else if $session.status === "preparing"}
      <span class="text-base-content/60">Tags are being prepared…</span>
    {:else}
      <span class="text-base-content/60">Tags aren't available right now: {$session.error}</span>
    {/if}
  </div>
{/if}
{#if adding}
  <TagPicker
    tags={vocabulary}
    label="Tag the whole meeting"
    anchor={addButton}
    on:pick={(event) => ((adding = false), session.write(markRequest(event.detail, WHOLE_MEETING)))}
    on:close={() => (adding = false)}
  />
{/if}

<style>
  .add-tag {
    display: inline-flex;
    flex: none;
    align-items: center;
    gap: 3px;
    box-sizing: border-box;
    height: 18px;
    padding: 0 5px;
    font-size: 11.5px;
    font-weight: 550;
    line-height: 1;
    white-space: nowrap;
    color: color-mix(in oklch, var(--color-base-content) 75%, var(--color-base-200));
    background: none;
    border: 1px dashed color-mix(in oklch, var(--color-base-content) 30%, var(--color-base-200));
    border-radius: 5px;
    cursor: pointer;
  }
  .add-tag:hover:not(:disabled),
  .add-tag[aria-expanded="true"] {
    color: var(--color-base-content);
    background-color: color-mix(in oklch, var(--color-base-content) 8%, transparent);
  }
  .add-tag:focus-visible {
    outline: 2px solid var(--color-primary);
    outline-offset: 2px;
  }
  .add-tag:disabled {
    opacity: 0.5;
    cursor: default;
  }
</style>
