<script lang="ts">
  import { createEventDispatcher, onMount } from "svelte";
  import { Check, Minus } from "@lucide/svelte";

  import { findByLabel, matchTags, plural, type TagPick, type VocabularyTag } from "../../viewer/annotations";
  import { colorFor, leastUsedColor, styleName, type TagColorId } from "../../viewer/tagPalette";
  import ColorSwatchPicker from "./ColorSwatchPicker.svelte";
  import TagIcon from "./TagIcon.svelte";
  import { popover, stepIndex } from "./popover";

  export let tags: readonly VocabularyTag[] = [];
  export let label = "Tag";
  export let query = "";
  // Tick boxes, for toggling tags on whole meetings. `mixed` is on some of several.
  export let multiple = false;
  export let selected: readonly string[] = [];
  export let mixed: readonly string[] = [];
  // Focus returns here on Esc, and a click on it does not count as outside.
  export let anchor: HTMLElement | null = null;
  // Off to choose among existing tags only, as a merge target does.
  export let creatable = true;

  const dispatch = createEventDispatcher<{ pick: TagPick; close: void }>();
  const uid = `tag-picker-${Math.random().toString(36).slice(2, 8)}`;
  let root: HTMLElement;
  let input: HTMLInputElement;
  let swatchButton: HTMLButtonElement;
  let active = 0;
  let chosenColor: TagColorId | null = null;
  let choosingColor = false;

  $: matches = matchTags(tags, query);
  $: draft = query.trim();
  $: canCreate = creatable && draft !== "" && !findByLabel(tags, draft);
  $: count = matches.length + (canCreate ? 1 : 0);
  $: if (active >= count) active = Math.max(0, count - 1);
  $: newColor = chosenColor ?? leastUsedColor(tags);

  onMount(() => input.focus());

  function pick(index: number) {
    const tag = matches[index];
    if (tag) {
      dispatch("pick", { tagId: tag.tagId, label: tag.label });
    } else if (canCreate) {
      dispatch("pick", { label: draft, color: newColor, icon: "" });
    } else {
      return;
    }
    query = "";
    chosenColor = null;
    choosingColor = false;
  }

  function onKeydown(event: KeyboardEvent) {
    if (event.key === "Enter") {
      event.preventDefault();
      pick(active);
      return;
    }
    const next = stepIndex(active, event.key, count);
    if (next !== null) {
      event.preventDefault();
      active = next;
      root.querySelector(`#${uid}-${next}`)?.scrollIntoView({ block: "nearest" });
    }
  }

  function chooseColor(event: CustomEvent<TagColorId>) {
    chosenColor = event.detail;
    choosingColor = false;
    input.focus();
  }

  const keepFocus = (event: Event) => event.preventDefault();
</script>

<div
  bind:this={root}
  use:popover={{ anchor, close: () => dispatch("close") }}
  class="tag-popover tag-picker"
  role="dialog"
  aria-label={label}
>
  <input
    bind:this={input}
    bind:value={query}
    type="text"
    maxlength="64"
    autocomplete="off"
    placeholder={creatable ? "Find or create a tag" : "Find a tag"}
    aria-label={creatable ? "Find or create a tag" : "Find a tag"}
    role="combobox"
    aria-expanded="true"
    aria-controls={`${uid}-list`}
    aria-autocomplete="list"
    aria-activedescendant={count > 0 ? `${uid}-${active}` : undefined}
    on:input={() => (active = 0)}
    on:keydown={onKeydown}
  />
  <ul
    id={`${uid}-list`}
    role="listbox"
    aria-label="Tags"
    aria-multiselectable={multiple || undefined}
    aria-owns={canCreate ? `${uid}-${matches.length}` : undefined}
  >
    {#each matches as tag, index (tag.tagId)}
      {@const on = selected.includes(tag.tagId)}
      <!-- Options take no focus: the field keeps it and handles the keys (aria-activedescendant). -->
      <!-- svelte-ignore a11y_click_events_have_key_events a11y_interactive_supports_focus -->
      <li
        id={`${uid}-${index}`}
        role="option"
        aria-selected={multiple ? on : index === active}
        class:active={index === active}
        data-tag-color={colorFor(tag)}
        on:pointerdown={keepFocus}
        on:click={() => pick(index)}
      >
        {#if multiple}
          <span class="tick" class:on class:mixed={!on && mixed.includes(tag.tagId)} aria-hidden="true">
            {#if on}<Check size={12} strokeWidth={3} />{:else if mixed.includes(tag.tagId)}<Minus
                size={12}
                strokeWidth={3}
              />{/if}
          </span>
        {/if}
        <span class="mark"><TagIcon icon={tag.icon} /></span>
        <span class="name">{tag.label}</span>
        <span class="uses">{plural(tag.meetings, "meeting")}</span>
      </li>
    {/each}
  </ul>
  {#if canCreate}
    <!-- The swatch is a real button, so it sits beside the option rather than inside it. -->
    <div class="create">
      <!-- svelte-ignore a11y_click_events_have_key_events a11y_interactive_supports_focus -->
      <div
        id={`${uid}-${matches.length}`}
        role="option"
        aria-selected={active === matches.length}
        class:active={active === matches.length}
        on:pointerdown={keepFocus}
        on:click={() => pick(matches.length)}
      >
        Create <b>“{draft}”</b>
      </div>
      <button
        bind:this={swatchButton}
        type="button"
        class="swatch"
        data-tag-color={newColor}
        aria-label={`Colour: ${styleName(newColor)}`}
        aria-haspopup="true"
        aria-expanded={choosingColor}
        on:click={() => (choosingColor = !choosingColor)}
      >
        <span aria-hidden="true"></span>
      </button>
      {#if choosingColor}
        <ColorSwatchPicker
          value={newColor}
          anchor={swatchButton}
          label={`Colour for “${draft}”`}
          on:select={chooseColor}
          on:close={() => (choosingColor = false)}
        />
      {/if}
    </div>
  {:else if tags.length === 0}
    <p class="empty">No tags yet. Type a name to create one.</p>
  {/if}
</div>

<style>
  .tag-picker {
    display: flex;
    flex-direction: column;
    width: 300px;
    max-width: calc(100vw - 24px);
  }
  input {
    margin: 10px 10px 8px;
    padding: 7px 10px;
    font-size: 13.5px;
    color: inherit;
    background: var(--color-base-100);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-field, 0.5rem);
  }
  input:focus-visible {
    outline: none;
    box-shadow: 0 0 0 3px color-mix(in oklch, var(--color-base-content) 11%, transparent);
  }
  ul {
    max-height: 264px;
    margin: 0;
    padding: 6px;
    overflow-y: auto;
    overscroll-behavior: contain;
    list-style: none;
    border-top: 1px solid color-mix(in oklch, var(--color-base-content) 7%, transparent);
  }
  ul:empty {
    display: none;
  }
  [role="option"] {
    display: flex;
    align-items: center;
    gap: 9px;
    min-width: 0;
    padding: 6px 8px;
    font-size: 13.5px;
    border-radius: var(--radius-field, 0.5rem);
    cursor: pointer;
  }
  .active {
    background: var(--color-base-200);
  }
  .mark {
    display: inline-flex;
    color: var(--tag);
  }
  .name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .uses {
    flex: none;
    font-family: var(--font-mono);
    font-size: 11px;
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  .tick {
    display: grid;
    flex: none;
    place-items: center;
    width: 16px;
    height: 16px;
    color: var(--color-base-100);
    border: 1px solid color-mix(in oklch, var(--color-base-content) 25%, transparent);
    border-radius: var(--radius-selector, 0.25rem);
  }
  .tick.on {
    background: var(--tag);
    border-color: var(--tag);
  }
  .tick.mixed {
    color: var(--tag);
    background: var(--tag-bg);
    border-color: var(--tag);
  }
  .create {
    display: flex;
    align-items: center;
    gap: 4px;
    padding: 0 12px 6px 6px;
  }
  .create [role="option"] {
    flex: 1;
    overflow-wrap: anywhere;
  }
  .create b {
    font-weight: 650;
  }
  .swatch {
    display: grid;
    flex: none;
    place-items: center;
    width: 22px;
    height: 22px;
    padding: 0;
    background: none;
    border: 1px dashed transparent;
    border-radius: 999px;
    cursor: pointer;
  }
  .swatch span {
    width: 10px;
    height: 10px;
    border-radius: 999px;
    background: var(--tag);
  }
  .swatch:hover,
  .swatch:focus-visible,
  .swatch[aria-expanded="true"] {
    border-color: var(--tag);
  }
  .empty {
    padding: 0 12px 12px;
    font-size: 12.5px;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
</style>
