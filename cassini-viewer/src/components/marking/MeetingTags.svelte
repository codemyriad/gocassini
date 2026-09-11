<script lang="ts">
  import { Plus, TriangleAlert } from "@lucide/svelte";

  import type { VocabularyTag } from "../../viewer/annotations";
  import TagChip from "../tags/TagChip.svelte";
  import TagPicker from "../tags/TagPicker.svelte";
  import { markRequest, removeRequest, untagMeetingRequest, viewMarks, type MarksSession } from "./session";

  // The meeting header's tags: on the whole meeting, and what went wrong with any.
  export let session: MarksSession;
  export let vocabulary: readonly VocabularyTag[] = [];

  let adding = false;
  let addButton: HTMLButtonElement;

  $: view = viewMarks($session, vocabulary);
  $: lost = view.lost.length;
</script>

{#if $session.status !== "off" && $session.status !== "loading"}
  <div class="mt-2 flex flex-wrap items-center gap-1.5 text-xs" role="group" aria-label="Tags on the whole meeting">
    {#if $session.status === "ready"}
      {#each view.whole as look (look.tag.id)}
        <TagChip
          label={look.tag.label}
          color={look.color}
          icon={look.icon}
          variant="whole"
          removable={!$session.busy}
          on:remove={() => session.write(untagMeetingRequest(look.tag.id))}
        />
      {/each}
      <button
        bind:this={addButton}
        type="button"
        class="btn btn-ghost btn-xs h-auto min-h-0 gap-1 border border-dashed border-base-content/30 px-2 py-0.5 font-medium"
        aria-haspopup="dialog"
        aria-expanded={adding}
        disabled={$session.busy}
        on:click={() => (adding = !adding)}
      >
        <Plus size={12} aria-hidden="true" />Add tag
      </button>
      {#if lost > 0}
        <span class="inline-flex items-center gap-1.5 rounded-field bg-warning/15 px-2 py-0.5">
          <TriangleAlert size={12} class="text-warning" aria-hidden="true" />
          {lost === 1 ? "1 mark" : `${lost} marks`} can't be placed on this recording
          <button type="button" class="link" disabled={$session.busy} on:click={() => session.write(removeRequest(view.lost.map((item) => item.id)))}>
            Remove {lost === 1 ? "it" : "them"}
          </button>
        </span>
      {/if}
      {#if $session.error}
        <span class="text-error" role="alert">{$session.error}</span>
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
    on:pick={(event) => ((adding = false), session.write(markRequest(event.detail, { kind: "meeting" })))}
    on:close={() => (adding = false)}
  />
{/if}
