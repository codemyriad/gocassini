<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { marked } from "marked";
  import DOMPurify from "dompurify";
  import { FileText, X } from "@lucide/svelte";
  import { formatMeetingDateShort, type MeetingCatalogEntry } from "../viewer/catalog";
  import { roomLabelOf } from "../viewer/rooms";
  import {
    formatInsightCreated,
    formatInsightStatus,
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

  const dispatch = createEventDispatcher<{
    close: void;
    openSource: MeetingCatalogEntry;
  }>();

  $: question = insight.question?.trim() ?? "";
  $: pending = insight.status === "queued" || insight.status === "running";
  $: failed = insight.status === "failed";

  function renderDocumentHtml(markdown: string): string {
    if (markdown.trim() === "") {
      return "";
    }
    // Same two steps MeetingView renders a sealed summary with: the document is
    // model output, so it is parsed as markdown and then sanitised, never
    // trusted into the DOM as it arrived.
    const rawHtml = marked.parse(markdown, { async: false }) as string;
    return DOMPurify.sanitize(rawHtml, { USE_PROFILES: { html: true } });
  }

  $: documentHtml = renderDocumentHtml(documentMarkdown);
</script>

<section class="ins-doc" aria-label="Insight">
  <header class="ins-head">
    <h2>Insight</h2>
    <button type="button" on:click={() => dispatch("close")} aria-label="Close the insight">
      <X size={16} aria-hidden="true" />
    </button>
  </header>

  <div class="ins-body">
    <!-- 1. The question the panel exists to answer, so it reads as the heading
         of the brief rather than as a caption on it. A workflow can be run with
         no question of its own, and then there is no quote to show. -->
    {#if question}
      <p class="ins-question">“{question}”</p>
    {:else}
      <p class="ins-question ins-question-none">
        Ran the <code>{insight.workflowId}</code> workflow, with no question of its own.
      </p>
    {/if}

    <!-- 2. The material. -->
    {#if sources.length > 0}
      <section class="ins-sources">
        <h3 class="ins-eyebrow">
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
                  {formatMeetingDateShort(source.dateLabel)} · {roomLabelOf(source)}
                </span>
              </button>
            </li>
          {/each}
        </ul>
      </section>
    {/if}

    <!-- 3. The answer, or the honest reason there is not one yet. -->
    {#if pending}
      <p class="ins-note" role="status">
        This run is {formatInsightStatus(insight.status).toLowerCase()}. The answer appears
        here when it finishes — the list keeps checking.
      </p>
    {:else if failed}
      <p class="ins-note ins-note-error" role="status">
        This run failed{insight.error ? `: ${insight.error}` : "."}
      </p>
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
      <p class="ins-note" role="status">
        This run succeeded but its document is empty.
      </p>
    {/if}

    <!-- 4. Provenance. The prototype showed four of the record's fields and
         invented the most important one; a re-run is append-only, so what ran,
         who asked for it and how it ended have to be on the document itself or
         two attempts are indistinguishable. -->
    <section class="ins-prov">
      <h3 class="ins-eyebrow">This run</h3>
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
          <dt>Created</dt>
          <dd>{formatInsightCreated(insight)}</dd>
        </div>
        <div>
          <dt>Workflow</dt>
          <dd><code>{insight.workflowId}</code> {insight.workflowVersion}</dd>
        </div>
        {#if insight.model}
          <div>
            <dt>Model</dt>
            <dd>
              <code>{insight.model}</code>{insight.provider ? ` · ${insight.provider}` : ""}
            </dd>
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
    background-color: var(--color-base-100);
    /* The insight reads on the same surface its cards use elsewhere, so the
       panel itself says which of the two kinds of thing the sheet is holding. */
    border-left: 4px solid var(--color-secondary);
  }

  .ins-head {
    display: flex;
    flex: none;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
    padding: 1rem 1.25rem;
    border-bottom: 1px solid var(--color-base-300);
  }
  .ins-head h2 {
    font-size: 0.75rem;
    font-weight: 650;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: color-mix(in oklch, var(--color-secondary) 80%, var(--color-base-content));
  }
  .ins-head button {
    display: inline-flex;
    flex: none;
    padding: 4px;
    cursor: pointer;
    background: none;
    border: 0;
    border-radius: var(--radius-field, 0.5rem);
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .ins-head button:hover {
    background-color: var(--color-base-200);
    color: var(--color-base-content);
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
  .ins-question-none {
    font-size: 0.9375rem;
    font-weight: 450;
    color: color-mix(in oklch, var(--color-base-content) 75%, transparent);
  }

  .ins-eyebrow {
    margin: 0 0 0.5rem;
    font-size: 11px;
    font-weight: 650;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }

  /* Lifted off the insight's own panel, because this is the material rather
     than the writing. */
  .ins-sources {
    padding: 0.75rem;
    background-color: var(--color-base-200);
    border-radius: var(--radius-box, 0.75rem);
  }
  .ins-sources ul {
    display: flex;
    flex-direction: column;
    gap: 2px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .ins-sources button {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
    width: 100%;
    padding: 6px 8px;
    text-align: left;
    cursor: pointer;
    background: none;
    border: 0;
    border-radius: var(--radius-field, 0.5rem);
    color: var(--color-base-content);
  }
  .ins-sources button:hover {
    background-color: var(--color-base-100);
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

  .ins-note {
    margin: 0;
    padding: 0.75rem;
    font-size: 0.875rem;
    line-height: 1.5;
    background-color: var(--color-base-200);
    border-radius: var(--radius-box, 0.75rem);
    color: color-mix(in oklch, var(--color-base-content) 80%, transparent);
  }
  .ins-note-error {
    background-color: color-mix(in oklch, var(--color-error) 18%, transparent);
    color: var(--color-base-content);
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
