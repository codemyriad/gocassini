<script lang="ts">
  // New topics (docs/topic-suggestions.md): themes that keep coming up in a
  // room's meetings and no topic covers. Each card is a draft from the
  // meetings' own words; turning one into a topic needs a name and a
  // definition written by a person.
  import { createEventDispatcher } from "svelte";

  import { AnnotationError } from "../../viewer/annotations";
  import type { DataProvider } from "../../viewer/dataProvider";
  import { DEFINITION_LIMIT, TOPICS_PREVIEW_NOTE, TOPIC_NOTICE, newRequestId, sharedTerms, type Topic, type TopicProposal } from "../../viewer/topics";
  import PreviewBadge from "../ui/PreviewBadge.svelte";

  export let provider: Pick<DataProvider, "loadTopicProposals" | "decideTopicProposal" | "saveTopic">;
  // Active topics, to link a proposal to.
  export let topics: readonly Topic[] = [];

  const dispatch = createEventDispatcher<{ changed: void; openMeeting: { meetingId: string; startMs: number } }>();

  let proposals: TopicProposal[] = [];
  let loaded = false;
  let failed = false;
  // Every room with a proposal, from the last unfiltered load, so the
  // selector offers them all while one is selected.
  let roomOptions: [string, string][] = [];
  let loadSeq = 0;
  // Errors of an action on a card, by proposal id.
  let cardErrors: Record<string, string> = {};
  let room = "";
  let busy = false;
  let notice = "";
  let creating: { id: string; label: string; definition: string; confirm: string[] | null; error: string } | null = null;
  let linking: { id: string; tagId: string } | null = null;
  // After a link: the linked topic's definition, open for sharpening.
  let sharpening: { proposal: TopicProposal; topic: Topic; definition: string; confirm: string[] | null; error: string } | null = null;

  async function sharpen(confirmed = false) {
    if (!sharpening || !provider.saveTopic || busy) return;
    const { proposal, topic, definition } = sharpening;
    const terms = sharedTerms(definition, [proposal.draftName, ...proposal.hints]).filter((term) => !sharedTerms(topic.definition, [term]).length);
    if (terms.length && !confirmed) {
      sharpening = { ...sharpening, confirm: terms };
      return;
    }
    busy = true;
    try {
      await provider.saveTopic({ tagId: topic.tagId, expectRevision: topic.revision, definition: definition.trim() });
      notice = `“${topic.label}” has its new definition.`;
      sharpening = null;
      dispatch("changed");
    } catch {
      if (sharpening) sharpening = { ...sharpening, error: "The definition could not be saved." };
    } finally {
      busy = false;
    }
  }

  $: void load(room);
  $: rooms = roomOptions;
  $: open = proposals.filter((p) => p.state === "open");
  $: plain = open.filter((p) => !p.flags.routine && !p.flags.edges);
  $: flagged = open.filter((p) => p.flags.routine || p.flags.edges);
  $: dismissed = proposals.filter((p) => p.state === "dismissed");

  async function load(selected: string) {
    if (!provider.loadTopicProposals) return;
    const seq = ++loadSeq;
    try {
      const answer = await provider.loadTopicProposals(selected ? `id:${selected}` : undefined);
      if (seq !== loadSeq) return;
      proposals = answer.proposals ?? [];
      failed = false;
      if (!selected) roomOptions = [...new Map(proposals.map((p) => [p.roomId, p.room || "Unnamed room"])).entries()];
    } catch {
      if (seq !== loadSeq) return;
      failed = true;
      proposals = [];
    } finally {
      if (seq === loadSeq) loaded = true;
    }
  }

  async function act(proposal: TopicProposal, action: Parameters<NonNullable<DataProvider["decideTopicProposal"]>>[1], done: string) {
    if (!provider.decideTopicProposal || busy) return false;
    busy = true;
    notice = "";
    cardErrors = { ...cardErrors, [proposal.id]: "" };
    try {
      await provider.decideTopicProposal(proposal.id, action);
      notice = done;
      dispatch("changed");
      await load(room);
      return true;
    } catch (e) {
      const code = e instanceof AnnotationError ? e.code : "";
      const editing = action.action === "approve" && creating?.id === proposal.id ? creating : null;
      if (editing && code === "terms-from-meetings") {
        creating = { ...editing, confirm: sharedTerms(`${editing.label} ${editing.definition}`, [proposal.draftName, ...proposal.hints]) };
      } else if (editing) {
        creating = { ...editing, error: code === "label-taken" ? "Another topic already has that name." : "The topic could not be created." };
      } else {
        // On the card that was acted on, whatever else is open.
        cardErrors = { ...cardErrors, [proposal.id]: code.startsWith("proposal-") ? "Someone else decided about this meanwhile." : "That did not work. Try again." };
        if (code.startsWith("proposal-")) await load(room);
      }
      return false;
    } finally {
      busy = false;
    }
  }

  async function create(proposal: TopicProposal, confirmTerms = false) {
    if (!creating) return;
    const ok = await act(proposal, { requestId: newRequestId(), action: "approve", label: creating.label.trim(), definition: creating.definition.trim(), confirmTerms }, `“${creating.label.trim()}” is a topic now. Its suggestions will need review like any other.`);
    if (ok) creating = null;
  }

  const focusOnMount = (node: HTMLElement) => {
    node.focus();
  };
</script>

<div class="grid gap-2 px-2.5 py-2 text-sm">
  <div class="flex items-start gap-2">
    <PreviewBadge />
    <p class="text-xs text-base-content/70">{TOPICS_PREVIEW_NOTE}</p>
  </div>
  <p class="text-xs text-base-content/70">
    Themes that keep coming up in a room's meetings and no topic covers yet. You only see one when you can read every meeting it comes from.
  </p>
  {#if rooms.length > 1 || room}
    <label class="flex items-center gap-2 text-xs">Room
      <select class="select select-xs" bind:value={room} aria-label="Room">
        <option value="">All rooms</option>
        {#each rooms as [id, name] (id)}<option value={id}>{name}</option>{/each}
      </select>
    </label>
  {/if}
  {#if notice}<p class="text-xs" role="status">{notice}</p>{/if}
  {#if sharpening}
    {@const edit = sharpening}
    <div class="grid gap-1.5 rounded-box border border-base-300 p-3" aria-label={`Definition of ${edit.topic.label}`} role="group">
      <p class="text-xs">Sharpen the definition of “{edit.topic.label}” so it covers this theme too?</p>
      <textarea use:focusOnMount class="textarea textarea-sm" rows="3" aria-label="New definition" value={edit.definition}
        on:input={(e) => sharpening && (sharpening = { ...sharpening, definition: e.currentTarget.value, confirm: null })}></textarea>
      {#if edit.confirm}
        <div class="alert alert-warning alert-soft grid gap-1 py-2 text-xs" role="alertdialog" aria-label="Words from these meetings">
          <p>These words come from meetings in {edit.proposal.room}: <strong>{edit.confirm.join(", ")}</strong>. Everyone who can tag will see them.</p>
          <div class="flex gap-2">
            <button type="button" class="btn btn-xs" disabled={busy} on:click={() => sharpen(true)}>Save anyway</button>
            <button type="button" class="btn btn-ghost btn-xs" on:click={() => sharpening && (sharpening = { ...sharpening, confirm: null })}>Change the words</button>
          </div>
        </div>
      {/if}
      {#if edit.error}<p class="text-xs text-error" role="alert">{edit.error}</p>{/if}
      <div class="flex justify-end gap-2">
        <button type="button" class="btn btn-ghost btn-xs" on:click={() => (sharpening = null)}>Not now</button>
        <button type="button" class="btn btn-neutral btn-xs" disabled={busy || !edit.definition.trim() || edit.definition.trim() === edit.topic.definition} on:click={() => sharpen()}>Save definition</button>
      </div>
    </div>
  {/if}


  {#if failed}
    <p class="py-4 text-error" role="alert">New topics could not be loaded.</p>
  {:else if loaded && open.length === 0}
    <p class="py-4 text-base-content/60">No new topics to look at.</p>
  {/if}

  {#each [...plain, ...flagged] as proposal (proposal.id)}
    {@const isFlagged = proposal.flags.routine || proposal.flags.edges}
    <details class="rounded-box border border-base-300 p-3" open={!isFlagged} aria-label={`New topic: ${proposal.draftName}`}>
      <summary class="cursor-pointer">
        <span class="font-semibold">{proposal.draftName}</span>
        <span class="badge badge-ghost badge-sm ml-1">draft</span>
        <span class="ml-1 text-xs text-base-content/60">{proposal.meetings} meetings on {proposal.days} days · {proposal.room}</span>
        {#if proposal.flags.edges}<span class="ml-1 text-xs text-base-content/60">· mostly greetings and wrap-ups</span>{/if}
        {#if proposal.flags.routine}<span class="ml-1 text-xs text-base-content/60">· in most of the room's meetings</span>{/if}
      </summary>
      <div class="mt-2 grid gap-2">
        {#if proposal.hints.length}
          <p class="text-xs text-base-content/70">Words from meetings in {proposal.room}: {proposal.hints.join(", ")}</p>
        {/if}
        {#if proposal.nearest}<p class="text-xs">Close to the topic “{proposal.nearest.label}”: link it there if it is the same thing.</p>{/if}
        <ul class="grid gap-1">
          {#each proposal.evidence as e (e.meetingId)}
            <li class="text-xs">
              <button type="button" class="link font-medium" on:click={() => dispatch("openMeeting", { meetingId: e.meetingId, startMs: e.startMs })}>{e.title}</button>
              <span class="text-base-content/60">{e.date}</span>
              {#if e.quote}<q class="text-base-content/75">{e.quote}</q>{/if}
            </li>
          {/each}
        </ul>
        {#if cardErrors[proposal.id]}<p class="text-xs text-error" role="alert">{cardErrors[proposal.id]}</p>{/if}
        {#if creating?.id === proposal.id}
          {@const draft = creating}
          <div class="grid gap-1.5 rounded-box bg-base-200 p-2">
            <p class="text-xs text-base-content/70">Draft: <strong>{proposal.draftName}</strong>{#if proposal.hints.length} · words from meetings in {proposal.room}: {proposal.hints.join(", ")}{/if}</p>
            <input class="input input-sm" aria-label="Topic name" maxlength="64" placeholder="Name it in your words" bind:value={draft.label} on:input={() => creating && (creating = { ...creating, confirm: null, error: "" })} />
            <textarea class="textarea textarea-sm" rows="3" aria-label="Definition" placeholder="What is this topic about?" bind:value={draft.definition}
              on:input={() => creating && (creating = { ...creating, confirm: null, error: "" })}></textarea>
            <p class="text-xs text-base-content/70">{TOPIC_NOTICE}</p>
            {#if draft.confirm}
              <div class="alert alert-warning alert-soft grid gap-1 py-2 text-xs" role="alertdialog" aria-label="Words from these meetings">
                <p>These words come from meetings in {proposal.room}: <strong>{draft.confirm.join(", ")}</strong>. Everyone who can tag will see them.</p>
                <div class="flex gap-2">
                  <button type="button" class="btn btn-xs" disabled={busy} on:click={() => create(proposal, true)}>Save anyway</button>
                  <button type="button" class="btn btn-ghost btn-xs" on:click={() => creating && (creating = { ...creating, confirm: null })}>Change the words</button>
                </div>
              </div>
            {/if}
            {#if draft.error}<p class="text-xs text-error" role="alert">{draft.error}</p>{/if}
            <div class="flex justify-end gap-2">
              <button type="button" class="btn btn-ghost btn-xs" on:click={() => (creating = null)}>Cancel</button>
              <button type="button" class="btn btn-neutral btn-xs" disabled={busy || !draft.label.trim() || !draft.definition.trim() || [...draft.definition].length > DEFINITION_LIMIT}
                on:click={() => create(proposal)}>Create topic</button>
            </div>
          </div>
        {:else if linking?.id === proposal.id}
          <div class="flex items-center gap-2">
            <select class="select select-xs" aria-label="Topic to link to" value={linking.tagId}
              on:change={(e) => linking && (linking = { ...linking, tagId: e.currentTarget.value })}>
              <option value="">Choose a topic</option>
              {#each topics as topic (topic.tagId)}<option value={topic.tagId}>{topic.label}</option>{/each}
            </select>
            <button type="button" class="btn btn-xs" disabled={busy || !linking.tagId}
              on:click={async () => {
                if (!linking) return;
                const target = topics.find((topic) => topic.tagId === linking?.tagId);
                if (await act(proposal, { requestId: newRequestId(), action: "link", tagId: linking.tagId }, "Linked. That topic is checked again on these meetings.")) {
                  linking = null;
                  if (target && provider.saveTopic) sharpening = { proposal, topic: target, definition: target.definition, confirm: null, error: "" };
                }
              }}>Link</button>
            <button type="button" class="btn btn-ghost btn-xs" on:click={() => (linking = null)}>Cancel</button>
          </div>
        {:else}
          <div class="flex flex-wrap gap-2">
            <button type="button" class="btn btn-xs" on:click={() => (creating = { id: proposal.id, label: "", definition: "", confirm: null, error: "" })}>Create topic</button>
            <button type="button" class="btn btn-ghost btn-xs" disabled={topics.length === 0} on:click={() => (linking = { id: proposal.id, tagId: proposal.nearest?.tagId ?? "" })}>Link to topic</button>
            <button type="button" class="btn btn-ghost btn-xs" disabled={busy} on:click={() => act(proposal, { requestId: newRequestId(), action: "dismiss" }, "Dismissed: it will not be proposed again.")}>Dismiss</button>
          </div>
        {/if}
      </div>
    </details>
  {/each}

  {#if dismissed.length}
    <details class="text-xs">
      <summary class="cursor-pointer text-base-content/70">Dismissed ({dismissed.length})</summary>
      <ul class="mt-1 grid gap-1">
        {#each dismissed as proposal (proposal.id)}
          <li class="flex items-center gap-2">
            <span>{proposal.draftName}</span><span class="text-base-content/60">{proposal.room}</span>
            <button type="button" class="link" disabled={busy} on:click={() => act(proposal, { requestId: newRequestId(), action: "restore" }, "Restored.")}>Restore</button>
          </li>
        {/each}
      </ul>
    </details>
  {/if}
</div>
