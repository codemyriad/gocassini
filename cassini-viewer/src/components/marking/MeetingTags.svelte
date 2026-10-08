<script lang="ts">
  import { Plus, Trash2, TriangleAlert } from "@lucide/svelte";

  import {
    WHOLE_MEETING,
    markRequest,
    plural,
    removeRequest,
    untagMeetingRequest,
    type MeetingTag,
    type VocabularyTag,
  } from "../../viewer/annotations";
  import { colorFor } from "../../viewer/tagPalette";
  import TagChip from "../tags/TagChip.svelte";
  import TagPicker from "../tags/TagPicker.svelte";
  import { popover } from "../tags/popover";
  import { viewMarks, type MarksSession } from "./session";

  // The meeting header's tags: on the whole meeting, and what went wrong with any.
  export let session: MarksSession;
  export let vocabulary: readonly VocabularyTag[] = [];
  export let preview: readonly MeetingTag[] = [];

  let adding = false;
  let addButton: HTMLButtonElement;
  let openTagId: string | null = null;
  let tagAnchor: HTMLButtonElement | null = null;

  $: openLook = view.whole.find((look) => look.tag.id === openTagId) ?? null;

  function toggleTag(id: string, event: MouseEvent) {
    tagAnchor = event.currentTarget as HTMLButtonElement;
    openTagId = openTagId === id ? null : id;
  }

  function removeOpenTag() {
    if (!openLook) return;
    const id = openLook.tag.id;
    openTagId = null;
    session.write(untagMeetingRequest(id));
  }

  const focusFirst = (node: HTMLElement) => {
    node.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus();
  };

  $: view = viewMarks($session, vocabulary);
  $: lost = view.lost.length;
</script>

{#if $session.status !== "off"}
  <div class="flex flex-wrap items-center gap-1.5 text-xs" role="group" aria-label="Tags on the whole meeting" aria-busy={$session.status === "loading"}>
    {#if $session.status === "loading"}
      {#each preview.filter((entry) => entry.whole) as { tag } (tag.tagId)}
        <TagChip label={tag.label} color={colorFor(tag)} icon={tag.icon} />
      {/each}
      {#if $session.editable}
        <button type="button" class="add-tag" disabled>
          <Plus size={11} aria-hidden="true" />Add tag
        </button>
      {/if}
    {:else if $session.status === "ready"}
      {#each view.whole as look (look.tag.id)}
        {#if $session.editable}
          <button
            type="button"
            class="meeting-tag"
            aria-haspopup="menu"
            aria-expanded={openTagId === look.tag.id}
            aria-label={`${look.tag.label} tag options`}
            on:click={(event) => toggleTag(look.tag.id, event)}
          >
            <TagChip label={look.tag.label} color={look.color} icon={look.icon} />
          </button>
        {:else}
          <TagChip label={look.tag.label} color={look.color} icon={look.icon} />
        {/if}
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
{#if openLook && tagAnchor}
  <div
    use:popover={{ anchor: tagAnchor, close: () => (openTagId = null) }}
    use:focusFirst
    role="menu"
    tabindex="-1"
    aria-label={`${openLook.tag.label} tag options`}
    class="tag-popover grid p-1"
  >
    <button type="button" role="menuitem" class="mt-item mt-remove" disabled={$session.busy} aria-label={`Remove ${openLook.tag.label}`} on:click={removeOpenTag}>
      <Trash2 size={12} aria-hidden="true" />Remove
      <TagChip label={openLook.tag.label} color={openLook.color} icon={openLook.icon} />
    </button>
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
  .meeting-tag {
    display: inline-flex;
    flex: none;
    padding: 0;
    border: 0;
    border-radius: 5px;
    background: none;
    cursor: pointer;
    transition: filter 150ms ease;
  }
  .meeting-tag:hover,
  .meeting-tag[aria-expanded="true"] {
    filter: brightness(1.2);
  }
  .meeting-tag:focus-visible {
    outline: 2px solid var(--color-primary);
    outline-offset: 2px;
  }
  .mt-item {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 4px 6px;
    border-radius: var(--radius-field, 0.25rem);
    font-size: 12px;
    font-weight: 550;
    text-align: left;
    white-space: nowrap;
    cursor: pointer;
  }
  .mt-remove {
    color: var(--color-error);
  }
  .mt-item:hover:not(:disabled),
  .mt-item:focus-visible {
    background-color: color-mix(in oklch, var(--color-error) 12%, transparent);
    outline: none;
  }
  .mt-item:disabled {
    opacity: 0.4;
    cursor: default;
  }
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
