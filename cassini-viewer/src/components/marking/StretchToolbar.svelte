<script lang="ts">
  import { createEventDispatcher } from "svelte";

  import { formatPreciseTime } from "../../core/marking";
  import type { TagPick, VocabularyTag } from "../../viewer/annotations";
  import TagChip from "../tags/TagChip.svelte";
  import TagIcon from "../tags/TagIcon.svelte";
  import TagPicker from "../tags/TagPicker.svelte";
  import ActionButton from "../ui/ActionButton.svelte";
  import { pickColor, type PlacedMark } from "./session";

  export let startMs: number;
  export let endMs: number;
  // The mark being moved, when the stretch is one already made.
  export let mark: PlacedMark | null = null;
  export let moved = false;
  // The tag last used here, to put on this section in one click.
  export let recent: TagPick | null = null;
  export let vocabulary: readonly VocabularyTag[] = [];
  export let busy = false;
  // Why the last write from here failed.
  export let error = "";
  // One line in the dock above the player rather than a card beside the text,
  // where the screen is too narrow for a column; no key hints there, where
  // there are no keys.
  export let row = false;

  const dispatch = createEventDispatcher<{ tag: TagPick; save: void; remove: void; clear: void }>();
  let tagButton: HTMLButtonElement | undefined;
  let picking = false;

  // Enter: nothing is ever saved on release, only here.
  export function confirm() {
    if (busy) {
      return;
    }
    if (mark) {
      if (moved) dispatch("save");
    } else {
      picking = true;
    }
  }
</script>

{#snippet times(inset: string)}
  <p class="font-mono tabular-nums whitespace-nowrap text-base-content/70 {inset}">
    <b class="text-base-content">{formatPreciseTime(startMs)}</b> → <b class="text-base-content">{formatPreciseTime(endMs)}</b>
  </p>
{/snippet}

{#snippet unsaved()}
  <p class="flex items-center gap-1.5 font-semibold whitespace-nowrap text-warning">
    <span class="size-1.5 rounded-full bg-current" aria-hidden="true"></span>Unsaved changes
  </p>
{/snippet}

<!-- The tag keeps the place it had as a chip, and the card is built around it:
     the same chip in the same spot, then who put it there, the times, and the
     two actions on one line. Every button is one size, its label at its start
     and its key at its end; in a bar, where there are no keys, none. -->
<!-- As a card, its corners follow the buttons' inside it: their radius, and
     the 6px and the border between them and the edge. As a row it sits in the
     dock's own card. -->
<div
  class="text-xs {row
    ? 'flex flex-wrap items-center gap-x-3 gap-y-1.5 px-1'
    : 'st-card grid w-52 gap-1.5 border border-base-300 bg-base-100 p-1.5 shadow-md'}"
  style:border-radius={row ? undefined : "calc(var(--radius-field, 0.5rem) + 7px)"}
  role="toolbar"
  aria-label="This section"
>
  {#if mark}
    <!-- No tag chip in the card: the section's own chip sits directly above it
         and never moves, so repeating it named the same thing twice. In a bar
         there is no chip beside it, so the line starts with the tag. -->
    {#if row}
      <TagChip label={mark.tag.label} color={mark.color} icon={mark.icon} />
      <p class="min-w-0 max-w-[9rem] truncate text-base-content/60">{mark.item.actor.id}</p>
      {@render times("")}
      {#if moved}{@render unsaved()}{/if}
    {:else}
      <div class="grid gap-0.5 px-0.5">
        <!-- Who made it, and across from them whether it has changed since. -->
        <div class="flex items-center justify-between gap-2">
          <p class="min-w-0 truncate text-base-content/60">{mark.item.actor.id}</p>
          {#if moved}{@render unsaved()}{/if}
        </div>
        {@render times("")}
      </div>
    {/if}
    <!-- Once an edge has moved, saving is the thing to do: it takes the
         strongest button, the one "Tag selection" has, above the rest. -->
    <div class={row ? "ml-auto flex flex-wrap items-center justify-end gap-1.5" : "grid gap-1.5"}>
      {#if moved}
        <ActionButton tone="ink" block={!row} key={row ? "" : "↵"} disabled={busy} on:click={confirm}>Save changes</ActionButton>
      {/if}
      <!-- Equal columns, so Remove is not the larger for its longer word, and
           tighter buttons, so "Cancel" and its key fit half the card. -->
      <div class="grid grid-flow-col auto-cols-fr gap-1">
        <ActionButton tone="error" compact disabled={busy} on:click={() => dispatch("remove")}>Remove</ActionButton>
        <ActionButton tone="plain" compact key={row ? "" : "Esc"} on:click={() => dispatch("clear")}>{moved ? "Cancel" : "Done"}</ActionButton>
      </div>
    </div>
  {:else}
    {@render times(row ? "" : "px-0.5")}
    <div class={row ? "ml-auto flex flex-wrap items-center justify-end gap-1.5" : "grid gap-1.5"}>
      <ActionButton bind:element={tagButton} tone="ink" block={!row} key={row ? "" : "↵"} disabled={busy} aria-haspopup="dialog" aria-expanded={picking} on:click={confirm}>
        Tag selection
      </ActionButton>
      <!-- The last tag used, once more: its own colour, so it reads as that
           tag rather than as a second way to pick one. -->
      {#if recent}
        <ActionButton
          tone="tag"
          block={!row}
          data-tag-color={pickColor(recent, vocabulary)}
          disabled={busy}
          title={`Tag as ${recent.label}`}
          aria-label={`Tag as ${recent.label}`}
          on:click={() => recent && dispatch("tag", recent)}
        >
          <TagIcon size={8} /><span class="truncate">{recent.label}</span>
        </ActionButton>
      {/if}
      <ActionButton tone="plain" block={!row} key={row ? "" : "Esc"} on:click={() => dispatch("clear")}>Clear</ActionButton>
    </div>
  {/if}
  {#if error}
    <p class="text-error {row ? 'basis-full' : 'px-0.5'}" role="alert">{error}</p>
  {/if}
</div>
{#if picking}
  <TagPicker
    tags={vocabulary}
    label="Tag selection"
    anchor={tagButton ?? null}
    on:pick={(event) => ((picking = false), dispatch("tag", event.detail))}
    on:close={() => (picking = false)}
  />
{/if}
