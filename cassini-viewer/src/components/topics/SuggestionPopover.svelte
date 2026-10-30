<script lang="ts">
  // One suggestion's evidence and decision (docs/topic-suggestions.md): what
  // the topic means, why it was suggested, the passages it rests on (from the
  // transcript already loaded), and Confirm or Not this meeting.
  import { createEventDispatcher } from "svelte";

  import { formatClockTime } from "../../core/transcript";
  import { COVERAGE_LIMITS, SUGGESTION_NOTE, TOPICS_PREVIEW_NOTE, UNRESOLVED_NOTE, UNVALIDATED_NOTE, whyLine, type TopicSuggestion } from "../../viewer/topics";
  import { popover } from "../tags/popover";

  export let suggestion: TopicSuggestion;
  export let anchor: HTMLElement;
  export let validatedLanguage = false;
  export let confirmable = true;
  export let busy = false;
  // The words of the loaded transcript in a time range.
  export let quote: (startMs: number, endMs: number) => string = () => "";

  const dispatch = createEventDispatcher<{ confirm: void; reject: void; seek: number; close: void }>();
  const MAX_QUOTES = 4;

  $: passages = [...suggestion.evidence].sort((a, b) => Number(b.counted) - Number(a.counted) || b.wordCount - a.wordCount).slice(0, MAX_QUOTES)
    .sort((a, b) => a.startMs - b.startMs);
</script>

<div use:popover={{ anchor, close: () => dispatch("close") }} role="dialog" aria-label={`Suggested topic: ${suggestion.label}`}
  class="tag-popover grid w-[min(360px,90vw)] gap-2 p-3 text-xs">
  <p class="text-sm font-semibold">{suggestion.label}</p>
  <p class="text-base-content/80">{suggestion.definition}</p>
  <p class="font-medium">{whyLine(suggestion)}</p>
  <ul class="grid gap-1.5">
    {#each passages as passage (passage.startMs)}
      {@const text = quote(passage.startMs, passage.endMs)}
      <li class="grid gap-0.5">
        <button type="button" class="link justify-self-start tabular-nums" aria-label={`Play from ${formatClockTime(passage.startMs)}`}
          on:click={() => dispatch("seek", passage.startMs)}>{formatClockTime(passage.startMs)}</button>
        {#if text}<q class="text-base-content/75">{text}</q>{/if}
      </li>
    {/each}
  </ul>
  <p class="text-base-content/60">{SUGGESTION_NOTE}</p>
  <p class="text-base-content/50">{COVERAGE_LIMITS}</p>
  <p class="text-base-content/60">{TOPICS_PREVIEW_NOTE}</p>
  {#if !validatedLanguage}<p class="text-warning-content/80" role="note">{UNVALIDATED_NOTE}</p>{/if}
  {#if !confirmable}<p class="text-error" role="note">{UNRESOLVED_NOTE}</p>{/if}
  <div class="flex justify-end gap-2">
    <button type="button" class="btn btn-ghost btn-sm" disabled={busy} on:click={() => dispatch("reject")}>Not this meeting</button>
    <button type="button" class="btn btn-neutral btn-sm" disabled={busy || !confirmable} on:click={() => dispatch("confirm")}>Confirm</button>
  </div>
</div>
