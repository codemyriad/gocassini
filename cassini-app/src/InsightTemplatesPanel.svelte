<script module lang="ts">
  // Enough hash to recognise a document by, with the whole of it a hover or a
  // copy away. A truncated hash is an identifier, not a checksum, which is why
  // the full value stays on the element it labels.
  export function shortHash(sha256: string): string {
    return sha256.length > 12 ? `${sha256.slice(0, 12)}…` : sha256;
  }

  // Letters NFKD does not split into a base letter and an accent.
  const FOLDED: Record<string, string> = {
    ß: "ss", æ: "ae", œ: "oe", ø: "o", ł: "l", đ: "d", ð: "d", þ: "th", ı: "i",
  };
  const MAX_DERIVED_ID = 60;

  // The id a new template is saved under, made from its name: it becomes the
  // file name and what `cassini insight run --workflow` and the provenance
  // record call it, so people never have to choose one. Lowercase ASCII
  // letters, digits and '-', accents folded away, and a number added when the
  // result is already taken (by a custom template or a built-in). A name with
  // nothing left after folding (Greek, Chinese, only punctuation) gets "t-"
  // and a hash of the name, so two such names get two ids rather than
  // "template" and "template-2". Fixed once saved: renaming a template does
  // not move its file.
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
        .replace(/-+$/, "") || `t-${fnv1a32Hex(name.trim())}`;
    const used = new Set(Array.from(taken, (id) => id.toLowerCase()));
    let candidate = base;
    for (let n = 2; used.has(candidate); n++) {
      candidate = `${base}-${n}`;
    }
    return candidate;
  }

  // FNV-1a, 32 bits, over the UTF-8 bytes, as 8 hex digits: a stable short
  // id from a name, not a security property.
  function fnv1a32Hex(text: string): string {
    let hash = 0x811c9dc5;
    for (const byte of new TextEncoder().encode(text)) {
      hash = Math.imul(hash ^ byte, 0x01000193) >>> 0;
    }
    return hash.toString(16).padStart(8, "0");
  }

  // The operator's rule for a template id (llmTemplateIDPattern): a file
  // whose stem breaks it is listed broken under that stem, and can be removed
  // but never saved under it.
  const USABLE_ID = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;
  export function usableTemplateId(id: string): boolean {
    return USABLE_ID.test(id) && Array.from(id).length <= 200;
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
  import { FileText, Plus, RefreshCw } from "@lucide/svelte";
  import { OperatorClient, OperatorHttpError } from "./operator/client";
  import { QUESTION_PLACEHOLDER, workflowTakesQuestion } from "./insights/client";
  import NeedsProviderCard from "./NeedsProviderCard.svelte";
  import type { InsightTemplateDraft, InsightWorkflow } from "./operator/types";

  // openPipeline: the step a refused delete names, opened where it is set.
  const dispatch = createEventDispatcher<{ openProviders: void; openPipeline: "summary" }>();

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

  // The one template being edited. `id` is null for a template with no file
  // yet: its id is derived from the name when it is first saved, and fixed
  // after that, because documents and settings name it by the id.
  type Draft = {
    name: string;
    description: string;
    prompt: string;
  };
  // `replaces` is a broken file being repaired under a new id, because it
  // cannot be saved under its own: its stem is not a usable id, or it shares a
  // built-in's. Saved as a new template, after which that file is removed.
  type Editor = { key: string; id: string | null; from: string; draft: Draft; replaces?: string };
  const NEW_KEY = "new";
  let editor: Editor | null = null;
  let editorEl: HTMLFormElement | null = null;
  let saving = false;
  let saveError = "";

  // The one template being deleted, asked about in a modal dialog. `inUse`
  // names the step configured to run it: the operator refuses to delete such
  // a template, so the dialog says so before anyone presses Delete and offers
  // the way to replace it instead. `error` is any other failure, which leaves
  // Delete there to try again.
  type Step = "summary" | "insight";
  type Deletion = { workflow: InsightWorkflow; error: string; inUse: Step | "" };
  let deletion: Deletion | null = null;
  let deleteDialog: HTMLDialogElement | null = null;
  let deleteCancel: HTMLButtonElement | null = null;

  // The templates the summary and insight steps are set to run, as last read.
  let stepTemplates: Record<Step, string> = { summary: "", insight: "" };

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
      const settings = await client.getLLMSettings();
      hasProvider = settings.providers.length > 0;
      stepTemplates = { summary: settings.summary.template.trim(), insight: settings.insight.template.trim() };
    } catch (error) {
      // Not fatal: the templates are the page, and an unreadable AI setting
      // costs the locked notice and nothing else. Logged so it can be found.
      console.warn("Insight templates: the AI settings could not be read", error);
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

  function shadowsBuiltIn(workflow: InsightWorkflow): boolean {
    return isCustom(workflow) && builtIns.some((builtIn) => builtIn.id === workflow.id);
  }

  function editExisting(workflow: InsightWorkflow): Editor {
    const draft = draftOf(workflow, workflow.name);
    if (!usableTemplateId(workflow.id) || shadowsBuiltIn(workflow)) {
      return { key: rowKey(workflow), id: null, from: "", draft, replaces: workflow.id };
    }
    return { key: rowKey(workflow), id: workflow.id, from: "", draft };
  }

  async function openEditor(next: Editor) {
    editor = next;
    saveError = "";
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
    const current = editor;
    // A new template's id comes from the list last loaded, which may be stale
    // (another administrator saved one since) or empty (it failed to load).
    // So a new template is saved create-only: the operator answers 412 rather
    // than overwrite one already there, and the id is derived once more from
    // a fresh list.
    const creating = current.id === null;
    let taken = workflows.map((workflow) => workflow.id);
    saving = true;
    saveError = "";
    try {
      for (let attempt = 0; ; attempt++) {
        const id = current.id ?? templateIdFromName(current.draft.name, taken);
        try {
          await client.saveInsightTemplate(id, toRequest(current.draft), { createOnly: creating });
          break;
        } catch (error) {
          if (!creating || attempt > 0 || !(error instanceof OperatorHttpError) || error.status !== 412) {
            throw error;
          }
          const fresh = await client.listInsightWorkflows();
          taken = [...taken, id, ...fresh.map((workflow) => workflow.id)];
        }
      }
      editor = null;
      if (current.replaces !== undefined) {
        // The repaired copy is saved; the broken file it came from goes.
        try {
          await client.deleteInsightTemplate(current.replaces);
        } catch (error) {
          const message = error instanceof Error ? error.message : String(error);
          saveError = `Saved as a new template, but ${current.replaces}.md could not be removed: ${message}`;
        }
      }
      await load();
    } catch (error) {
      saveError = error instanceof Error ? error.message : String(error);
    } finally {
      saving = false;
    }
  }

  async function askDelete(workflow: InsightWorkflow) {
    // A custom file sharing a built-in's id never runs; the built-in answers
    // to the id whether or not the file is there, so no step uses the file.
    deletion = { workflow, error: "", inUse: shadowsBuiltIn(workflow) ? "" : await stepUsing(workflow.id) };
    await tick();
    // showModal is absent only where there is no real browser (jsdom).
    if (deleteDialog && !deleteDialog.open) {
      deleteDialog.showModal?.();
    }
    // Cancel, not Delete: a stray Enter keeps the template.
    deleteCancel?.focus();
  }

  // Which step runs this template, read fresh from the AI settings, or from
  // the last reading when they cannot be read now.
  async function stepUsing(id: string): Promise<Step | ""> {
    try {
      const settings = await client!.getLLMSettings();
      stepTemplates = { summary: settings.summary.template.trim(), insight: settings.insight.template.trim() };
    } catch (error) {
      // Keep the last reading; the operator still refuses, and says why.
      console.warn("Insight templates: the AI settings could not be read; using the last reading", error);
    }
    return stepTemplates.summary === id ? "summary" : stepTemplates.insight === id ? "insight" : "";
  }

  function closeDelete() {
    if (deleteDialog?.open) {
      deleteDialog.close();
    }
    deletion = null;
  }

  // Escape: closed like Cancel, except while the
  // delete is on its way. Handled here rather than left to the browser: inside
  // the shadow root on Nextcloud's page, a modal dialog's own Escape handling
  // never fires, so the dialog would stay open. Not skipped for an event
  // something else has already prevented, which may be why the browser's
  // handling does not run there; closing clears `deletion`, so the window's
  // fallback after the dialog's own handler does nothing twice.
  function escapeDelete(event: KeyboardEvent) {
    if (event.key !== "Escape" || !deletion) {
      return;
    }
    event.preventDefault();
    if (!saving) {
      closeDelete();
    }
  }

  // Where the browser's own Escape does work, it is the same path.
  function cancelDelete(event: Event) {
    event.preventDefault();
    if (!saving) {
      closeDelete();
    }
  }

  // A click on the dialog itself, not on its box, is a click on the backdrop.
  function backdropClick(event: MouseEvent) {
    if (event.target === deleteDialog && !saving) {
      closeDelete();
    }
  }

  async function remove() {
    if (!client || !deletion) {
      return;
    }
    const { workflow } = deletion;
    saving = true;
    deletion = { ...deletion, error: "", inUse: "" };
    try {
      await client.deleteInsightTemplate(workflow.id);
      closeDelete();
      if (editor?.id === workflow.id) {
        editor = null;
      }
      await load();
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      if (error instanceof OperatorHttpError && error.status === 409) {
        // A step was set to it after the dialog opened: the same in-use
        // state. The operator names the step in a field of its own, never
        // read out of its sentence, which quotes the id; the settings are
        // the fallback for an operator that does not name it.
        const named = error.details.step;
        const step: Step =
          named === "summary" || named === "insight" ? named : (await stepUsing(workflow.id)) || "insight";
        deletion = { workflow, error: "", inUse: step };
        await tick();
        deleteCancel?.focus();
      } else {
        deletion = { workflow, error: message, inUse: "" };
      }
    } finally {
      saving = false;
    }
  }

  function openPipeline() {
    closeDelete();
    dispatch("openPipeline", "summary");
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
      <!-- Hints sit between a label and its field, read before typing, and
           describe the field rather than name it. One editor is open at a
           time, so the ids are fixed. -->
      <div class="op-field">
        <label class="op-field-label" for="tpl-name">Name</label>
        <input id="tpl-name" class="op-input" type="text" bind:value={editor.draft.name} maxlength="500" autocomplete="off" />
      </div>
      <div class="op-field">
        <label class="op-field-label" for="tpl-description">Description</label>
        <span id="tpl-description-hint" class="tpl-hint">Shown to the person choosing it, so they know what it does.</span>
        <input
          id="tpl-description"
          class="op-input"
          type="text"
          bind:value={editor.draft.description}
          maxlength="500"
          autocomplete="off"
          aria-describedby="tpl-description-hint"
        />
      </div>
      <div class="op-field">
        <label class="op-field-label" for="tpl-prompt">Prompt</label>
        <span id="tpl-prompt-hint" class="tpl-hint">
          The instruction the model receives, together with the transcripts.<br />
          Put <code>{QUESTION_PLACEHOLDER}</code> where a question should go, and people will be asked for one when they
          pick this template.
        </span>
        <textarea
          id="tpl-prompt"
          class="op-input tpl-textarea"
          bind:value={editor.draft.prompt}
          rows="16"
          spellcheck="false"
          placeholder={PROMPT_EXAMPLE}
          aria-describedby="tpl-prompt-hint"
        ></textarea>
        {#if editorProblem}
          <span class="tpl-hint tpl-hint-bad" role="alert">{editorProblem}</span>
        {/if}
      </div>
      {#if editor.id !== null}
        <!-- Not editable: the file name is what `cassini insight run
             --workflow` and every recorded insight name it by. -->
        <p class="tpl-file">File: <code>{editor.id}.md</code></p>
      {:else if editor.replaces !== undefined}
        <p class="tpl-file">
          <code>{editor.replaces}.md</code> cannot be saved under its own name. Saving keeps this as a new template and
          removes that file.
        </p>
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
              aria-haspopup="dialog"
              on:click={() => void askDelete(editing)}
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
  <!-- One compact card: name and description on a line with the actions,
       the prompt one small line under it. -->
  <article class="op-tint tpl-card" class:off={hasProvider === false || workflow.error !== ""}>
    <div class="tpl-row-head">
      <div class="tpl-row-text">
        <h3 class="tpl-row-name">{workflow.name || workflow.id}</h3>
        {#if workflow.description}
          <p class="tpl-row-desc">{workflow.description}</p>
        {/if}
      </div>

      <div class="tpl-row-actions">
        {#if custom}
          <button class="link-btn" type="button" disabled={saving} on:click={() => void openEditor(editExisting(workflow))}>
            Edit
          </button>
        {/if}
        {#if !workflow.error}
          {#if custom}<span class="tpl-sep" aria-hidden="true">·</span>{/if}
          <button class="link-btn" type="button" disabled={saving} on:click={() => void openEditor(duplicate(workflow))}>
            Duplicate
          </button>
        {/if}
        {#if custom}
          <span class="tpl-sep" aria-hidden="true">·</span>
          <button
            class="link-btn danger"
            type="button"
            disabled={saving}
            aria-haspopup="dialog"
            on:click={() => void askDelete(workflow)}
          >
            Delete
          </button>
        {/if}
      </div>
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
    {#if customs.length > 0}
      <div class="tpl-list">
        {#each customs as workflow (`${workflow.origin}:${workflow.id}`)}
          {@render templateRow(workflow, true)}
        {/each}
      </div>
    {/if}
  </section>

  {#if builtIns.length > 0}
    <section class="tpl-section" aria-labelledby="tpl-builtin-heading">
      <div class="tpl-section-head">
        <h2 id="tpl-builtin-heading" class="tpl-section-title">
          Built in <span class="tpl-count">{builtIns.length}</span>
        </h2>
      </div>
      <p class="tpl-note">Shipped with Cassini. Duplicate one to change it.</p>
      <div class="tpl-list">
        {#each builtIns as workflow (`${workflow.origin}:${workflow.id}`)}
          {@render templateRow(workflow, false)}
        {/each}
      </div>
    </section>
  {/if}
{/if}

<!-- A modal dialog for the one irreversible thing on this panel. It stays a
     descendant of the panel in the DOM, so the panel's styles and theme still
     reach it from the top layer. Escape and the backdrop cancel it. -->
<!-- The fallback for an Escape that does not start inside the dialog. -->
<svelte:window on:keydown={(event) => deletion && escapeDelete(event)} />

<dialog
  class="modal"
  bind:this={deleteDialog}
  aria-labelledby="tpl-delete-title"
  on:close={() => (deletion = null)}
  on:cancel={cancelDelete}
  on:keydown={escapeDelete}
  on:click={backdropClick}
>
  {#if deletion}
    {@const name = deletion.workflow.name || deletion.workflow.id}
    <div class="modal-box tpl-dialog">
      <h2 id="tpl-delete-title" class="tpl-dialog-title">Delete <strong>{name}</strong>?</h2>
      {#if deletion.inUse}
        <!-- A step runs this template, so the operator would refuse: no
             Delete to press, only the way to replace it. The insight
             default is not set anywhere in the app, so that case has no
             place to send anyone and closes with OK. -->
        <p class="tpl-dialog-body">
          {#if deletion.inUse === "summary"}
            This template is being used for generating summaries after calls are transcribed. Replace it with another
            template before deleting it.
          {:else}
            This template is being used as the default for new insights. Replace it with another template before
            deleting it.
          {/if}
        </p>
        <div class="tpl-dialog-actions">
          <button class="link-btn op-cancel" type="button" bind:this={deleteCancel} on:click={closeDelete}>Cancel</button>
          {#if deletion.inUse === "summary"}
            <button class="op-btn" type="button" on:click={openPipeline}>Open the Publish pipeline</button>
          {:else}
            <button class="op-btn" type="button" on:click={closeDelete}>OK</button>
          {/if}
        </div>
      {:else}
        <p class="tpl-dialog-body">
          Insights that have already been produced from it will keep a reference to its name, id, version and hash.
        </p>
        {#if deletion.error}
          <div class="err-box" role="alert">{deletion.error}</div>
        {/if}
        <div class="tpl-dialog-actions">
          <button class="link-btn op-cancel" type="button" disabled={saving} bind:this={deleteCancel} on:click={closeDelete}>
            Cancel
          </button>
          <button class="btn btn-sm btn-error" type="button" disabled={saving} on:click={() => void remove()}>
            {saving ? "Deleting…" : "Delete"}
          </button>
        </div>
      {/if}
    </div>
  {/if}
</dialog>

<style>
  .tpl-list {
    display: grid;
    gap: 6px;
  }
  .tpl-card {
    display: flex;
    flex-direction: column;
    padding: 7px 12px;
  }
  /* Name, description and actions on one line; the description drops under
     the name only when the two do not fit side by side. */
  .tpl-row-head {
    display: flex;
    align-items: baseline;
    gap: 12px;
  }
  .tpl-row-text {
    display: flex;
    flex: 1 1 auto;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 0 8px;
    min-width: 0;
  }
  .tpl-row-name {
    margin: 0;
    font-size: 13.5px;
    font-weight: 600;
    line-height: 1.45;
  }
  .tpl-row-desc {
    min-width: 0;
    margin: 0;
    font-size: 12.5px;
    line-height: 1.45;
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }
  .tpl-prompt {
    min-width: 0;
    margin-top: 1px;
  }
  .tpl-prompt :global(.tpl-toggle) {
    margin: 0;
    font-size: 12px;
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
    align-items: baseline;
    gap: 6px;
    margin-left: auto;
  }
  .tpl-row-actions .link-btn {
    padding: 0;
  }
  .tpl-sep {
    font-size: 12px;
    color: color-mix(in oklch, var(--color-base-content) 35%, transparent);
  }
  .tpl-editor {
    display: grid;
    gap: 12px;
    margin-top: 8px;
    padding-top: 12px;
    border-top: 1px solid var(--color-base-300);
  }
  .tpl-dialog {
    display: grid;
    gap: 12px;
    max-width: 28rem;
  }
  .tpl-dialog-title {
    margin: 0;
    font-size: 15px;
    font-weight: 600;
  }
  .tpl-dialog-body {
    margin: 0;
    font-size: 13px;
    line-height: 1.5;
  }
  .tpl-dialog-actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: flex-end;
    gap: 12px;
  }
  .tpl-dialog-title strong {
    font-weight: 700;
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
    margin: 2px 0 0;
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
