<script lang="ts">
  // Describing a topic (docs/topic-suggestions.md): its name, the definition
  // Cassini compares transcripts with, and its colour. Creating one from an
  // existing tag keeps that tag's id, so its marks count as the topic's.
  import { createEventDispatcher, onMount } from "svelte";

  import { colorFor, styleName, type TagColorId } from "../../viewer/tagPalette";
  import { DEFINITION_LIMIT, roomWarning, TOPIC_NOTICE, type Topic, type TopicSave, type TopicTagRooms } from "../../viewer/topics";
  import ColorSwatchPicker from "../tags/ColorSwatchPicker.svelte";

  // The topic being edited, or null for a new one.
  export let topic: Topic | null = null;
  // Describing an existing tag: its id and label.
  export let fromTag: { tagId: string; label: string; color?: TagColorId | "" } | null = null;
  // Where the tag's visible meetings come from, for the warning.
  export let rooms: TopicTagRooms | null = null;
  // The last refusal, as the server's code and the tag it named.
  export let error: { code: string; tagId?: string; label?: string } | null = null;
  export let busy = false;
  // The caller's tags, to tell when a typed name is an existing tag's, and how
  // to ask where that tag's meetings come from.
  export let tags: readonly { tagId: string; label: string }[] = [];
  export let loadRooms: ((tagId: string) => Promise<TopicTagRooms | null>) | null = null;

  const dispatch = createEventDispatcher<{ save: TopicSave; cancel: void; restore: string }>();
  let label = topic?.label ?? fromTag?.label ?? "";
  let definition = topic?.definition ?? "";
  let color: TagColorId = (topic?.color ?? fromTag?.color ?? colorFor({ tagId: topic?.tagId ?? fromTag?.tagId ?? label, color: "" })) as TagColorId;
  let choosing = false;
  let swatch: HTMLButtonElement;
  let input: HTMLInputElement;

  onMount(() => input?.focus());

  $: remaining = DEFINITION_LIMIT - [...definition.trim()].length;
  $: valid = label.trim() !== "" && definition.trim() !== "" && remaining >= 0;
  // A new topic whose name is an existing tag's adopts that tag.
  $: matched = !topic && !fromTag ? tags.find((t) => t.label.trim().toLowerCase() === label.trim().toLowerCase()) ?? null : null;
  let matchedRooms: TopicTagRooms | null = null;
  let roomsFor = "";
  $: if (matched && matched.tagId !== roomsFor) void fetchRooms(matched.tagId);
  async function fetchRooms(tagId: string) {
    roomsFor = tagId;
    matchedRooms = null;
    try {
      const found = (await loadRooms?.(tagId)) ?? null;
      if (roomsFor === tagId) matchedRooms = found;
    } catch {
      // The general warning stays.
    }
  }
  $: adopting = !!fromTag || !!matched;
  $: warning = roomWarning(fromTag ? rooms : matchedRooms, !!fromTag, adopting);

  function save(separate = false) {
    if (!valid || busy) return;
    if (topic) {
      const update: TopicSave = { tagId: topic.tagId, expectRevision: topic.revision };
      if (label.trim() !== topic.label) update.label = label.trim();
      if (definition.trim() !== topic.definition) update.definition = definition.trim();
      if (color !== (topic.color ?? "")) update.color = color;
      if (Object.keys(update).length === 2) return dispatch("cancel");
      dispatch("save", update);
    } else {
      dispatch("save", { label: label.trim(), definition: definition.trim(), color, fromTagId: fromTag?.tagId, separate: separate || undefined });
    }
  }

  const message = (e: NonNullable<typeof error>): string => {
    switch (e.code) {
      case "label-taken": return `“${label.trim()}” is already a topic.`;
      case "topic-retired": return `“${label.trim()}” was a topic that has been retired.`;
      case "revision-conflict": return "Someone else changed this topic meanwhile. Close it and open it again.";
      case "rate-limited": return "You have changed many definitions in the last hour. Try again later.";
      case "library-full": return "The library already has the most topics it can hold. Retire one first.";
      case "disabled": return "Topic suggestions were switched off.";
      default: return "The topic could not be saved. Try again.";
    }
  };
</script>

<form class="grid gap-2" aria-label={topic ? `Edit topic ${topic.label}` : "New topic"} on:submit|preventDefault={() => save()}>
  <div class="flex items-center gap-1.5" data-tag-color={color}>
    <input bind:this={input} bind:value={label} type="text" maxlength="64" autocomplete="off" aria-label="Topic name"
      class="input input-sm min-w-0 flex-1 text-[14px]" disabled={!!fromTag && !topic} on:input={() => (error = null)} />
    <button bind:this={swatch} type="button" class="btn btn-square btn-ghost btn-sm" aria-label={`Colour: ${styleName(color)}`}
      aria-haspopup="true" aria-expanded={choosing} on:click={() => (choosing = !choosing)}>
      <span class="size-3.5 rounded-full bg-(--tag)" aria-hidden="true"></span>
    </button>
  </div>
  <label class="grid gap-1 text-xs">
    <span class="font-medium text-base-content/80">What is this topic about?</span>
    <textarea bind:value={definition} rows="4" class="textarea textarea-sm w-full text-[13.5px]" aria-label="Definition"
      placeholder="Describe the subject in your own words, as you would explain it to a new colleague."
      on:input={() => (error = null)}></textarea>
    <span class="justify-self-end tabular-nums" class:text-error={remaining < 0} aria-live="polite">{remaining} characters left</span>
  </label>
  <p class="text-xs text-base-content/70">{TOPIC_NOTICE}</p>
  {#if warning}<p class="alert alert-warning alert-soft py-1.5 text-xs" role="note">{warning}</p>{/if}
  {#if error}
    {@const refusal = error}
    <div class="text-xs text-error" role="alert">
      {message(refusal)}
      {#if refusal.code === "topic-retired" && refusal.tagId}
        <button type="button" class="link ml-1 font-medium" on:click={() => dispatch("restore", refusal.tagId ?? "")}>Restore it</button>
        <span class="text-base-content/60">or</span>
        <button type="button" class="link font-medium" on:click={() => save(true)}>create a separate topic</button>
      {/if}
    </div>
  {/if}
  <div class="flex justify-end gap-2">
    <button type="button" class="btn btn-ghost btn-sm" on:click={() => dispatch("cancel")}>Cancel</button>
    <button type="submit" class="btn btn-neutral btn-sm px-4" disabled={!valid || busy}>{topic ? "Save" : "Add topic"}</button>
  </div>
  {#if choosing}
    <ColorSwatchPicker value={color} anchor={swatch} label="Topic colour" on:close={() => (choosing = false)}
      on:select={(event) => ((color = event.detail), (choosing = false), swatch.focus())} />
  {/if}
</form>
