<script lang="ts">
  import { claimEscape } from "../../ui/escapeClaim";
  import { createEventDispatcher, onMount } from "svelte";
  import { Plus } from "@lucide/svelte";

  import { LL } from "../../../i18n";
  import type { TagUpdate, VocabularyTag } from "../../../viewer/annotations";
  import { confirmLine, tagUpdate } from "../../../viewer/tagManager";
  import { colorFor, type TagColorId, type TagIconId } from "../../../viewer/tagPalette";
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
  $: text = $LL.viewer.browse.tags;

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
    if (event.key === "Enter") {
      event.preventDefault();
      save();
    } else if (event.key === "Escape") {
      claimEscape(event);
      dispatch("cancel");
    }
  }

  const toggle = (which: "color" | "icon") => (choosing = choosing === which ? null : which);
</script>

<div class="tm-row flex flex-wrap items-center gap-1.5" data-tag-color={color}>
  <input bind:this={input} bind:value={label} type="text" maxlength="64" autocomplete="off" spellcheck="false" aria-label={text.name_field()}
    class="input input-sm min-w-0 flex-[1_1_140px] text-[14px]" on:input={() => (conflict = null)} on:keydown={onKeydown} />
  <button bind:this={swatch} type="button" class="tm-pick btn btn-square btn-ghost btn-sm" title={text.colour()}
    aria-label={text.colour_named({ name: text.colors[color]() })} aria-haspopup="true" aria-expanded={choosing === "color"} on:click={() => toggle("color")}>
    <span class="size-3.5 rounded-full bg-(--tag)" aria-hidden="true"></span>
  </button>
  <button bind:this={iconButton} type="button" class="tm-pick btn btn-square btn-ghost btn-sm text-(--tag)"
    title={text.icon()} aria-label={icon ? text.icon_named({ name: text.icons[icon]() }) : text.add_icon()} aria-haspopup="true" aria-expanded={choosing === "icon"} on:click={() => toggle("icon")}>
    {#if icon}<TagIcon {icon} size={15} />{:else}<Plus size={14} class="text-base-content/50" />{/if}
  </button>
  <button type="button" class="tm-cancel cursor-pointer px-1" on:click={() => dispatch("cancel")}>{$LL.common.action.cancel()}</button>
  <button type="button" class="tm-save btn btn-neutral btn-sm px-4 font-semibold" disabled={!label.trim()} on:click={save}>{$LL.common.action.save()}</button>
  {#if conflict}
    {@const other = conflict}
    <p class="basis-full text-xs">
      {text.already_exists({ name: other.label })}
      <button type="button" class="link font-medium" on:click={() => dispatch("merge", other.tagId)}>{text.merge_instead({ name: other.label })}</button>
    </p>
  {/if}
  {#if renamed}<p class="basis-full text-xs text-base-content/65">{confirmLine(tag.meetings)}</p>{/if}
  {#if choosing === "color"}
    <ColorSwatchPicker value={color} anchor={swatch} label={text.colour_for({ name: tag.label })} on:close={() => (choosing = null)}
      on:select={(event) => ((color = event.detail), (choosing = null), swatch.focus())} />
  {:else if choosing === "icon"}
    <IconGrid value={icon} anchor={iconButton} on:close={() => (choosing = null)}
      on:select={(event) => ((icon = event.detail), (choosing = null), iconButton.focus())} />
  {/if}
</div>

<style>
  /* The two pickers are fields, like the name beside them: same border, same
     radius, so the row reads as one control strip. */
  .tm-pick {
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-field, 0.5rem);
  }
  /* The size Save is set in (daisyUI's btn-sm), so the two actions read as a
     pair rather than as a link beside a button. */
  .tm-cancel {
    /* Clear of the two pickers on one side and of Save on the other: between
       them it read as a third control in the strip. */
    margin-left: 8px;
    margin-right: 6px;
    background: none;
    border: 0;
    font-size: 0.75rem;
    font-weight: 600;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }
  .tm-cancel:hover {
    color: var(--color-base-content);
  }
</style>
