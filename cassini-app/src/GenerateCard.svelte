<script lang="ts">
  // Create an insight: ask these meetings a question (D-700, D-720, D-749).
  //
  // It sits in the Prepare panel's `generate` slot, under Copy and Download,
  // because asking a question of the set you have just reviewed is the next
  // thing you do with it. It lives in cassini-app rather than in the viewing
  // layer for the reason the readiness card does: there is an operator behind
  // this build and none behind a standalone export, and the viewer has no
  // notion of one.
  //
  // # What is NOT here, and why
  //
  // The run, once started. Generate hands the created record up as a `created`
  // event and stops: the shell puts it at the top of the browse list and
  // closes the panel, and the list's own refresh carries it from queued to an
  // answer. This card used to render the run under the button and poll it,
  // which was a second place one run was shown — disagreeing with the browse
  // list behind it for as long as their two refreshes were out of step — and
  // the only place a failed run could be retried, in a panel that is gone by
  // the time most people notice. The browse catalogue is where insights live
  // (D-721), and Retry lives on the card and the sheet there.
  //
  // # Who chooses what
  //
  // The TEMPLATE picker is admin-only, because the registry is ADMIN at the
  // proxy and a control that 403s when opened is worse than none.
  //
  // The PROVIDER picker is for everybody. Choosing which of the configured
  // endpoints answers your question is the asker's decision — it is where
  // their own transcripts go — so `operator/ai/providers` is a USER route
  // carrying ids, display names and each endpoint's default model, and nothing
  // else. The base URL, the key and the request bounds stay on the ADMIN
  // settings surface.
  //
  // The MODEL field is for everybody too, pre-filled with the chosen endpoint's
  // default so nobody has to pick one every time, and editable for the run
  // where the default is the wrong one (D-749). Changing the endpoint re-fills
  // it with that endpoint's default. What is typed here affects this run only;
  // the default itself is set in AI providers and nothing is written back.
  // The box is never empty when the endpoint has a default, so a run cannot
  // inherit a model chosen for a different endpoint the way an empty per-run
  // field once let it.
  import { createEventDispatcher } from "svelte";
  import { ChevronDown } from "@lucide/svelte";
  import type { MeetingCatalogEntry } from "cassini-viewer/dataProvider";

  import ModelCombobox from "./ModelCombobox.svelte";
  import { loadConfig } from "./operator/config";
  import type { OperatorClient } from "./operator/client";
  import type { InsightWorkflow } from "./operator/types";
  import type { OperatorPanel } from "./surfaceRouting";
  import {
    createInsight,
    listAIProviders,
    listAIProviderModels,
    workflowTakesQuestion,
    type AIProviderChoice,
    type InsightRun,
  } from "./insights/client";

  // The picked meetings, in pick order — the order the bundle prints in, and so
  // the order the question is asked about. Handed down through the Prepare
  // panel's slot rather than fetched here: the selection belongs to the browse
  // surface, and a second reading of it would be a second answer.
  export let entries: readonly MeetingCatalogEntry[] = [];

  // The operator API client, or null for anyone the admin probe denied.
  // `operator/settings/workflows` is ADMIN at the proxy, so a non-admin cannot
  // list templates at all — null is that fact, and the card offers no template
  // control rather than one that 403s when opened. Their run carries no
  // workflow, which the operator reads as "this deployment's configured one".
  export let operatorClient: OperatorClient | null = null;

  // `created` carries the run the operator answered with — a record that
  // exists before its content does — up to the shell, which owns the list it
  // belongs in.
  const dispatch = createEventDispatcher<{
    open: { panel: OperatorPanel; href: string };
    created: InsightRun;
  }>();

  // The id the publish pipeline's summary step runs when nothing names one
  // (internal/insight/workflows: SummariseID). Named here only to pick the
  // picker's OPENING selection: the operator accepts any id the registry
  // resolves, so a build that ever stopped shipping this one degrades to "the
  // first template listed" rather than to an error.
  const SHIPPED_DEFAULT_WORKFLOW = "summarise";

  let workflows: InsightWorkflow[] = [];
  // A registry that could not be read is not a registry with nothing in it.
  // Either way Generate still works — an insight with no template named runs
  // whatever the deployment configured — so this narrows the picker rather than
  // blocking the card.
  let workflowsError = "";
  let workflowsAsked = false;
  let chosenWorkflow = "";

  let question = "";

  // The endpoints this deployment has, and the one this run will reach.
  // Defaults to the first provider, which is what somebody who does not care
  // should get without touching anything.
  let providers: AIProviderChoice[] = [];
  let providersAsked = false;
  let providersError = "";
  let chosenProvider = "";
  // The model this run asks for. Pre-filled with the chosen endpoint's default
  // whenever the endpoint is chosen, and free to edit after that. An untouched
  // box sends the default explicitly, which is the same run the operator
  // would have resolved for itself.
  let chosenModel = "";

  // What each endpoint said it serves, asked for when the model field opens
  // and kept per endpoint so a second opening is free. Loading and failure are
  // keyed per endpoint too, never held in one in-flight marker: one slow
  // listing must not make another endpoint's field say it listed nothing
  // (D-740).
  let modelsByProvider: Record<string, AIProviderChoice[]> = {};
  let modelsLoadingByProvider: Record<string, boolean> = {};
  let modelsErrorByProvider: Record<string, string> = {};

  // The operator base is the same one every other call in this app resolves,
  // and it is a pure read of the injected config — no client, so it works for
  // the people who have none.
  let operatorBasePath = "";
  try {
    operatorBasePath = loadConfig().operatorBasePath;
  } catch {
    // A build with no operator config behind it offers no picker, exactly as it
    // offers no Generate button.
    operatorBasePath = "";
  }

  let creating = false;
  let createError = "";

  $: meetingIds = entries.map((entry) => entry.id);
  // "Generate insight", as the design has it. The count is already on the rows
  // above it and on the selection bar behind the panel, and repeating it on the
  // button made the one action read as a summary of the thing above it.
  const generateLabel = "Generate insight";
  // The probe that decides whether there is an operator surface at all is the
  // same one that produced this client. There is no second notion of admin here
  // to drift from the first.
  $: isAdmin = operatorClient !== null;
  $: if (operatorClient && !workflowsAsked) {
    void loadWorkflows();
  }
  // Not gated on operatorClient: this route is USER, and the whole point is
  // that everybody gets to choose.
  $: if (operatorBasePath !== "" && !providersAsked) {
    void loadProviders();
  }
  $: chosenWorkflowEntry = workflows.find((workflow) => workflow.id === chosenWorkflow) ?? null;

  // Whether this run may carry a question of your own, decided against the
  // registry's own bytes rather than assumed.
  //
  // `POST insights` refuses a question a workflow has no slot for AND a workflow
  // that needs one with none given, so the box appears for exactly the templates
  // that can use it. It is read off the instruction — the spliced system prompt
  // itself — which is the same thing the operator decides on, so the two cannot
  // disagree about which templates take a question.
  $: questionAccepted = chosenWorkflow !== "" && workflowTakesQuestion(chosenWorkflowEntry);
  // A workflow with a slot for a question cannot run without one, so Generate
  // waits for it rather than sending a request the operator will refuse.
  $: questionMissing = questionAccepted && question.trim() === "";

  async function loadWorkflows() {
    if (!operatorClient) {
      return;
    }
    workflowsAsked = true;
    workflowsError = "";
    try {
      workflows = await operatorClient.listInsightWorkflows();
      // Opened on a real template rather than on a synthetic "this deployment's
      // default" row. That row named no template, disclosed no question, and
      // read as a setting rather than a choice — and the thing it stood for is
      // in the list beside it.
      if (chosenWorkflow === "" && workflows.length > 0) {
        chosenWorkflow =
          workflows.find((workflow) => workflow.id === SHIPPED_DEFAULT_WORKFLOW)?.id ??
          workflows[0].id;
      }
    } catch (error) {
      workflowsError = describe(error);
    }
  }

  async function loadProviders() {
    providersAsked = true;
    providersError = "";
    try {
      providers = await listAIProviders(operatorBasePath);
      if (chosenProvider === "" && providers.length > 0) {
        chooseProvider(providers[0].id);
      }
    } catch (error) {
      // Narrows the card rather than blocking it: with no list, the run carries
      // no provider and the deployment's configured endpoint answers, which is
      // exactly what happened before there was a picker.
      providersError = describe(error);
    }
  }

  // Choosing an endpoint chooses its default model with it. A model typed for
  // the old endpoint is not carried across: it would name one the new endpoint
  // may never have heard of.
  function chooseProvider(id: string) {
    chosenProvider = id;
    chosenModel = defaultModelOf(id);
  }

  // What the folded control says it will use, so the choice inside it need not
  // be opened to be known.
  function providerName(providerId: string): string {
    return providers.find((entry) => entry.id === providerId)?.name ?? "";
  }

  function defaultModelOf(providerId: string): string {
    return providers.find((provider) => provider.id === providerId)?.model ?? "";
  }

  async function loadModels(providerId: string) {
    if (
      providerId === "" ||
      modelsByProvider[providerId] ||
      modelsLoadingByProvider[providerId]
    ) {
      return;
    }
    modelsLoadingByProvider = { ...modelsLoadingByProvider, [providerId]: true };
    modelsErrorByProvider = { ...modelsErrorByProvider, [providerId]: "" };
    try {
      modelsByProvider = {
        ...modelsByProvider,
        [providerId]: await listAIProviderModels(operatorBasePath, providerId),
      };
    } catch (error) {
      modelsErrorByProvider = { ...modelsErrorByProvider, [providerId]: describe(error) };
    } finally {
      modelsLoadingByProvider = { ...modelsLoadingByProvider, [providerId]: false };
    }
  }

  async function generate() {
    if (creating || meetingIds.length === 0 || questionMissing) {
      return;
    }
    creating = true;
    createError = "";
    try {
      const started = await createInsight({
        meetingIds,
        workflow: chosenWorkflow,
        // Only ever sent to a workflow with somewhere to put it. Text left in
        // the box by a template that was then switched away from must not ride
        // along into one that would be refused for carrying it.
        question: questionAccepted ? question : "",
        // The endpoint this run should reach. Empty when there was no list to
        // choose from, which the operator reads as "this deployment's own".
        provider: chosenProvider,
        model: chosenModel,
      });
      // Handed up and done. The record exists before its content does, and
      // the shell shows it where every insight is shown — at the top of the
      // browse list, with the panel closed — rather than under this button.
      dispatch("created", started);
    } catch (error) {
      createError = describe(error);
    } finally {
      creating = false;
    }
  }

  function describe(error: unknown): string {
    return error instanceof Error ? error.message : String(error);
  }
</script>

<!-- Nothing to ask about, nothing to render: an empty selection is not a
     question, and the Prepare panel is closed by the browse surface as soon as
     the last meeting is unpicked anyway. -->
{#if entries.length > 0}
  <section class="ins-card" aria-label="Create an insight">
    <div>
      <h3 class="ins-card-title">Create an insight</h3>
      <p class="ins-card-sub">
        A written answer drawn from these meetings, saved alongside the meeting bundle rather
        than inside it.
      </p>
    </div>

    {#if isAdmin}
      <!-- One box: the template you pick and what it will ask are the same
           decision, so they are not two controls with a gap between them. -->
      <div class="tpl-box">
        <label class="tpl-field tpl-field-row">
          <span>Insight template</span>
          <span class="gc-select">
            <select class="gc-input" bind:value={chosenWorkflow}>
              {#each workflows as workflow (workflow.id)}
                <option value={workflow.id}>{workflow.name}</option>
              {/each}
            </select>
            <ChevronDown size={14} class="gc-select-chevron" aria-hidden="true" />
          </span>
        </label>

        {#if questionAccepted}
          <!-- The template whose question is yours. Offered only by one with
               somewhere to put it, so a box that appears is a box the operator
               will accept. -->
          <label class="tpl-field">
            <span>Question</span>
            <textarea
              class="tpl-question-box textarea textarea-sm textarea-bordered w-full"
              rows="3"
              placeholder="What should this insight answer?"
              bind:value={question}
            ></textarea>
          </label>
        {:else if chosenWorkflowEntry}
          <!-- What this template asks, and what comes back. The question is the
               affordance — the name says nothing about what the model is asked
               to do — so it carries the weight, and the shape of the document
               reads under it. -->
          <!-- Quoted and inset: it is what the template will actually ask,
               not a note about it. The registry's own description said the
               same thing a second time, so it is gone. -->
          <p class="tpl-question">{chosenWorkflowEntry.question}</p>
        {/if}
      </div>

      {#if workflowsError}
        <p class="text-xs text-warning">Templates could not be listed. The configured one runs.</p>
      {/if}
    {:else}
      <!-- The template registry is ADMIN at the proxy, so there is no picker to
           offer. Saying which template runs is the honest substitute for a
           control that would 403 when opened. -->
      <p class="ins-card-sub">
        This runs the template your administrator configured for insights.
      </p>
    {/if}

    <!-- Where this question goes. Offered to EVERYBODY, not only
         administrators: it is the asker's own transcripts being sent, so the
         choice is theirs. Absent only where there is nothing to choose — no
         endpoint at all, or a list that could not be read. -->
    {#if providers.length > 0}
      <!-- Folded away: the first provider and its default model answer the
           question for most people, and the ones who want another are the ones
           who will open this. -->
      <details class="ins-endpoint">
        <summary class="ins-endpoint-toggle">
          <span class="ins-endpoint-chev" aria-hidden="true"></span>
          <span class="ins-endpoint-name">Provider and model</span>
          <span class="ins-endpoint-now">
            {#if providerName(chosenProvider)}
              <code>{providerName(chosenProvider)}</code>
            {/if}
            {#if chosenModel}
              <code>{chosenModel}</code>
            {/if}
          </span>
        </summary>
        <div class="ins-endpoint-body">
        <label class="tpl-field tpl-field-row">
          <span>Provider</span>
          <span class="gc-select">
            <select
              class="gc-input"
              value={chosenProvider}
              on:change={(event) => chooseProvider(event.currentTarget.value)}
            >
              {#each providers as provider (provider.id)}
                <option value={provider.id}>{provider.name}</option>
              {/each}
            </select>
            <ChevronDown size={14} class="gc-select-chevron" aria-hidden="true" />
          </span>
        </label>
        <!-- Pre-filled with the endpoint's default, editable for this run.
             Opening the field lists what the endpoint serves; the list narrows
             what you type and never gates it. -->
        <ModelCombobox
          bind:value={chosenModel}
          label="Model"
          models={modelsByProvider[chosenProvider] ?? []}
          loading={modelsLoadingByProvider[chosenProvider] === true}
          error={modelsErrorByProvider[chosenProvider] ?? ""}
          placeholder="endpoint default"
          on:open={() => void loadModels(chosenProvider)}
        />
        </div>
      </details>
    {/if}
    {#if providersError}
      <p class="ins-card-note">{providersError}</p>
    {/if}

    <div class="ins-card-foot">
      <button
        class="btn btn-primary w-full"
        type="button"
        disabled={creating || questionMissing}
        on:click={generate}
      >
        {creating ? "Starting…" : generateLabel}
      </button>
      {#if questionMissing}
        <p class="ins-card-note">
          This template asks whatever you type above, so it needs a question.
        </p>
      {/if}

      <!-- Where the transcripts go and where the answer lands, said before
           the run rather than discovered after it (D-700). -->
      <p class="ins-card-note">
        These meetings' transcripts are sent to the AI provider to be read. The insight comes
        back as a document in your Nextcloud files.
      </p>
    </div>

    {#if createError}
      <p class="text-xs text-error" role="alert">{createError}</p>
    {/if}
  </section>
{/if}

<style>
  /* Type and rhythm from the design prototype's own values (.prep-insight-card
     and .tpl-box), because "a bit tighter" is not a spec and the panel reads
     wrong at Tailwind's nearest steps: the heading is 16px where text-sm is 14,
     and the description sits almost against it rather than a gap away. */
  .ins-card {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .ins-card-title {
    font-size: 16px;
    font-weight: 650;
    letter-spacing: -0.01em;
    color: var(--color-base-content);
  }
  /* Pulled up under the heading: they are one block, and a full gap between
     them reads as two. */
  .ins-card-sub {
    margin-top: 2px;
    font-size: 12.5px;
    line-height: 1.5;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }

  /* A card on the drawer's ground, like the bundle list above it. */
  .tpl-box {
    display: grid;
    gap: 12px;
    padding: 14px;
    background-color: var(--color-base-100);
    border: 1px solid color-mix(in oklch, var(--color-base-content) 12%, var(--color-base-200));
    border-radius: var(--radius-box, 0.75rem);
  }
  .tpl-field {
    display: grid;
    gap: 5px;
  }
  /* The label and the control are one line while there is room for both: a
     picker under its own name reads as a form where this is one choice. */
  .tpl-field-row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 12px;
  }
  .tpl-field-row > span {
    flex: none;
  }
  .tpl-field-row > .gc-select {
    flex: 1 1 160px;
    min-width: 0;
  }
  /* One field shape for the two pickers and the model box: same height, same
     border, same chevron, drawn rather than left to the browser. */
  .gc-select {
    position: relative;
    display: flex;
    min-width: 0;
  }
  .gc-input {
    width: 100%;
    min-width: 0;
    height: 2rem;
    padding: 0 28px 0 10px;
    cursor: pointer;
    appearance: none;
    -webkit-appearance: none;
    background-image: none;
    background-color: var(--color-base-100);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-field, 0.5rem);
    font-size: 0.875rem;
    color: var(--color-base-content);
  }
  .gc-input:focus,
  .gc-input:focus-visible {
    outline: none;
    border-color: color-mix(in oklch, var(--color-base-content) 45%, transparent);
    box-shadow: 0 0 0 3px color-mix(in oklch, var(--color-base-content) 11%, var(--color-base-100));
  }
  .gc-select :global(.gc-select-chevron) {
    position: absolute;
    top: 50%;
    right: 8px;
    transform: translateY(-50%);
    pointer-events: none;
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  .tpl-field > span {
    font-size: 12px;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }
  /* Typed into, so it reads at the size of the fields rather than of a note. */
  .tpl-question-box {
    font-size: 0.875rem;
    line-height: 1.5;
  }

  /* The question carries the weight: it is what the template will actually
     ask, so it is set as the quotation it is. */
  .tpl-question {
    padding: 8px 12px 8px 11px;
    background-color: var(--color-base-200);
    border-left: 2px solid color-mix(in oklch, var(--color-base-content) 30%, transparent);
    border-radius: 0 var(--radius-field, 0.5rem) var(--radius-field, 0.5rem) 0;
    font-size: 13px;
    line-height: 1.5;
    color: color-mix(in oklch, var(--color-base-content) 80%, transparent);
  }

  .ins-endpoint {
    display: grid;
    gap: 12px;
  }
  /* The fields line up with the summary's own text, not with its chevron. */
  .ins-endpoint-body {
    display: grid;
    gap: 12px;
    /* The open fields keep the same air under them as the closed summary has,
       so the button does not sit tighter to the last field than to the row it
       replaced. */
    padding: 0 14px 8px;
  }
  .ins-endpoint-toggle {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 2px 8px;
    cursor: pointer;
    list-style: none;
    font-size: 12.5px;
    font-weight: 550;
    color: color-mix(in oklch, var(--color-base-content) 75%, transparent);
  }
  .ins-endpoint-toggle::-webkit-details-marker {
    display: none;
  }
  .ins-endpoint-chev {
    width: 6px;
    height: 6px;
    border-right: 1.5px solid currentColor;
    border-bottom: 1.5px solid currentColor;
    transform: rotate(-45deg);
    transition: transform 120ms ease;
  }
  .ins-endpoint[open] .ins-endpoint-chev {
    transform: rotate(45deg);
  }
  /* The model field takes the same shape as the two selects above it: its
     name on the left, its control on the right, and a box that matches theirs
     rather than the settings pages' own input. */
  .ins-endpoint :global(.model-field) {
    flex-direction: row;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 12px;
  }
  .ins-endpoint :global(.model-label) {
    flex: none;
    font-size: 12px;
    font-weight: 400;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }
  .ins-endpoint :global(.model-input) {
    flex: 1 1 160px;
  }
  .ins-endpoint :global(.model-input input) {
    height: 2rem;
    padding-block: 0;
    font-size: 0.875rem;
    background-color: var(--color-base-100);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-field, 0.5rem);
  }
  .ins-endpoint :global(.model-chevron) {
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }

  /* On its own line: the summary names the control, and what it is set to is
     a value under it rather than a tail that squeezes the name. */
  .ins-endpoint-name {
    flex: none;
    white-space: nowrap;
  }
  /* Beside the name where the panel is wide enough for both, under it when it
     is not. */
  .ins-endpoint-now {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    min-width: 0;
  }
  @media (max-width: 720px) {
    .ins-endpoint-now {
      flex-basis: 100%;
      padding-left: 14px;
    }
  }
  .ins-endpoint-now code {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    padding: 1px 6px;
    background-color: var(--color-base-200);
    border: 1px solid color-mix(in oklch, var(--color-base-content) 14%, var(--color-base-200));
    border-radius: 5px;
    font-family: var(--font-mono, ui-monospace, monospace);
    font-size: 11.5px;
    font-weight: 400;
    color: color-mix(in oklch, var(--color-base-content) 75%, transparent);
  }

  /* Narrow enough that a label and its control share a line badly: the two
     stack, and the control takes the width. */
  @media (max-width: 720px) {
    .tpl-field-row {
      display: grid;
      gap: 5px;
    }
    .ins-endpoint :global(.model-field) {
      display: grid;
      gap: 5px;
    }
    /* Stacked, the rows need the space between them that a shared line gave
       them side by side. */
    .tpl-box {
      gap: 16px;
    }
    .ins-endpoint-body {
      gap: 16px;
    }
    /* Stacked, a control takes the row it is on. */
    .tpl-field-row > .gc-select,
    .ins-endpoint :global(.model-input) {
      width: 100%;
    }
  }

  .ins-card-foot {
    margin-top: 4px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .ins-card-note {
    font-size: 12px;
    line-height: 1.5;
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }
</style>
