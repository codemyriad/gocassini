<script module lang="ts">
  // Enough hash to recognise a document by, with the whole of it a hover or a
  // copy away. A truncated hash is an identifier, not a checksum, which is why
  // the full value stays on the element it labels.
  export function shortHash(sha256: string): string {
    return sha256.length > 12 ? `${sha256.slice(0, 12)}…` : sha256;
  }

  export const QUESTION_PLACEHOLDER = "{{QUESTION}}";

  // Letters NFKD does not split into a base letter and an accent.
  const FOLDED: Record<string, string> = {
    ß: "ss", æ: "ae", œ: "oe", ø: "o", ł: "l", đ: "d", ð: "d", þ: "th", ı: "i",
  };
  const MAX_DERIVED_ID = 60;

  // The id a new template is saved under, made from its name: it becomes the
  // file name and what `cassini insight run --workflow` and the provenance
  // record call it, so people never have to choose one. Lowercase ASCII
  // letters, digits and '-', accents folded away, and a number added when the
  // result is already taken (by a custom template or a built-in). Fixed once
  // saved: renaming a template does not move its file.
  export function templateIdFromName(name: string, taken: Iterable<string>): string {
    const folded = name
      .toLowerCase()
      .replace(/[ßæœøłđðþı]/g, (letter) => FOLDED[letter] ?? "")
      .normalize("NFKD")
      .replace(/[̀-ͯ]/g, "");
    const base =
      folded
        .replace(/[^a-z0-9]+/g, "-")
        .replace(/^-+|-+$/g, "")
        .slice(0, MAX_DERIVED_ID)
        .replace(/-+$/, "") || "template";
    const used = new Set(Array.from(taken, (id) => id.toLowerCase()));
    let candidate = base;
    for (let n = 2; used.has(candidate); n++) {
      candidate = `${base}-${n}`;
    }
    return candidate;
  }

  export const TEMPLATE_PLACEHOLDER = "{{TEMPLATE}}";

  // The recorder's rule, said before Save rather than after: a custom template
  // is written in its final form, so it has no {{TEMPLATE}} splice. A prompt
  // with {{QUESTION}} in it is fine: that is how a template asks for a
  // question, built in or custom.
  export function promptProblem(prompt: string): string {
    return prompt.includes(TEMPLATE_PLACEHOLDER)
      ? `A custom template has no place for ${TEMPLATE_PLACEHOLDER}. Write the shape of the document into the prompt instead.`
      : "";
  }
</script>

<script lang="ts">
  // Insight templates: the registry, rendered (D-718), and the custom ones
  // edited (D-819).
  //
  // Two lists. "Your templates" are files on the app's volume, written by the
  // operator, which checks each save against the recorder's registry and
  // refuses one that would not run; every save is a new version, so a document
  // still names the bytes it was written with. "Built in" are compiled into
  // the image and cannot be changed here: Duplicate copies one into a new
  // template of your own, opened at the top of your list, never under the row
  // it came from, so nobody thinks they are editing the original.
  //
  // Every row opens onto the instruction it actually sends — the system prompt
  // with its template spliced in, byte for byte — and the hash beside it names
  // those bytes, which an insight document records too.
  import { createEventDispatcher, onMount, tick } from "svelte";
  import { ChevronRight, FileText, Plus, RefreshCw } from "@lucide/svelte";
  import { OperatorClient } from "./operator/client";
  import { workflowTakesQuestion } from "./insights/client";
  import NeedsProviderCard from "./NeedsProviderCard.svelte";
  import type { InsightTemplateDraft, InsightWorkflow } from "./operator/types";

  const dispatch = createEventDispatcher<{ openProviders: void }>();

  // Handed in by Settings.svelte, exactly as the two sibling panels are: it
  // already builds the client and already renders the config error, so a second
  // one here would be a second place that decision could be made differently.
  export let operatorClient: OperatorClient | null = null;

  let client: OperatorClient | null = operatorClient;

  // Three states that must not be confused for each other: still loading, the
  // fetch failed, and this deployment ships no templates. Rendering an empty
  // list for the second would send an administrator looking for a feature that
  // is there.
  let workflows: InsightWorkflow[] = [];
  let loading = true;
  let loadError = "";

  // A template on its own does nothing: it is the starting point for a request
  // to a model, so the list reads as inert until there is one to send it to.
  // Three states again — null is "the question was never answered", and a panel
  // that could not read the AI settings must not accuse a configured
  // deployment of having no endpoint.
  let hasProvider: boolean | null = null;

  // Collapsed when there is something of your own to look at first; decided
  // once, on the first listing, so a save never folds it under the reader.
  let builtInOpen = true;
  let builtInDecided = false;

  // The one template being edited. `id` is null for a template with no file
  // yet: its id is derived from the name when it is first saved, and fixed
  // after that, because documents and settings name it by the id.
  type Draft = {
    name: string;
    description: string;
    prompt: string;
  };
  type Editor = { key: string; id: string | null; from: string; draft: Draft };
  const NEW_KEY = "new";
  let editor: Editor | null = null;
  let editorEl: HTMLFormElement | null = null;
  let saving = false;
  let saveError = "";
  let pendingDelete = "";
  let deleteError = "";

  const PROMPT_EXAMPLE = [
    "You read meeting transcripts and list the risks people raised.",
    "",
    "Write a Markdown document with one heading, ## Risks, and under it one bullet per risk:",
    "what the risk is, who raised it, and what was agreed about it.",
    "If nobody raised a risk, say so in one line.",
  ].join("\n");

  onMount(() => {
    void load();
  });

  async function load() {
    if (!client) {
      loading = false;
      return;
    }
    loading = true;
    loadError = "";
    try {
      workflows = await client.listInsightWorkflows();
      if (!builtInDecided) {
        builtInOpen = !workflows.some(isCustom);
        builtInDecided = true;
      }
    } catch (error) {
      loadError = error instanceof Error ? error.message : String(error);
    } finally {
      loading = false;
    }
    void loadProviders();
  }

  async function loadProviders() {
    if (!client) {
      return;
    }
    try {
      hasProvider = (await client.getLLMSettings()).providers.length > 0;
    } catch {
      // Swallowed: the templates are the page, and an unreadable AI setting
      // costs the locked notice and nothing else.
      hasProvider = null;
    }
  }

  function rowKey(workflow: InsightWorkflow): string {
    return `${workflow.origin}:${workflow.id}`;
  }

  function isCustom(workflow: InsightWorkflow): boolean {
    return workflow.origin !== "Built in";
  }

  $: customs = workflows.filter(isCustom);
  $: builtIns = workflows.filter((workflow) => !isCustom(workflow));

  function draftOf(workflow: InsightWorkflow, name: string): Draft {
    return { name, description: workflow.description, prompt: workflow.instruction };
  }

  function newTemplate(): Editor {
    return {
      key: NEW_KEY,
      id: null,
      from: "",
      draft: { name: "", description: "", prompt: "" },
    };
  }

  function duplicate(workflow: InsightWorkflow): Editor {
    return { key: NEW_KEY, id: null, from: workflow.name, draft: draftOf(workflow, `${workflow.name} (copy)`) };
  }

  function editExisting(workflow: InsightWorkflow): Editor {
    return { key: rowKey(workflow), id: workflow.id, from: "", draft: draftOf(workflow, workflow.name) };
  }

  async function openEditor(next: Editor) {
    editor = next;
    saveError = "";
    pendingDelete = "";
    await tick();
    editorEl?.scrollIntoView?.({ block: "nearest", behavior: "smooth" });
    editorEl?.querySelector<HTMLInputElement>("input")?.focus({ preventScroll: true });
  }

  $: editorProblem = editor ? promptProblem(editor.draft.prompt) : "";
  $: editorFilled = editor !== null && editor.draft.name.trim() !== "" && editor.draft.prompt.trim() !== "";

  function toRequest(draft: Draft): InsightTemplateDraft {
    return { name: draft.name, description: draft.description, instruction: draft.prompt };
  }

  async function save() {
    if (!client || !editor || !editorFilled) {
      return;
    }
    if (promptProblem(editor.draft.prompt)) {
      return;
    }
    const id =
      editor.id ??
      templateIdFromName(
        editor.draft.name,
        workflows.map((workflow) => workflow.id),
      );
    saving = true;
    saveError = "";
    try {
      await client.saveInsightTemplate(id, toRequest(editor.draft));
      editor = null;
      await load();
    } catch (error) {
      saveError = error instanceof Error ? error.message : String(error);
    } finally {
      saving = false;
    }
  }

  async function remove(workflow: InsightWorkflow) {
    if (!client) {
      return;
    }
    saving = true;
    deleteError = "";
    try {
      await client.deleteInsightTemplate(workflow.id);
      pendingDelete = "";
      if (editor?.id === workflow.id) {
        editor = null;
      }
      await load();
    } catch (error) {
      deleteError = error instanceof Error ? error.message : String(error);
    } finally {
      saving = false;
    }
  }

  function askDelete(workflow: InsightWorkflow) {
    pendingDelete = rowKey(workflow);
    deleteError = "";
  }
</script>

<header class="op-panel-head">
  <div>
    <div class="op-panel-title">
      <h1>Insight templates</h1>
    </div>
    <p>Used for insights and meeting summaries. Write your own, or duplicate a built-in one and change it.</p>
  </div>
  <div class="op-panel-actions">
    <button
      class="icon-btn"
      type="button"
      on:click={load}
      disabled={loading || !client}
      aria-label="Reload insight templates"
    >
      <RefreshCw size={15} aria-hidden="true" />
    </button>
  </div>
</header>

{#snippet templateEditor()}
  {#if editor}
    <form
      class="tpl-editor"
      class:tpl-editor-new={editor.id === null}
      bind:this={editorEl}
      on:submit|preventDefault={save}
      aria-label={editor.id === null ? "New insight template" : "Edit insight template"}
      novalidate
    >
      {#if editor.id === null}
        <p class="tpl-editor-title">
          New template{#if editor.from}<span class="tpl-hint"> · a copy of {editor.from}</span>{/if}
        </p>
      {/if}
      <label class="op-field">
        <span class="op-field-label">Name</span>
        <input class="op-input" type="text" bind:value={editor.draft.name} maxlength="500" autocomplete="off" />
        <span class="tpl-hint">What people pick it by.</span>
      </label>
      <label class="op-field">
        <span class="op-field-label">Description</span>
        <input class="op-input" type="text" bind:value={editor.draft.description} maxlength="500" autocomplete="off" />
        <span class="tpl-hint">Shown to the person choosing it, so they know what it does.</span>
      </label>
      <label class="op-field">
        <span class="op-field-label">Prompt</span>
        <textarea
          class="op-input tpl-textarea"
          bind:value={editor.draft.prompt}
          rows="16"
          spellcheck="false"
          placeholder={PROMPT_EXAMPLE}
        ></textarea>
        <span class="tpl-hint">The instruction the model receives, together with the transcripts.</span>
        <span class="tpl-hint">
          Put <code>{QUESTION_PLACEHOLDER}</code> where a question should go, and people will be asked for one when they
          pick this template.
        </span>
        {#if editorProblem}
          <span class="tpl-hint tpl-hint-bad" role="alert">{editorProblem}</span>
        {/if}
      </label>
      {#if editor.id !== null}
        <!-- Not editable: the file name is what `cassini insight run
             --workflow` and every recorded insight name it by. -->
        <p class="tpl-file">File: <code>{editor.id}.md</code></p>
      {/if}
      {#if saveError}
        <div class="err-box" role="alert">{saveError}</div>
      {/if}
      <div class="tpl-actions">
        <button class="op-btn" type="submit" disabled={!editorFilled || saving}>
          {saving ? "Saving…" : "Save"}
        </button>
        <button class="link-btn op-cancel" type="button" disabled={saving} on:click={() => (editor = null)}>
          Cancel
        </button>
        {#if editor.id !== null}
          {@const editing = customs.find((workflow) => workflow.id === editor?.id)}
          {#if editing}
            <button
              class="link-btn danger tpl-push"
              type="button"
              disabled={saving}
              on:click={() => askDelete(editing)}
            >
              Delete
            </button>
          {/if}
        {/if}
      </div>
    </form>
  {/if}
{/snippet}

{#snippet templateRow(workflow: InsightWorkflow, custom: boolean)}
  <article class="op-tint tpl-card" class:off={hasProvider === false || workflow.error !== ""}>
    <div class="set-row-main">
      <h3 class="set-row-name">{workflow.name || workflow.id}</h3>
      {#if workflow.description}
        <p class="set-row-sub">{workflow.description}</p>
      {/if}
    </div>

    <div class="tpl-row-actions">
      {#if custom}
        <button class="link-btn" type="button" disabled={saving} on:click={() => void openEditor(editExisting(workflow))}>
          Edit
        </button>
      {/if}
      {#if !workflow.error}
        <button class="link-btn" type="button" disabled={saving} on:click={() => void openEditor(duplicate(workflow))}>
          Duplicate
        </button>
      {/if}
      {#if custom}
        <button
          class="link-btn danger"
          type="button"
          disabled={saving}
          aria-expanded={pendingDelete === rowKey(workflow)}
          on:click={() => askDelete(workflow)}
        >
          Delete
        </button>
      {/if}
    </div>

    {#if workflow.error}
      <!-- A custom file that will not run is shown with the reason, never
           dropped: the administrator who added it needs to know why it is
           not on offer. -->
      <p class="tpl-error" role="note">
        <code>{workflow.id}.md</code> cannot run: {workflow.error}
      </p>
    {:else}
      <!-- Name, description and prompt: the same three parts for a built-in
           and a custom template. Whether it asks for a question is read off
           the prompt's {{QUESTION}} slot, the same bytes the operator reads. -->
      <details class="tpl-prompt">
        <summary class="tpl-toggle">
          <span class="tpl-chev" aria-hidden="true"></span>
          Prompt{#if workflowTakesQuestion(workflow)}<span class="tpl-hint"> · asks for a question</span>{/if}
        </summary>
        <div class="tpl-body">
          <pre class="tpl-instruction">{workflow.instruction}</pre>
          <p class="tpl-ident">
            <code>{workflow.id}</code>
            <code>{workflow.version}</code>
            <code title={workflow.sha256}>{shortHash(workflow.sha256)}</code>
          </p>
        </div>
      </details>
    {/if}

    {#if pendingDelete === rowKey(workflow)}
      <!-- Inline, like the provider list's: the app runs inside a shadow
           root, where a top-layer <dialog> does not reliably follow its
           styling or focus. -->
      <div class="tpl-confirm" role="alertdialog" aria-label="Confirm deleting this template">
        <p>
          Delete <strong>{workflow.name || workflow.id}</strong>? Insights already written with it keep their text
          and still name its id, version and hash.
        </p>
        {#if deleteError}
          <div class="err-box" role="alert">{deleteError}</div>
        {/if}
        <div class="tpl-actions">
          <button class="btn btn-sm btn-error" type="button" disabled={saving} on:click={() => void remove(workflow)}>
            Yes, delete it
          </button>
          <button class="link-btn op-cancel" type="button" disabled={saving} on:click={() => (pendingDelete = "")}>
            Cancel
          </button>
        </div>
      </div>
    {/if}

    {#if custom && editor && editor.key === rowKey(workflow)}
      {@render templateEditor()}
    {/if}
  </article>
{/snippet}

{#if hasProvider === false}
  <NeedsProviderCard
    title="Add a provider to use these templates"
    on:open={() => dispatch("openProviders")}
  />
{/if}
{#if loadError}
  <div class="err-box" role="alert">Templates could not be listed.</div>
{:else if loading && workflows.length === 0}
  <p class="op-state">Loading insight templates…</p>
{:else if workflows.length === 0}
  <div class="op-empty">
    <FileText size={20} aria-hidden="true" />
    <p class="t">This build ships no templates.</p>
  </div>
{:else}
  <!-- Dimmed rather than hidden while there is no endpoint: the templates
       are real and worth reading, they just have nowhere to be sent. -->
  <section class="tpl-section" aria-labelledby="tpl-custom-heading">
    <div class="tpl-section-head">
      <h2 id="tpl-custom-heading" class="tpl-section-title">Your templates</h2>
      <button
        class="link-btn tpl-new"
        type="button"
        disabled={saving || editor?.key === NEW_KEY}
        on:click={() => void openEditor(newTemplate())}
      >
        <Plus size={14} aria-hidden="true" />
        New template
      </button>
    </div>
    {#if editor && editor.key === NEW_KEY}
      <div class="op-tint tpl-card">
        {@render templateEditor()}
      </div>
    {/if}
    {#if customs.length === 0 && editor?.key !== NEW_KEY}
      <p class="tpl-none">None yet. Start a new one, or duplicate a built-in template below.</p>
    {/if}
    <!-- Keyed by origin as well as id: a custom file named after a built-in is
         listed, broken, and the two must not collide. -->
    {#each customs as workflow (`${workflow.origin}:${workflow.id}`)}
      {@render templateRow(workflow, true)}
    {/each}
  </section>

  {#if builtIns.length > 0}
    <section class="tpl-section" aria-labelledby="tpl-builtin-heading">
      <div class="tpl-section-head">
        <h2 class="tpl-section-title">
          <button
            id="tpl-builtin-heading"
            class="tpl-fold"
            type="button"
            aria-expanded={builtInOpen}
            aria-controls="tpl-builtin-list"
            on:click={() => (builtInOpen = !builtInOpen)}
          >
            <span class="tpl-fold-chev" class:open={builtInOpen} aria-hidden="true"><ChevronRight size={14} /></span>
            Built in
            <span class="tpl-count">{builtIns.length}</span>
          </button>
        </h2>
      </div>
      <p class="tpl-note">
        Shipped with this version and recorded by hash in every document they produce. Duplicate one to change it.
      </p>
      {#if builtInOpen}
        <div id="tpl-builtin-list" class="tpl-list">
          {#each builtIns as workflow (`${workflow.origin}:${workflow.id}`)}
            {@render templateRow(workflow, false)}
          {/each}
        </div>
      {/if}
    </section>
  {/if}
{/if}

<style>
  .tpl-list {
    display: grid;
    gap: 12px;
  }
  .tpl-card {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-start;
    gap: 0 14px;
    padding: 14px 16px;
  }
  .tpl-prompt {
    flex-basis: 100%;
    min-width: 0;
    margin-top: 8px;
  }
  .tpl-body {
    margin-top: 9px;
  }
  .tpl-ident {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin: 10px 0 0;
  }
  .tpl-ident code {
    padding: 2px 6px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    font-weight: 500;
    line-height: 1.5;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
    background-color: var(--op-code-bg);
    border: 1px solid var(--op-code-border);
    border-radius: var(--radius-selector, 0.25rem);
  }
  .tpl-instruction {
    max-height: 24rem;
    margin: 0;
    padding: 8px 12px 8px 11px;
    background-color: var(--op-inset);
    border-left: 2px solid var(--color-base-300);
    border-radius: 0 var(--radius-field, 0.5rem) var(--radius-field, 0.5rem) 0;
    font-size: 12.5px;
    line-height: 1.6;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
    overflow: auto;
    font-family: inherit;
    white-space: pre-wrap;
  }
  .tpl-row-actions {
    display: flex;
    flex: none;
    gap: 12px;
    margin-left: auto;
  }
  .tpl-editor,
  .tpl-confirm {
    display: grid;
    flex-basis: 100%;
    gap: 12px;
    margin-top: 12px;
    padding-top: 12px;
    border-top: 1px solid var(--color-base-300);
  }
  .tpl-confirm p {
    margin: 0;
    font-size: 13px;
  }
  .tpl-textarea {
    min-height: 16rem;
    font-family: var(--font-mono);
    font-size: 12.5px;
    line-height: 1.5;
    resize: vertical;
  }
  .tpl-hint {
    font-size: 12px;
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }
  .tpl-hint-bad {
    color: var(--color-error);
  }
  .tpl-actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 12px;
  }
  .tpl-error {
    flex-basis: 100%;
    margin: 8px 0 0;
    font-size: 12.5px;
    color: var(--color-error);
  }
  .tpl-error code {
    font-family: var(--font-mono);
    font-size: 11.5px;
  }
  .tpl-section {
    display: grid;
    gap: 12px;
  }
  .tpl-section + .tpl-section {
    margin-top: 28px;
  }
  .tpl-section-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }
  .tpl-section-title {
    margin: 0;
    font-size: 14px;
    font-weight: 600;
  }
  .tpl-new {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-weight: 550;
  }
  .tpl-fold {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 0;
    cursor: pointer;
    background: none;
    border: 0;
    font: inherit;
    color: inherit;
  }
  .tpl-fold-chev {
    display: inline-flex;
    transition: transform 120ms ease;
  }
  .tpl-fold-chev.open {
    transform: rotate(90deg);
  }
  .tpl-count {
    font-size: 12px;
    font-weight: 500;
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  .tpl-note,
  .tpl-none {
    margin: -4px 0 0;
    font-size: 12.5px;
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }
  .tpl-editor-new {
    margin-top: 0;
    padding-top: 0;
    border-top: 0;
  }
  .tpl-editor-title {
    margin: 0;
    font-size: 13.5px;
    font-weight: 600;
  }
  .tpl-file {
    margin: 0;
    font-size: 12px;
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  .tpl-file code {
    font-family: var(--font-mono);
    font-size: 11.5px;
  }
  .tpl-push {
    margin-left: auto;
  }
</style>
