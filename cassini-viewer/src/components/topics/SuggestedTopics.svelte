<script lang="ts">
  // Suggested topics on the meeting page (docs/topic-suggestions.md): after
  // the meeting's tags, the topics Cassini suggests from its transcript. A
  // person confirms one (it becomes an ordinary tag, credited to them) or says
  // "Not this meeting"; either can be undone from the toast, and a decision
  // can be reopened later. Nothing is shown where the provider has no topics.
  import { createEventDispatcher, onDestroy } from "svelte";
  import { ChevronDown } from "@lucide/svelte";

  import { AnnotationError, type MeetingAnnotations } from "../../viewer/annotations";
  import type { DataProvider, MeetingCatalogEntry } from "../../viewer/dataProvider";
  import { colorFor } from "../../viewer/tagPalette";
  import { meetingNote, newRequestId, type MeetingSuggestions, type TopicDecision, type TopicSuggestion } from "../../viewer/topics";
  import TagChip from "../tags/TagChip.svelte";
  import SuggestionPopover from "./SuggestionPopover.svelte";

  export let entry: MeetingCatalogEntry;
  export let provider: Pick<DataProvider, "loadMeetingSuggestions" | "decideSuggestion">;
  export let quote: (startMs: number, endMs: number) => string = () => "";
  export let seek: (ms: number) => void = () => {};
  // How long the Undo toast stays.
  export let toastMs = 8000;
  // The meeting's annotation revision: when its marks change (old marks
  // sorted out, a tag removed), what can be confirmed or reopened changes.
  export let marksRevision: number | null = null;

  const dispatch = createEventDispatcher<{ evidence: { startMs: number; endMs: number }[]; annotations: MeetingAnnotations }>();

  let view: MeetingSuggestions | null = null;
  let loadedFor = "";
  let open: { suggestion: TopicSuggestion; anchor: HTMLElement } | null = null;
  let busy = false;
  let error = "";
  let toast: { text: string; tagId: string; label: string; entry: MeetingCatalogEntry } | null = null;
  let toastTimer: ReturnType<typeof setTimeout> | undefined;
  // One request id per (topic, decision) attempt, kept across a retry.
  const requestIds = new Map<string, string>();

  $: if (entry.id !== loadedFor) switchTo(entry);
  // Reload when the meeting's marks change under us.
  let seenRevision = marksRevision;
  $: if (marksRevision !== seenRevision) {
    seenRevision = marksRevision;
    if (!busy) void load(entry);
  }

  // Another meeting: nothing of the last one may act on this one.
  function switchTo(meeting: MeetingCatalogEntry) {
    clearTimeout(toastTimer);
    toast = null;
    error = "";
    requestIds.clear();
    view = null;
    void load(meeting);
  }
  $: dispatch("evidence", open ? open.suggestion.evidence.map(({ startMs, endMs }) => ({ startMs, endMs })) : []);
  onDestroy(() => clearTimeout(toastTimer));

  async function load(meeting: MeetingCatalogEntry) {
    loadedFor = meeting.id;
    open = null;
    try {
      const next = (await provider.loadMeetingSuggestions?.(meeting)) ?? null;
      if (loadedFor === meeting.id) view = next;
    } catch {
      if (loadedFor === meeting.id) view = null;
    }
  }

  function requestFor(tagId: string, decision: TopicDecision) {
    const key = `${tagId}:${decision}`;
    let id = requestIds.get(key);
    if (!id) requestIds.set(key, (id = newRequestId()));
    return id;
  }

  async function decide(meeting: MeetingCatalogEntry, tagId: string, label: string, decision: TopicDecision) {
    if (!provider.decideSuggestion || busy) return;
    busy = true;
    error = "";
    const requestId = requestFor(tagId, decision);
    let ok = false;
    try {
      const result = await provider.decideSuggestion(meeting, tagId, decision, requestId);
      requestIds.delete(`${tagId}:${decision}`);
      // The answer belongs to the meeting it was asked for.
      if (meeting.id !== entry.id) return;
      ok = true;
      if (result.annotations) dispatch("annotations", result.annotations as unknown as MeetingAnnotations);
      open = null;
      if (decision === "confirm") showToast(`Tagged “${label}”.`, tagId, label);
      else if (decision === "reject") showToast(`“${label}” won't be suggested for this meeting.`, tagId, label);
      else toast = null;
    } catch (e) {
      if (meeting.id !== entry.id) return;
      const code = e instanceof AnnotationError ? e.code : "";
      error = code === "marks-unresolved"
        ? "This meeting's old tags need attention before a topic can be confirmed."
        : code === "already-decided" || code === "not-pending"
          ? "Someone decided this meanwhile."
          : code === "confirmed"
            ? `“${label}” is still a tag on this meeting. Remove the tag first.`
            : "That did not work. Try again.";
      if (decision === "undo") showToast(`The change to “${label}” could not be undone.`, tagId, label);
      if (code === "already-decided" || code === "not-pending" || code === "confirmed") {
        requestIds.delete(`${tagId}:${decision}`);
        ok = true;
      }
    } finally {
      busy = false;
    }
    if (ok && meeting.id === entry.id) await load(meeting);
  }

  function showToast(text: string, tagId: string, label: string) {
    clearTimeout(toastTimer);
    toast = { text, tagId, label, entry };
    toastTimer = setTimeout(() => (toast = null), toastMs);
  }

  function undo() {
    if (!toast || toast.entry.id !== entry.id) return;
    const { tagId, label, entry: meeting } = toast;
    clearTimeout(toastTimer);
    toast = null;
    void decide(meeting, tagId, label, "undo");
  }

  $: suggestions = view?.suggestions ?? [];
  $: decided = view?.decided ?? [];
  $: note = view?.enabled && suggestions.length === 0 ? meetingNote(view) : "";
</script>

{#if view?.enabled}
  <div class="flex flex-wrap items-center gap-1.5 text-xs" role="group" aria-label="Suggested topics">
    {#if suggestions.length}
      <span class="text-base-content/60">Suggested</span>
      {#each suggestions as suggestion (suggestion.tagId)}
        <button type="button" class="suggested-chip" aria-haspopup="dialog" aria-expanded={open?.suggestion.tagId === suggestion.tagId}
          aria-label={`Suggested topic ${suggestion.label}`}
          on:click={(event) => (open = open?.suggestion.tagId === suggestion.tagId ? null : { suggestion, anchor: event.currentTarget })}>
          <TagChip suggested label={suggestion.label} color={colorFor({ tagId: suggestion.tagId, color: suggestion.color ?? "" })} icon={suggestion.icon ?? ""} />
        </button>
      {/each}
    {/if}
    {#if note}<span class="text-base-content/60" role="note">{note}</span>{/if}
    {#if decided.length}
      <details class="decided">
        <summary class="inline-flex cursor-pointer items-center gap-0.5 text-base-content/60">Decided for this meeting ({decided.length})<ChevronDown size={12} aria-hidden="true" /></summary>
        <ul class="mt-1 grid gap-1">
          {#each decided as d (d.tagId)}
            <li class="flex items-center gap-2">
              <span>{d.label}</span>
              <span class="text-base-content/60">{d.verdict === "confirmed" ? (d.source === "direct" ? "already tagged" : "confirmed") : "not this meeting"}</span>
              <button type="button" class="link" disabled={busy} on:click={() => decide(entry, d.tagId, d.label, "reopen")}>Reopen</button>
            </li>
          {/each}
        </ul>
      </details>
    {/if}
    {#if error}<span class="text-error" role="alert">{error}</span>{/if}
    {#if toast}
      {@const shown = toast}
      <span class="toast-inline" role="status">{shown.text} <button type="button" class="link font-medium" on:click={undo}>Undo</button></span>
    {/if}
  </div>
  {#if open}
    {@const current = open}
    <SuggestionPopover suggestion={current.suggestion} anchor={current.anchor} {quote} {busy}
      validatedLanguage={!!view.validatedLanguage} confirmable={view.confirmable !== false}
      on:close={() => (open = null)} on:seek={(e) => seek(e.detail)}
      on:confirm={() => decide(entry, current.suggestion.tagId, current.suggestion.label, "confirm")}
      on:reject={() => decide(entry, current.suggestion.tagId, current.suggestion.label, "reject")} />
  {/if}
{/if}

<style>
  .suggested-chip {
    display: inline-flex;
    cursor: pointer;
  }
  .toast-inline {
    padding: 2px 8px;
    border-radius: 6px;
    background: color-mix(in oklch, var(--color-base-content) 8%, transparent);
  }
</style>
