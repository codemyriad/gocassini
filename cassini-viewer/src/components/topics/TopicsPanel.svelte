<script lang="ts">
  // The Topics tab of Manage tags (docs/topic-suggestions.md): the topics
  // people described, with what they cover among the meetings the caller can
  // read, and their edit history.
  import { createEventDispatcher } from "svelte";

  import { AnnotationError, type VocabularyTag } from "../../viewer/annotations";
  import type { DataProvider } from "../../viewer/dataProvider";
  import { colorFor } from "../../viewer/tagPalette";
  import { usageLine, type Topic, type TopicLibrary, type TopicRevision, type TopicSave, type TopicTagRooms } from "../../viewer/topics";
  import TagChip from "../tags/TagChip.svelte";
  import TopicEditor from "./TopicEditor.svelte";

  type TopicProvider = Pick<DataProvider, "saveTopic" | "loadTopicHistory" | "redactTopicRevision" | "loadTopicTagRooms">;

  export let library: TopicLibrary;
  export let provider: TopicProvider;
  // A tag the Tags tab asked to describe.
  export let describe: VocabularyTag | null = null;
  // The caller's tags, so a typed name that is a tag's gets the warning.
  export let tags: readonly VocabularyTag[] = [];

  const dispatch = createEventDispatcher<{ changed: void; describeDone: void }>();

  type Editing = { topic: Topic | null; fromTag: VocabularyTag | null; rooms: TopicTagRooms | null; error: { code: string; tagId?: string } | null };
  let editing: Editing | null = null;
  let busy = false;
  let history: { topic: Topic; revisions: TopicRevision[]; error: string } | null = null;
  let notice = "";

  $: topics = library.library ?? [];
  $: active = topics.filter((t) => t.status === "active");
  $: retired = topics.filter((t) => t.status === "retired");
  $: if (describe) void startDescribe(describe);

  async function startDescribe(tag: VocabularyTag) {
    describe = null;
    dispatch("describeDone");
    editing = { topic: null, fromTag: tag, rooms: null, error: null };
    try {
      const rooms = await provider.loadTopicTagRooms?.(tag.tagId);
      if (editing?.fromTag?.tagId === tag.tagId && rooms) editing = { ...editing, rooms };
    } catch {
      // No room list: the generic notice still shows.
    }
  }

  async function save(change: TopicSave) {
    if (!editing || !provider.saveTopic) return;
    busy = true;
    try {
      const result = await provider.saveTopic(change);
      notice = result.job ? `Saved. Renaming it in your meetings…` : "Saved.";
      editing = null;
      dispatch("changed");
    } catch (e) {
      const err = e instanceof AnnotationError ? e : null;
      if (editing) editing = { ...editing, error: { code: err?.code ?? "", tagId: err?.tagId } };
    } finally {
      busy = false;
    }
  }

  async function setStatus(topic: Topic, status: "active" | "retired") {
    if (!provider.saveTopic || busy) return;
    busy = true;
    notice = "";
    try {
      await provider.saveTopic({ tagId: topic.tagId, expectRevision: topic.revision, status });
      notice = status === "retired" ? `“${topic.label}” is retired: no new suggestions. Its tags stay on meetings.` : `“${topic.label}” is restored.`;
      dispatch("changed");
    } catch (e) {
      notice = e instanceof AnnotationError && e.code === "label-taken"
        ? `Another topic is already called “${topic.label}”.`
        : "That did not work. Try again.";
    } finally {
      busy = false;
    }
  }

  async function restoreByID(tagId: string) {
    const topic = topics.find((t) => t.tagId === tagId);
    editing = null;
    if (topic) await setStatus(topic, "active");
  }

  async function openHistory(topic: Topic) {
    if (history?.topic.tagId === topic.tagId) {
      history = null;
      return;
    }
    history = { topic, revisions: [], error: "" };
    try {
      const revisions = (await provider.loadTopicHistory?.(topic.tagId)) ?? [];
      if (history?.topic.tagId === topic.tagId) history = { ...history, revisions };
    } catch {
      if (history) history = { ...history, error: "The history could not be loaded." };
    }
  }

  async function redact(revision: TopicRevision) {
    if (!history || !provider.redactTopicRevision) return;
    const topic = history.topic;
    try {
      await provider.redactTopicRevision(topic.tagId, revision.revision);
      history = null;
      await openHistory(topic);
    } catch {
      if (history) history = { ...history, error: "That revision could not be removed." };
    }
  }

  async function restoreRevision(topic: Topic, revision: TopicRevision) {
    if (!provider.saveTopic || busy) return;
    busy = true;
    try {
      await provider.saveTopic({ tagId: topic.tagId, expectRevision: topic.revision, label: revision.label, definition: revision.definition });
      notice = `“${topic.label}” is back to revision ${revision.revision}.`;
      history = null;
      dispatch("changed");
    } catch (e) {
      const code = e instanceof AnnotationError ? e.code : "";
      if (history) history = { ...history, error: code === "revision-conflict" ? "Someone changed this topic meanwhile. Close the history and open it again." : code === "label-taken" || code === "label-exists" ? "Another tag already has that name." : "That version could not be restored." };
    } finally {
      busy = false;
    }
  }

  const when = (iso: string) => new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
</script>

<div class="grid gap-2 px-2.5 py-2">
  <p class="text-xs text-base-content/70">
    Cassini suggests these topics for meetings by comparing transcripts with each definition. Someone confirms every suggestion before it becomes a tag.
  </p>
  {#if library.ready === false}
    <p class="alert alert-warning alert-soft py-1.5 text-xs" role="status">Suggestions are paused on this server; definitions are kept.</p>
  {/if}
  {#if notice}<p class="text-xs" role="status">{notice}</p>{/if}
  {#if editing && !editing.topic}
    <div class="rounded-box border border-base-300 bg-base-200 p-3">
      <TopicEditor fromTag={editing.fromTag} rooms={editing.rooms} error={editing.error} {busy} {tags}
        loadRooms={provider.loadTopicTagRooms ? (id) => provider.loadTopicTagRooms?.(id) ?? Promise.resolve(null) : null}
        on:save={(e) => save(e.detail)} on:cancel={() => (editing = null)} on:restore={(e) => restoreByID(e.detail)} />
    </div>
  {:else}
    <button type="button" class="btn btn-sm justify-self-start" on:click={() => (editing = { topic: null, fromTag: null, rooms: null, error: null })}>New topic</button>
  {/if}
  {#if active.length === 0 && !editing}
    <p class="py-4 text-sm text-base-content/60">No topics yet. Describe one, or describe an existing tag from the Tags tab.</p>
  {/if}
  <ul class="grid gap-1">
    {#each active as topic (topic.tagId)}
      <li class="topic-row grid gap-1 rounded-box px-1 py-2" data-tag-color={colorFor({ tagId: topic.tagId, color: topic.color ?? "" })}>
        {#if editing?.topic?.tagId === topic.tagId}
          <TopicEditor {topic} error={editing.error} {busy} on:save={(e) => save(e.detail)} on:cancel={() => (editing = null)} />
        {:else}
          <div class="flex flex-wrap items-center gap-2">
            <TagChip label={topic.label} color={colorFor({ tagId: topic.tagId, color: topic.color ?? "" })} icon={topic.icon ?? ""} />
            <span class="text-xs tabular-nums text-base-content/65">{usageLine(topic)}</span>
            {#if topic.preparing}<span class="badge badge-ghost badge-sm">Preparing</span>{/if}
            {#if topic.broad}<span class="badge badge-warning badge-soft badge-sm" title="Suggested or confirmed on most of the meetings you can read: the definition may be too general.">Very common</span>{/if}
            <span class="flex-1"></span>
            <button type="button" class="btn btn-ghost btn-xs" aria-label={`Edit ${topic.label}`} on:click={() => (editing = { topic, fromTag: null, rooms: null, error: null })}>Edit</button>
            <button type="button" class="btn btn-ghost btn-xs" aria-label={`History of ${topic.label}`} aria-expanded={history?.topic.tagId === topic.tagId} on:click={() => openHistory(topic)}>History</button>
            <button type="button" class="btn btn-ghost btn-xs" aria-label={`Retire ${topic.label}`} disabled={busy} on:click={() => setStatus(topic, "retired")}>Retire</button>
          </div>
          <p class="line-clamp-3 text-[13px] text-base-content/80">{topic.definition}</p>
        {/if}
        {#if history?.topic.tagId === topic.tagId}
          {@const shown = history}
          <div class="rounded-box border border-base-300 p-2 text-xs" role="region" aria-label={`History of ${topic.label}`}>
            {#if shown.error}<p class="text-error">{shown.error}</p>{/if}
            <ol class="grid gap-1.5">
              {#each [...shown.revisions].reverse() as revision (revision.revision)}
                <li class="grid gap-0.5">
                  <span class="text-base-content/60">Revision {revision.revision} · {when(revision.at)}</span>
                  {#if revision.redacted}
                    <span class="italic text-base-content/60">Removed from the history.</span>
                  {:else}
                    <span><strong>{revision.label}</strong>: {revision.definition}</span>
                    <span class="flex gap-2">
                      {#if revision.revision !== topic.revision}
                        <button type="button" class="link" on:click={() => restoreRevision(topic, revision)}>Use this version</button>
                      {/if}
                      {#if revision.canRedact}
                        <button type="button" class="link text-error" on:click={() => redact(revision)}>Remove from history</button>
                      {/if}
                    </span>
                  {/if}
                </li>
              {/each}
            </ol>
          </div>
        {/if}
      </li>
    {/each}
  </ul>
  {#if retired.length}
    <details class="text-sm">
      <summary class="cursor-pointer text-xs text-base-content/70">Retired ({retired.length})</summary>
      <ul class="mt-1 grid gap-1">
        {#each retired as topic (topic.tagId)}
          <li class="flex items-center gap-2 px-1 py-1">
            <TagChip label={topic.label} color={colorFor({ tagId: topic.tagId, color: topic.color ?? "" })} icon={topic.icon ?? ""} />
            <span class="flex-1 truncate text-xs text-base-content/60">{topic.definition}</span>
            <button type="button" class="btn btn-ghost btn-xs" aria-label={`Restore ${topic.label}`} disabled={busy} on:click={() => setStatus(topic, "active")}>Restore</button>
          </li>
        {/each}
      </ul>
    </details>
  {/if}
</div>
