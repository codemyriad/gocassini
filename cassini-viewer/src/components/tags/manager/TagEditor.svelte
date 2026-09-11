<script lang="ts">
  import { createEventDispatcher, onMount } from "svelte";
  import { Plus } from "@lucide/svelte";

  import type { TagUpdate, VocabularyTag } from "../../../viewer/annotations";
  import { confirmLine, tagUpdate } from "../../../viewer/tagManager";
  import { colorFor, colorName, type TagColorId, type TagIconId } from "../../../viewer/tagPalette";
  import ColorSwatchPicker from "../ColorSwatchPicker.svelte";
  import TagIcon from "../TagIcon.svelte";
  import IconGrid from "./IconGrid.svelte";

  export let tag: VocabularyTag;
  // The tag that already has the typed name, once the server has said so.
  export let conflict: { tagId: string; label: string } | null = null;
  export let choosing: "color" | "icon" | null = null;

  const dispatch = createEventDispatcher<{ save: TagUpdate; cancel: void; merge: string }>();
  let label = tag.label;
  let color: TagColorId = colorFor(tag);
  let icon: TagIconId | "" = tag.icon;
  let swatch: HTMLButtonElement;
  let iconButton: HTMLButtonElement;
  let input: HTMLInputElement;

  $: renamed = label.trim() !== "" && label.trim() !== tag.label;

  onMount(() => {
    if (choosing) return;
    input.focus();
    input.select();
  });

  function save() {
    const update = tagUpdate(tag, { label, color, icon });
    if (update) dispatch("save", update);
    else dispatch("cancel");
  }

  function onKeydown(event: KeyboardEvent) {
    if (event.key !== "Enter" && event.key !== "Escape") return;
    event.preventDefault();
    if (event.key === "Enter") save();
    else dispatch("cancel");
  }

  const toggle = (which: "color" | "icon") => (choosing = choosing === which ? null : which);
</script>

<div class="flex flex-wrap items-center gap-1.5" data-tag-color={color}>
  <button bind:this={swatch} type="button" class="btn btn-square btn-ghost btn-sm border-base-300" title="Colour"
    aria-label={`Colour: ${colorName(color)}`} aria-haspopup="true" aria-expanded={choosing === "color"} on:click={() => toggle("color")}>
    <span class="size-3.5 rounded-full bg-(--tag)" aria-hidden="true"></span>
  </button>
  <button bind:this={iconButton} type="button" class="btn btn-square btn-ghost btn-sm border-base-300 text-(--tag)" class:border-dashed={!icon}
    title="Icon" aria-label={icon ? `Icon: ${icon}` : "Add an icon"} aria-haspopup="true" aria-expanded={choosing === "icon"} on:click={() => toggle("icon")}>
    {#if icon}<TagIcon {icon} size={15} />{:else}<Plus size={14} class="text-base-content/50" />{/if}
  </button>
  <input bind:this={input} bind:value={label} type="text" maxlength="64" autocomplete="off" spellcheck="false" aria-label="Tag name"
    class="input input-sm min-w-0 flex-[1_1_140px]" on:input={() => (conflict = null)} on:keydown={onKeydown} />
  <button type="button" class="btn btn-ghost btn-sm" on:click={() => dispatch("cancel")}>Cancel</button>
  <button type="button" class="btn btn-neutral btn-sm" disabled={!label.trim()} on:click={save}>Save</button>
  {#if conflict}
    {@const other = conflict}
    <p class="basis-full text-xs">
      “{other.label}” already exists.
      <button type="button" class="link font-medium" on:click={() => dispatch("merge", other.tagId)}>Merge into “{other.label}” instead</button>
    </p>
  {/if}
  {#if renamed}<p class="basis-full text-xs text-base-content/65">{confirmLine(tag.meetings)}</p>{/if}
  {#if choosing === "color"}
    <ColorSwatchPicker value={color} anchor={swatch} label={`Colour for “${tag.label}”`} on:close={() => (choosing = null)}
      on:select={(event) => ((color = event.detail), (choosing = null), swatch.focus())} />
  {:else if choosing === "icon"}
    <IconGrid value={icon} {color} anchor={iconButton} on:close={() => (choosing = null)}
      on:select={(event) => ((icon = event.detail), (choosing = null), iconButton.focus())} />
  {/if}
</div>
