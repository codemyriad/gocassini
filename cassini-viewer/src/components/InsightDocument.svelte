<script lang="ts">
  import { createEventDispatcher, tick } from "svelte";
  import { marked } from "marked";
  import DOMPurify from "dompurify";
  import { Calendar, FileText, MessageSquare, Pencil } from "@lucide/svelte";
  import CloseButton from "./ui/CloseButton.svelte";
  import { formatMeetingDateWithDay, type MeetingCatalogEntry } from "../viewer/catalog";
  import { roomLabelOf } from "../viewer/rooms";
  import { INSIGHT_NAME_MAX } from "../viewer/insightName";
  import {
    describeInsightFailure,
    formatInsightCreated,
    formatInsightStatus,
    insightHeadline,
    sameHeadlineText,
    stripInsightFrontMatter,
    stripLeadingHeading,
    type InsightRecord,
  } from "../viewer/insights";

  // The insight document (D-721). It opens in the same sheet a meeting opens
  // in, because an insight is a few hundred words: it reads in the list rather
  // than on a page of its own.
  //
  // Order is the prototype's, and its reason is worth keeping: what it read
  // comes before what it said — the question, then the material, then the
  // answer. What is NOT the prototype's is the material: it fabricated the
  // source list by bucketing the catalog by room and taking the first four.
  // The real set is on the record, and this renders that.
  //
  // Presentational: the shell owns the record, resolves which sources this
  // caller may see, and fetches the document.

  export let insight: InsightRecord;
  // The run's source meetings, in the order it recorded them, narrowed to the
  // ones this caller can read. A source they may not read is absent and
  // uncounted — the section itself disappears rather than announcing that
  // there was material they cannot see.
  export let sources: MeetingCatalogEntry[] = [];
  export let documentMarkdown = "";
  export let documentError = "";
  export let documentLoading = false;
  // False where the build can list insights but cannot fetch one's document.
  // Then the panel says so, rather than rendering an answer-shaped blank.
  export let canLoadDocument = true;
  // Whether a failed run can be retried from here (D-749): true where the
  // provider offers it, and only then is the control rendered. The shell owns
  // the request; this only asks.
  export let canRetry = false;
  export let retrying = false;
  export let retryError = "";
  // Whether the name can be changed from here (D-791), in the same shape as
  // retry: the provider offers it, the shell sends it. This is the one place an
  // insight is renamed; the card and the list only show the name.
  export let canRename = false;
  export let renaming = false;
  export let renameError = "";

  const dispatch = createEventDispatcher<{
    close: void;
    openSource: MeetingCatalogEntry;
    retry: void;
    rename: string;
    // The editor was closed without a save, so a refusal shown in it is over:
    // the shell drops it, and the next opening starts clean.
    renameCancel: void;
  }>();

  // The heading turns into its own input, pre-filled with what it says, with
  // Cancel and Save beside it as the tag manager's row has: this viewer's two
  // inline editors should not behave differently from each other. Enter saves
  // and Escape cancels, and nothing else leaves the editor, so clicking away to
  // check something does not throw a half-typed name out. It stays an input
  // until the save has landed, so a refused name is still there to fix next to
  // the reason it was refused.
  let editingName = false;
  let editingId = "";
  let draftName = "";
  let savingName = false;
  let nameInput: HTMLInputElement | undefined;
  let renameButton: HTMLButtonElement | undefined;

  async function startRename() {
    draftName = headline;
    editingId = insight.id;
    editingName = true;
    await tick();
    nameInput?.select();
  }

  // leaveRename closes the editor and puts the keyboard back on the pencil it
  // was opened from. The pencil is not rendered while editing, so without this
  // focus fell to the document body.
  async function leaveRename() {
    editingName = false;
    savingName = false;
    await tick();
    renameButton?.focus();
  }

  function cancelRename() {
    void leaveRename();
    dispatch("renameCancel");
  }

  // An empty name cannot be saved, as an empty tag name cannot: the button is
  // disabled rather than the request refused.
  $: nameSaveable = draftName.trim() !== "" && !renaming;

  function saveRename() {
    if (!nameSaveable) {
      return;
    }
    const name = draftName.trim();
    if (name === headline) {
      // Nothing changed. Saving it would turn a headline that is only
      // standing in for a name — the question — into a name.
      cancelRename();
      return;
    }
    savingName = true;
    dispatch("rename", name);
  }

  function onNameKeydown(event: KeyboardEvent) {
    if (event.key !== "Enter" && event.key !== "Escape") {
      return;
    }
    event.preventDefault();
    if (event.key === "Enter") {
      saveRename();
    } else {
      cancelRename();
    }
  }

  // Another insight in the same sheet is not the one being renamed.
  $: if (editingName && insight.id !== editingId) {
    editingName = false;
    savingName = false;
  }
  $: if (savingName && !renaming) {
    if (renameError) {
      // Refused: the input is enabled again, and the keyboard goes back to it
      // so that Enter and Escape mean something without a click first.
      savingName = false;
      void tick().then(() => nameInput?.focus());
    } else {
      void leaveRename();
    }
  }
  $: renameErrorId = `ins-rename-error-${insight.id}`;

  $: question = insight.question?.trim() ?? "";
  $: headline = insightHeadline(insight);
  $: showQuestion = question !== "" && !sameHeadlineText(question, headline);
  $: pending = insight.status === "queued" || insight.status === "running";
  $: failed = insight.status === "failed";
  // Never `insight.error` raw: the operator records a reason token and this
  // build owns every word said for it (insights.ts, INSIGHT_FAILURE_COPY).
  $: failure = failed ? describeInsightFailure(insight) : null;

  // The mock files an insight under one room. A real one can span them —
  // spanning rooms is the whole premise of asking one question of several
  // meetings — so this names the room only when every source agrees, and counts
  // them otherwise. Read off the resolved sources rather than record.roomIds,
  // so it names what this caller can actually see and never discloses a room
  // whose meeting is hidden from them.
  $: roomLabels = [...new Set(sources.map((source) => roomLabelOf(source)))];
  $: room =
    roomLabels.length === 1
      ? roomLabels[0]
      : roomLabels.length > 1
        ? `${roomLabels.length} rooms`
        : "";

  function renderDocumentHtml(markdown: string, quoted: boolean): string {
    // The recorder writes its provenance as YAML front matter, and to markdown
    // `---` is a horizontal rule: rendered as-is it became two rules with a
    // run-on paragraph of quoted hashes and IDs between them, sitting above the
    // answer somebody actually asked for. The facts in it are shown by this
    // panel anyway — the header and the provenance list below both read them
    // off the run record — so it is cut here rather than kept as text nobody
    // can use.
    // The document's own first heading goes only when the quote sits right
    // above it saying the same thing (the Ask template copies the question in
    // verbatim). With no quote — the name IS the question — the heading stays:
    // it is the document's title, in the place the question is read.
    const front = stripInsightFrontMatter(markdown);
    const body = quoted ? stripLeadingHeading(front, question) : front;
    if (body.trim() === "") {
      return "";
    }
    // Same two steps MeetingView renders a sealed summary with: the document is
    // model output, so it is parsed as markdown and then sanitised, never
    // trusted into the DOM as it arrived.
    const rawHtml = marked.parse(body, { async: false }) as string;
    return DOMPurify.sanitize(rawHtml, { USE_PROFILES: { html: true } });
  }

  $: documentHtml = renderDocumentHtml(documentMarkdown, showQuestion);
</script>

<section class="ins-doc" aria-label="Insight">
  <!-- The insight's own identity, and it stays put while the answer scrolls
       under it. The eyebrow that used to be the whole header said only which
       KIND of thing the sheet was holding, which the amber rule down its side
       already says; the room, the date and the model that answered were at the
       bottom of the page, under the answer, where they are provenance nobody
       reaches. The three that identify this run are here. -->
  <header class="ins-head">
    <div class="ins-head-top">
      <div class="ins-head-title">
        {#if editingName}
          <!-- One strip, as the tag manager's: the name, then Cancel and
               Save, in that order and at those sizes. -->
          <div class="ins-name-editor">
            <input
              bind:this={nameInput}
              bind:value={draftName}
              type="text"
              class="input input-sm ins-name-input"
              aria-label="Insight name"
              aria-invalid={renameError ? "true" : undefined}
              aria-describedby={renameError ? renameErrorId : undefined}
              maxlength={INSIGHT_NAME_MAX}
              autocomplete="off"
              spellcheck="false"
              disabled={renaming}
              on:keydown={onNameKeydown}
            />
            <button type="button" class="ins-name-cancel" on:click={cancelRename}>Cancel</button>
            <button
              type="button"
              class="btn btn-neutral btn-sm px-4 font-semibold"
              disabled={!nameSaveable}
              on:click={saveRename}
            >
              {renaming ? "Saving…" : "Save"}
            </button>
          </div>
        {:else}
          <h2>{headline}</h2>
        {/if}
      </div>
      <!-- The pencil keeps the close button's company at the right edge,
           whatever the title's length: a control that trailed the last word
           of the title landed somewhere different on every insight. -->
      <div class="ins-head-controls">
        {#if canRename && !editingName}
          <button
            bind:this={renameButton}
            type="button"
            class="ins-rename btn btn-sm btn-square btn-quiet"
            aria-label="Rename the insight"
            title="Rename"
            on:click={startRename}
          >
            <Pencil size={14} aria-hidden="true" />
          </button>
        {/if}
        <CloseButton label="Close the insight" on:click={() => dispatch("close")} />
      </div>
    </div>
    {#if editingName && renameError}
      <p class="ins-rename-error" id={renameErrorId} role="status">{renameError}</p>
    {/if}
    <div class="ins-head-meta">
      {#if room}
        <span class="ins-head-fact">
          <MessageSquare size={13} aria-hidden="true" />
          {room}
        </span>
      {/if}
      <span class="ins-head-fact">
        <Calendar size={13} aria-hidden="true" />
        {formatInsightCreated(insight)}
      </span>
      {#if insight.model}
        <code class="ins-head-model">{insight.model}</code>
      {/if}
    </div>
  </header>

  <div class="ins-body">
    <!-- 1. The material. -->
    {#if sources.length > 0}
      <!-- One card: the count heads the list it counts, inside it. -->
      <section class="ins-sources ins-card">
        <h3 class="ins-card-title">
          Context from {sources.length}
          {sources.length === 1 ? "meeting" : "meetings"}
        </h3>
        <ul>
          {#each sources as source (source.id)}
            <li>
              <!-- Each source opens its own sheet: from here the meeting is the
                   thing you want next, and it is one press away. -->
              <button type="button" on:click={() => dispatch("openSource", source)}>
                <FileText size={14} aria-hidden="true" />
                <span class="ins-source-title">{source.title}</span>
                <span class="ins-source-meta">
                  {formatMeetingDateWithDay(source.dateLabel)}
                </span>
              </button>
            </li>
          {/each}
        </ul>
      </section>
    {/if}

    <!-- 2. The question, where it is a different thing from the name above:
         an Ask run is named after its question unless somebody renames it. It
         sits under the material and over the answer, which is where the
         document puts its own heading — and when the two say the same thing
         the document's heading gives way to this. A workflow with no question
         of its own ("summarise these") has no quote to show. -->
    {#if showQuestion}
      <p class="ins-question">“{question}”</p>
    {/if}

    <!-- 3. The answer, or the honest reason there is not one yet. -->
    {#if pending}
      <p class="ins-note" role="status">
        {formatInsightStatus(insight.status)}. The answer appears here when it finishes.
      </p>
    {:else if failure}
      <div class="ins-note ins-note-error" role="status">
        <p class="ins-failure-title">{failure.title}</p>
        <p>{failure.line}</p>
        {#if canRetry}
          <!-- Retry from where the failure is seen, not only from the panel
               that started the run — which is gone by the time most people
               notice (D-749). A retry re-runs the request: the endpoint this
               insight asked for, falling back to the deployment's own only if
               that endpoint has since been removed. -->
          <div class="ins-retry">
            <button type="button" disabled={retrying} on:click={() => dispatch("retry")}>
              {retrying ? "Retrying…" : "Retry"}
            </button>
          </div>
          {#if retryError}
            <p class="ins-failure-detail">{retryError}</p>
          {/if}
        {/if}
      </div>
    {:else if !canLoadDocument}
      <p class="ins-note" role="status">
        This build cannot fetch the document — the run and what it read are all it can show.
      </p>
    {:else if documentLoading}
      <p class="ins-note" role="status">Loading the answer…</p>
    {:else if documentError}
      <p class="ins-note ins-note-error" role="status">
        The answer could not be loaded: {documentError}
      </p>
    {:else if documentHtml}
      <!-- Markdown rendered via {@html} cannot receive Svelte-scoped styles, so
           per-tag styling is expressed through Tailwind's arbitrary descendant
           selectors on the wrapper — the same way the meeting summary is
           styled. -->
      <div
        class="text-base leading-relaxed text-base-content
          [&>*+*]:mt-3.5
          [&>h1:first-child]:mt-0 [&>h2:first-child]:mt-0 [&>h3:first-child]:mt-0
          [&_h1]:text-lg [&_h1]:font-semibold [&_h1]:mt-5 [&_h1]:leading-tight
          [&_h2]:text-lg [&_h2]:font-semibold [&_h2]:mt-5 [&_h2]:leading-tight
          [&_h3]:text-base [&_h3]:font-semibold [&_h3]:mt-4 [&_h3]:leading-tight
          [&_strong]:font-semibold [&_em]:italic
          [&_a]:text-primary [&_a]:underline [&_a]:underline-offset-2
          [&_ul]:pl-6 [&_ul]:grid [&_ul]:gap-1.5 [&_ul]:list-disc
          [&_ol]:pl-6 [&_ol]:grid [&_ol]:gap-1.5 [&_ol]:list-decimal
          [&_li]:marker:text-base-content/55
          [&_code]:font-mono [&_code]:text-[0.875em] [&_code]:px-1.5 [&_code]:py-0.5 [&_code]:rounded [&_code]:bg-base-300
          [&_pre]:font-mono [&_pre]:text-sm [&_pre]:p-3 [&_pre]:rounded-lg [&_pre]:bg-base-300 [&_pre]:overflow-x-auto
          [&_pre_code]:p-0 [&_pre_code]:bg-transparent [&_pre_code]:text-[1em]
          [&_blockquote]:border-l-[3px] [&_blockquote]:border-primary/60 [&_blockquote]:pl-3.5
          [&_hr]:border-0 [&_hr]:border-t [&_hr]:border-base-300"
      >{@html documentHtml}</div>
    {:else}
      <!-- A succeeded run always has a path — the operator refuses to record
           a success without one — so an empty document here means the file
           is not where it was written, not that the model wrote nothing. It
           is the requester's own file; they may have moved or deleted it. -->
      <p class="ins-note" role="status">
        This run succeeded, but its document is no longer at
        <code>{insight.documentPath}</code> in your Nextcloud files.
      </p>
    {/if}

    <!-- 4. Provenance. The prototype showed four of the record's fields and
         invented the most important one; a re-run is append-only, so what ran,
         who asked for it and how it ended have to be on the document itself or
         two attempts are indistinguishable. -->
    <!-- What is left after the header took the date and the model: who asked,
         and which prompt answered. A re-run is append-only, so what ran, who
         asked for it and how it ended have to be ON the document or two
         attempts are indistinguishable. -->
    <section class="ins-prov">
      <h3 class="ins-title">This run</h3>
      <dl>
        <div>
          <dt>Asked by</dt>
          <dd>{insight.createdBy || "unknown"}</dd>
        </div>
        <div>
          <dt>Status</dt>
          <dd>{formatInsightStatus(insight.status)}</dd>
        </div>
        <div>
          <dt>Template</dt>
          <dd><code>{insight.workflowId}</code> {insight.workflowVersion}</dd>
        </div>
        {#if insight.provider}
          <div>
            <dt>Provider</dt>
            <dd>{insight.provider}</dd>
          </div>
        {/if}
        {#if insight.attemptNumber > 1}
          <div>
            <dt>Attempt</dt>
            <dd>{insight.attemptNumber}</dd>
          </div>
        {/if}
      </dl>
    </section>
  </div>
</section>

<style>
  .ins-doc {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
    /* The drawer ground the Prepare panel and the meeting sheet use, with what
       is lifted off it on the lighter card surface. The insight used to be the
       other way up, which made one sheet read as two different surfaces
       depending on what it was holding. */
    background-color: var(--color-base-200);
    border-left: 4px solid var(--color-primary);
  }

  .ins-head {
    flex: none;
    padding: 1rem 1.25rem;
    border-bottom: 1px solid var(--color-base-300);
  }
  .ins-head-top {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 0.75rem;
    min-width: 0;
  }
  .ins-head-title {
    display: flex;
    flex: 1 1 auto;
    align-items: flex-start;
    gap: 0.25rem;
    min-width: 0;
  }
  /* The two 32px squares, lifted together so they sit on the title's first
     line rather than a quarter-rem under it. */
  .ins-head-controls {
    display: flex;
    flex: none;
    align-items: center;
    gap: 0.25rem;
    margin-top: -0.25rem;
  }
  /* Quieter than the heading it sits by until it is wanted. */
  .ins-rename {
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  .ins-name-editor {
    display: flex;
    flex: 1 1 auto;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.375rem;
    min-width: 0;
  }
  .ins-name-input {
    flex: 1 1 10rem;
    min-width: 0;
    font-size: 1.0625rem;
    font-weight: 650;
  }
  /* The tag editor's Cancel: a quiet word beside Save, not a second button. */
  .ins-name-cancel {
    margin: 0 0.25rem 0 0.5rem;
    background: none;
    border: 0;
    font-size: 0.75rem;
    font-weight: 600;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
    cursor: pointer;
  }
  .ins-name-cancel:hover {
    color: var(--color-base-content);
  }
  .ins-rename-error {
    margin-top: 0.375rem;
    font-size: 0.8125rem;
    color: var(--color-error);
  }
  .ins-head h2 {
    min-width: 0;
    font-size: 1.0625rem;
    font-weight: 650;
    line-height: 1.3;
    color: var(--color-base-content);
  }
  .ins-head-meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.25rem 1rem;
    margin-top: 0.375rem;
    font-size: 0.8125rem;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .ins-head-fact {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
  }
  /* The chip the insight card's provider and model wear, so the model reads
     as the name of a thing rather than as another fact in the row. */
  .ins-head-model {
    padding: 1px 6px;
    background-color: var(--color-base-100);
    border: 1px solid color-mix(in oklch, var(--color-base-content) 14%, var(--color-base-100));
    border-radius: 5px;
    font-family: var(--font-mono, ui-monospace, monospace);
    font-size: 11.5px;
    overflow-wrap: anywhere;
    color: color-mix(in oklch, var(--color-base-content) 75%, transparent);
  }

  .ins-body {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    overscroll-behavior: contain;
    display: flex;
    flex-direction: column;
    gap: 1.25rem;
    padding: 1.25rem;
  }

  .ins-question {
    margin: 0;
    font-size: 1.0625rem;
    font-weight: 550;
    line-height: 1.4;
    color: var(--color-base-content);
  }

  /* The meeting sheet's section heading (MeetingView's .mv-section-title),
     0.5rem over what it heads. */
  .ins-title {
    margin: 0 0 0.5rem;
    font-size: 15px;
    font-weight: 600;
    color: var(--color-base-content);
  }

  /* The meeting sheet's card (its .mv-card), which is the operator's: a tint
     of the ink over the drawer's ground. */
  .ins-card {
    background-color: color-mix(in oklch, var(--color-base-content) 4%, var(--color-base-200));
    border: 1px solid color-mix(in oklch, var(--color-base-content) 9%, var(--color-base-200));
    border-radius: var(--radius-box, 0.5rem);
  }
  /* A list of documents, held close as the meeting sheet's insights are: 4px
     of card round rows whose hover corners sit inside its own, under a title
     that starts where their icons do. */
  .ins-sources {
    padding: 4px;
  }
  /* A label on the card rather than a heading over it: the drawer's small
     capitals. */
  .ins-card-title {
    margin: 0;
    padding: 8px 8px 10px;
    font-size: 11px;
    font-weight: 650;
    line-height: 1.2;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .ins-sources ul {
    display: flex;
    flex-direction: column;
    gap: 0;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .ins-sources button {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
    width: 100%;
    padding: 4px 8px;
    text-align: left;
    cursor: pointer;
    background: none;
    border: 0;
    border-radius: calc(var(--radius-box, 0.5rem) - 4px);
    color: var(--color-base-content);
  }
  .ins-sources button:hover {
    background-color: color-mix(in oklch, var(--color-base-content) 8%, transparent);
  }
  .ins-source-title {
    flex: 1;
    min-width: 0;
    font-size: 0.875rem;
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .ins-source-meta {
    flex: none;
    font-size: 0.71875rem;
    font-variant-numeric: tabular-nums;
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }

  /* On the same card as the sources. */
  .ins-note {
    margin: 0;
    padding: 0.75rem;
    font-size: 0.875rem;
    line-height: 1.5;
    background-color: color-mix(in oklch, var(--color-base-content) 4%, var(--color-base-200));
    border: 1px solid color-mix(in oklch, var(--color-base-content) 9%, var(--color-base-200));
    border-radius: var(--radius-box, 0.5rem);
    color: color-mix(in oklch, var(--color-base-content) 80%, transparent);
  }
  .ins-note-error {
    background-color: color-mix(in oklch, var(--color-error) 18%, transparent);
    border-color: color-mix(in oklch, var(--color-error) 30%, transparent);
    color: var(--color-base-content);
  }
  .ins-note-error p {
    margin: 0;
  }
  .ins-note-error p + p {
    margin-top: 0.375rem;
  }
  .ins-failure-title {
    font-weight: 600;
  }
  .ins-failure-detail {
    font-size: 0.8125rem;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
    overflow-wrap: anywhere;
  }
  .ins-retry {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem;
    margin-top: 0.625rem;
    font-size: 0.8125rem;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }
  .ins-retry button {
    padding: 4px 12px;
    cursor: pointer;
    font-size: 0.8125rem;
    font-weight: 550;
    background-color: var(--color-base-100);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-field, 0.5rem);
    color: var(--color-base-content);
  }
  .ins-retry button:hover:not(:disabled) {
    background-color: color-mix(in oklch, var(--color-base-content) 8%, var(--color-base-100));
  }
  .ins-retry button:disabled {
    cursor: default;
    opacity: 0.6;
  }

  .ins-prov {
    margin-top: auto;
    padding-top: 1rem;
    border-top: 1px solid var(--color-base-300);
  }
  .ins-prov dl {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin: 0;
    font-size: 0.8125rem;
  }
  .ins-prov dl div {
    display: flex;
    gap: 0.5rem;
  }
  .ins-prov dt {
    flex: none;
    width: 5.5rem;
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  .ins-prov dd {
    margin: 0;
    min-width: 0;
    overflow-wrap: anywhere;
    color: var(--color-base-content);
  }
  .ins-prov code {
    font-family: monospace;
    font-size: 0.9em;
  }
</style>
