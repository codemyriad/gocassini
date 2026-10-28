<script module lang="ts">
  // Enough hash to recognise a document by, with the whole of it a hover or a
  // copy away. A truncated hash is an identifier, not a checksum, which is why
  // the full value stays on the element it labels.
  export function shortHash(sha256: string): string {
    return sha256.length > 12 ? `${sha256.slice(0, 12)}…` : sha256;
  }
</script>

<script lang="ts">
  // Insight templates: the registry, rendered (D-718), and the custom ones
  // edited (D-819).
  //
  // Every row opens onto the instruction it actually sends — the system prompt
  // with its template spliced in, byte for byte — rather than a plain-language
  // paraphrase of it. The hash beside it names the bytes on the screen, and an
  // insight document records the same hash, so the two can be compared rather
  // than trusted.
  //
  // Built-in templates are compiled into the image and cannot be changed here;
  // "Start from this" copies one into a new custom template. Custom templates
  // are files on the app's volume, written by the operator, which checks each
  // save against the recorder's registry and refuses one that would not run.
  // Every save is a new version, so a document still names the bytes it was
  // written with.
  import { createEventDispatcher, onMount } from "svelte";
  import { FileText, RefreshCw } from "@lucide/svelte";
  import { OperatorClient } from "./operator/client";
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

  // The one template being edited. `isNew` is a template that has no file yet,
  // whose id is still the administrator's to choose; an existing one keeps its
  // id, because the id is what documents and settings name it by.
  type Editor = { key: string; isNew: boolean; id: string; draft: InsightTemplateDraft };
  let editor: Editor | null = null;
  let saving = false;
  let saveError = "";
  let pendingDelete = "";
  let deleteError = "";

  // The id rule the operator and the recorder both enforce: it becomes the
  // file name and a `--workflow` argument.
  const TEMPLATE_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;

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

  // A fresh id nobody is using yet, so "Start from this" opens on something
  // that will save as it stands.
  function freeId(base: string): string {
    const taken = new Set(workflows.map((workflow) => workflow.id));
    let candidate = `${base}-custom`;
    for (let n = 2; taken.has(candidate); n++) {
      candidate = `${base}-custom-${n}`;
    }
    return candidate;
  }

  function startFrom(workflow: InsightWorkflow): Editor {
    return {
      key: `new:${workflow.id}`,
      isNew: true,
      id: freeId(workflow.id),
      draft: {
        name: `${workflow.name} (custom)`,
        question: workflow.question,
        description: workflow.description,
        instruction: workflow.instruction,
      },
    };
  }

  function editExisting(workflow: InsightWorkflow): Editor {
    return {
      key: rowKey(workflow),
      isNew: false,
      id: workflow.id,
      draft: {
        name: workflow.name,
        question: workflow.question,
        description: workflow.description,
        instruction: workflow.instruction,
      },
    };
  }

  function openEditor(next: Editor) {
    editor = next;
    saveError = "";
    pendingDelete = "";
  }

  $: idProblem =
    editor === null || !editor.isNew
      ? ""
      : !TEMPLATE_ID_PATTERN.test(editor.id)
        ? "Use letters, digits, '.', '_' or '-', starting with a letter or digit."
        : workflows.some((workflow) => workflow.id === editor?.id)
          ? "A template with this id already exists."
          : "";
  $: editorReady =
    editor !== null &&
    idProblem === "" &&
    editor.draft.name.trim() !== "" &&
    editor.draft.instruction.trim() !== "";

  async function save() {
    if (!client || !editor || !editorReady) {
      return;
    }
    saving = true;
    saveError = "";
    try {
      await client.saveInsightTemplate(editor.id, editor.draft);
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
      await load();
    } catch (error) {
      deleteError = error instanceof Error ? error.message : String(error);
    } finally {
      saving = false;
    }
  }
</script>

<header class="op-panel-head">
  <div>
    <div class="op-panel-title">
      <h1>Insight templates</h1>
    </div>
    <p>Used for insights and meeting summaries. Built-in templates come with Cassini; start a custom one from any of them.</p>
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
  <section class="tpl-list">
    <!-- Keyed by origin as well as id: a custom file named after a built-in is
         listed beside it, broken, and the two must not collide. -->
    {#each workflows as workflow (`${workflow.origin}:${workflow.id}`)}
      <article class="op-tint tpl-card" class:off={hasProvider === false || workflow.error !== ""}>
        <div class="set-row-main">
          <h3 class="set-row-name">{workflow.name}</h3>
          {#if workflow.description}
            <p class="set-row-sub">{workflow.description}</p>
          {/if}
        </div>

        {#if workflow.origin}
          <div class="tpl-origin">{workflow.origin}</div>
        {/if}

        <div class="tpl-row-actions">
          {#if isCustom(workflow)}
            <button class="link-btn" type="button" disabled={saving} on:click={() => openEditor(editExisting(workflow))}>
              Edit
            </button>
            <button
              class="link-btn danger"
              type="button"
              disabled={saving}
              aria-expanded={pendingDelete === rowKey(workflow)}
              on:click={() => {
                pendingDelete = rowKey(workflow);
                deleteError = "";
              }}
            >
              Delete
            </button>
          {:else if !workflow.error}
            <button class="link-btn" type="button" disabled={saving} on:click={() => openEditor(startFrom(workflow))}>
              Start from this
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
        <!-- The question is the affordance: it says what the template asks,
             and opening it says how it asks for it. A template with no
             question of its own (the asker's) opens on a plain label. -->
        <details class="tpl-prompt">
          <summary class="tpl-toggle">
            <span class="tpl-chev" aria-hidden="true"></span>
            {#if workflow.question}“{workflow.question}”{:else}Prompt{/if}
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
              Delete <strong>{workflow.name}</strong>? Insights already written with it keep their text
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

        {#if editor && (editor.key === rowKey(workflow) || editor.key === `new:${workflow.id}`)}
            <form class="tpl-editor" on:submit|preventDefault={save} aria-label="Edit insight template">
              {#if editor.isNew}
                <label class="op-field">
                  <span class="op-field-label">Id</span>
                  <input class="op-input" type="text" bind:value={editor.id} autocomplete="off" spellcheck="false" />
                  {#if idProblem}
                    <span class="tpl-hint tpl-hint-bad">{idProblem}</span>
                  {:else}
                    <span class="tpl-hint">Documents and settings name the template by this. It cannot be changed later.</span>
                  {/if}
                </label>
              {/if}
              <label class="op-field">
                <span class="op-field-label">Name</span>
                <input class="op-input" type="text" bind:value={editor.draft.name} maxlength="500" />
              </label>
              <label class="op-field">
                <span class="op-field-label">Question</span>
                <input class="op-input" type="text" bind:value={editor.draft.question} maxlength="500" />
                <span class="tpl-hint">
                  What it asks, in a person's words. Leave it empty for a template that takes the reader's own
                  question, and put <code>{"{{QUESTION}}"}</code> in the instruction where it goes.
                </span>
              </label>
              <label class="op-field">
                <span class="op-field-label">Description</span>
                <input class="op-input" type="text" bind:value={editor.draft.description} maxlength="500" />
              </label>
              <label class="op-field">
                <span class="op-field-label">Instruction</span>
                <textarea class="op-input tpl-textarea" bind:value={editor.draft.instruction} rows="16" spellcheck="false"></textarea>
                <span class="tpl-hint">Exactly what the model receives, including the shape of the document it writes.</span>
              </label>
              {#if saveError}
                <div class="err-box" role="alert">{saveError}</div>
              {/if}
              <div class="tpl-actions">
                <button class="op-btn" type="submit" disabled={!editorReady || saving}>
                  {saving ? "Saving…" : "Save template"}
                </button>
                <button class="link-btn op-cancel" type="button" disabled={saving} on:click={() => (editor = null)}>
                  Cancel
                </button>
              </div>
            </form>
        {/if}
      </article>
    {/each}
  </section>
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
  .tpl-origin {
    flex: none;
    margin-top: 1px;
    padding: 2px 7px;
    font-size: 11px;
    font-weight: 500;
    line-height: 1.4;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-selector, 0.25rem);
  }
</style>
