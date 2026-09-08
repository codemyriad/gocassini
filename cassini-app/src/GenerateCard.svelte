<script lang="ts">
  // Create an insight: ask these meetings a question, and watch the answer take
  // the minutes it takes (D-700, D-720).
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
  // A list of previous runs. It used to open on one, which put a job console in
  // a panel whose subject is the meetings you just picked — and it was a second
  // place insights are listed, disagreeing with the browse list behind it for
  // as long as their two refreshes were out of step. The browse catalogue is
  // where insights live (D-721); this card owns only the run it starts, which
  // is feedback for a button press rather than a history.
  //
  // # Who chooses what
  //
  // The TEMPLATE picker is admin-only, because the registry is ADMIN at the
  // proxy and a control that 403s when opened is worse than none.
  //
  // The PROVIDER and MODEL pickers are for everybody. Choosing which of the
  // configured endpoints answers your question is the asker's decision — it is
  // where their own transcripts go — so `operator/ai/providers` is a USER route
  // carrying ids and display names and nothing else. The base URL, the key and
  // the request bounds stay on the ADMIN settings surface.
  import { createEventDispatcher, onDestroy } from "svelte";
  import type { MeetingCatalogEntry } from "cassini-viewer/dataProvider";

  import ModelCombobox from "./ModelCombobox.svelte";
  import NeedsSetupCard from "./NeedsSetupCard.svelte";
  import { loadConfig } from "./operator/config";
  import type { OperatorClient } from "./operator/client";
  import type { InsightWorkflow } from "./operator/types";
  import type { OperatorPanel } from "./surfaceRouting";
  import {
    buildRunFailureNotice,
    createInsight,
    describeRunProgress,
    isTerminalStatus,
    listAIProviders,
    listAIProviderModels,
    pollDelayMs,
    readInsight,
    retryInsight,
    workflowTakesQuestion,
    InsightRequestError,
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

  const dispatch = createEventDispatcher<{ open: { panel: OperatorPanel; href: string } }>();

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
  // Defaults to the first provider and its own default model, which is what
  // somebody who does not care should get without touching anything.
  let providers: AIProviderChoice[] = [];
  let providersAsked = false;
  let providersError = "";
  let chosenProvider = "";
  let chosenModel = "";

  // Fetched when the model field is opened rather than on load: it is a call
  // out to the endpoint, and most runs never touch the model.
  let modelsByProvider: Record<string, AIProviderChoice[]> = {};
  let modelsErrorByProvider: Record<string, string> = {};
  let loadingModelsFor = "";

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

  // The run this card started, and nothing else. Null until Generate is
  // pressed; it survives the panel being scrolled but not reopened, which is
  // right — a run you started five minutes ago is a card in the browse list by
  // then, not an item of this panel's business.
  let run: InsightRun | null = null;
  let document = "";
  // A poll that failed is not a run that failed: the run is still whatever the
  // operator says it is, and the only honest thing to report is that the
  // question could not be asked this time.
  let pollError = "";
  let retrying = false;

  let pollTimer: ReturnType<typeof setTimeout> | null = null;
  let pollRound = 0;
  // Set on destroy, and checked after every await. Clearing the timer alone
  // would still let an in-flight poll reschedule itself after the panel closed,
  // which is a request loop with no component behind it.
  let stopped = false;

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
  $: chosenModels = modelsByProvider[chosenProvider] ?? [];

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

  onDestroy(() => {
    stopped = true;
    clearPollTimer();
  });

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
        chosenProvider = providers[0].id;
      }
    } catch (error) {
      // Narrows the card rather than blocking it: with no list, the run carries
      // no provider and the deployment's configured endpoint answers, which is
      // exactly what happened before there was a picker.
      providersError = describe(error);
    }
  }

  async function loadModels(providerId: string) {
    if (
      operatorBasePath === "" ||
      providerId === "" ||
      modelsByProvider[providerId] ||
      loadingModelsFor !== ""
    ) {
      return;
    }
    loadingModelsFor = providerId;
    modelsErrorByProvider = { ...modelsErrorByProvider, [providerId]: "" };
    try {
      modelsByProvider = {
        ...modelsByProvider,
        [providerId]: await listAIProviderModels(operatorBasePath, providerId),
      };
    } catch (error) {
      modelsErrorByProvider = { ...modelsErrorByProvider, [providerId]: describe(error) };
    } finally {
      loadingModelsFor = "";
    }
  }

  function chooseProvider(id: string) {
    // The model belonged to the old endpoint. Carried across it would name a
    // model the new one may never have heard of, and the operator refuses a
    // model with no provider to run it on.
    chosenProvider = id;
    chosenModel = "";
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
      // On screen before anything has happened to it, which is the whole point
      // of a record that exists before its content does: a local model over
      // five meetings is minutes, and a button that goes quiet for minutes
      // reads as a button that did nothing.
      run = started;
      document = "";
      schedulePoll({ reset: true });
    } catch (error) {
      createError = describe(error);
    } finally {
      creating = false;
    }
  }

  async function retry() {
    if (!run || retrying) {
      return;
    }
    const id = run.id;
    retrying = true;
    pollError = "";
    try {
      run = await retryInsight(id);
      schedulePoll({ reset: true });
    } catch (error) {
      if (error instanceof InsightRequestError && error.status === 409) {
        // The status is the lock, and 409 means the run is already moving —
        // which is what the reader wanted. Re-read it rather than paint an
        // error over a run that is doing the right thing.
        pollError = "";
        void refresh(id);
      } else {
        pollError = describe(error);
      }
    } finally {
      retrying = false;
    }
  }

  async function refresh(id: string) {
    try {
      const fresh = await readInsight(id);
      if (stopped) {
        return;
      }
      run = fresh.run;
      if (fresh.document !== "") {
        document = fresh.document;
      }
      schedulePoll({ reset: true });
    } catch (error) {
      if (!stopped) {
        pollError = describe(error);
      }
    }
  }

  function clearPollTimer() {
    if (pollTimer !== null) {
      clearTimeout(pollTimer);
      pollTimer = null;
    }
  }

  function schedulePoll(options: { reset?: boolean } = {}) {
    clearPollTimer();
    if (stopped) {
      return;
    }
    if (options.reset) {
      pollRound = 0;
    }
    // No timer at all once the run has finished. Polling stops because there is
    // nothing left to ask about, not because a counter ran out.
    if (!run || isTerminalStatus(run.status)) {
      return;
    }
    pollTimer = setTimeout(() => {
      pollTimer = null;
      void poll();
    }, pollDelayMs(pollRound));
  }

  async function poll() {
    const pending = run;
    if (stopped || !pending || isTerminalStatus(pending.status)) {
      return;
    }
    try {
      const { run: fresh, document: doc } = await readInsight(pending.id);
      if (stopped) {
        return;
      }
      const moved = fresh.status !== pending.status || fresh.attemptNumber !== pending.attemptNumber;
      run = fresh;
      if (doc !== "") {
        document = doc;
      }
      pollError = "";
      // A run that moved is asked about promptly again; one that has not is
      // asked about less and less, up to the cap.
      pollRound = moved ? 0 : pollRound + 1;
    } catch (error) {
      if (stopped) {
        return;
      }
      pollError = describe(error);
      pollRound += 1;
    }
    schedulePoll();
  }

  function describe(error: unknown): string {
    return error instanceof Error ? error.message : String(error);
  }

  function handleOpenPanel(event: CustomEvent<{ panel: OperatorPanel; href: string }>) {
    dispatch("open", event.detail);
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
        <label class="tpl-field">
          <span>Insight template</span>
          <select class="select select-sm select-bordered w-full" bind:value={chosenWorkflow}>
            {#each workflows as workflow (workflow.id)}
              <option value={workflow.id}>{workflow.name}</option>
            {/each}
          </select>
        </label>

        {#if questionAccepted}
          <!-- The template whose question is yours. Offered only by one with
               somewhere to put it, so a box that appears is a box the operator
               will accept. -->
          <label class="tpl-field">
            <span>Question</span>
            <textarea
              class="textarea textarea-sm textarea-bordered w-full"
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
          <p class="tpl-question">“{chosenWorkflowEntry.question}”</p>
          {#if chosenWorkflowEntry.description}
            <p class="tpl-description">{chosenWorkflowEntry.description}</p>
          {/if}
        {/if}
      </div>

      {#if workflowsError}
        <p class="text-xs text-warning">
          The template list could not be read, so this runs the template your deployment has
          configured. {workflowsError}
        </p>
      {/if}
    {:else}
      <!-- The template registry is ADMIN at the proxy, so there is no picker to
           offer. Saying which template runs is the honest substitute for a
           control that would 403 when opened. -->
      <p class="ins-card-sub">
        This runs the template your administrator configured for insights.
      </p>
    {/if}

    <!-- Where this question goes, and on which model. Offered to EVERYBODY, not
         only administrators: it is the asker's own transcripts being sent, so
         the choice is theirs. Absent only where there is nothing to choose
         between — one endpoint, or a list that could not be read. -->
    <!-- Where this question goes, and on which model. Offered to EVERYBODY, not
         only administrators: it is the asker's own transcripts being sent, so
         the choice is theirs. Absent only where there is nothing to choose —
         no endpoint at all, or a list that could not be read. -->
    {#if providers.length > 0}
      <div class="ins-endpoint">
        <label class="tpl-field">
          <span>Provider</span>
          <select
            class="select select-sm select-bordered w-full"
            value={chosenProvider}
            on:change={(event) => chooseProvider(event.currentTarget.value)}
          >
            {#each providers as provider (provider.id)}
              <option value={provider.id}>{provider.name}</option>
            {/each}
          </select>
        </label>
        <!-- Empty is the endpoint's own default, which is what somebody who
             does not care should get without touching anything. Opening the
             field fetches what this endpoint serves. -->
        <ModelCombobox
          bind:value={chosenModel}
          models={chosenModels}
          loading={loadingModelsFor === chosenProvider}
          error={modelsErrorByProvider[chosenProvider] ?? ""}
          on:open={() => void loadModels(chosenProvider)}
        />
      </div>
    {/if}
    {#if providersError}
      <p class="ins-card-note">
        The AI endpoints could not be listed, so this runs on the one your deployment has
        configured. {providersError}
      </p>
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

      <!-- The instance's key pays for this, so a run is attributable to the
           deployment rather than to the person who asked. Said here rather than
           discovered from a bill (D-700). -->
      <p class="ins-card-note">
        The transcripts of these meetings are sent to this deployment's configured AI endpoint,
        and the insight is written into your own Nextcloud files.
      </p>
    </div>

    {#if createError}
      <p class="text-xs text-error" role="alert">{createError}</p>
    {/if}

    <!-- The run this card started. Not a list: what happened to it is the
         answer to the button, and every insight — this one included — is a card
         in the browse list as soon as it exists. -->
    {#if run}
      <div class="rounded-box border border-base-300 bg-base-100 p-2.5">
        <div class="flex flex-wrap items-center justify-between gap-x-2 gap-y-1">
          <span class="min-w-0 truncate text-xs font-medium">
            {run.question.trim() !== ""
              ? `“${run.question.trim()}”`
              : (chosenWorkflowEntry?.name ?? "This insight")}
          </span>
          <span class="badge badge-sm" data-status={run.status}>{run.status}</span>
        </div>

        {#if run.attemptNumber > 1}
          <p class="mt-1 text-xs text-base-content/60">Attempt {run.attemptNumber}.</p>
        {/if}

        {#if run.status !== "failed"}
          <p class="mt-1 text-xs text-base-content/70">{describeRunProgress(run)}</p>
        {/if}

        {#if run.status === "succeeded"}
          {#if run.documentPath}
            <p class="mt-1 font-mono text-xs break-all text-base-content/60">
              {run.documentPath}
            </p>
          {/if}
          {#if document}
            <details class="mt-1">
              <summary class="cursor-pointer text-xs">Read it here</summary>
              <pre
                class="mt-1 max-h-72 overflow-auto rounded-box border border-base-300 bg-base-200 p-2 text-xs whitespace-pre-wrap">{document}</pre>
            </details>
          {/if}
        {/if}

        {#if run.status === "failed"}
          <div class="mt-1.5 grid gap-1.5">
            <!-- The same card, and the same route-preserving deep link, that
                 every other "this deployment cannot do that yet" state renders.
                 Its words come from buildRunFailureNotice, where a test can
                 reach them. -->
            <NeedsSetupCard notice={buildRunFailureNotice({ run, isAdmin })} on:open={handleOpenPanel} />
            <div class="flex items-center gap-2">
              <button
                class="btn btn-outline btn-xs"
                type="button"
                disabled={retrying}
                on:click={retry}
              >
                {retrying ? "Retrying…" : "Retry"}
              </button>
              <!-- Retry re-runs the REQUEST: the endpoint chosen when this
                   insight was asked for. It falls back to the deployment's own
                   only when that endpoint has since been removed, which is what
                   keeps "configure an endpoint, then retry" a fix. -->
              <span class="text-xs text-base-content/60">
                Runs again on the endpoint you chose.
              </span>
            </div>
          </div>
        {/if}

        {#if pollError}
          <!-- Deliberately not an error on the run: the run is whatever the
               operator says it is, and this says only that it could not be
               asked. -->
          <p class="mt-1 text-xs text-warning" role="status">{pollError}</p>
        {/if}
      </div>
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

  .tpl-box {
    display: grid;
    gap: 12px;
    padding: 14px;
    background-color: color-mix(in oklch, var(--color-base-content) 4%, var(--color-base-100));
    border: 1px solid color-mix(in oklch, var(--color-base-content) 14%, var(--color-base-100));
    border-radius: var(--radius-box, 0.75rem);
  }
  .tpl-field {
    display: grid;
    gap: 5px;
  }
  .tpl-field > span {
    font-size: 12px;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }
  /* The question carries the weight: it is what the template will actually ask. */
  .tpl-question {
    font-size: 13.5px;
    font-weight: 600;
    line-height: 1.45;
    color: var(--color-base-content);
  }
  /* And the shape of the document reads under it, tight enough to belong to it. */
  .tpl-description {
    margin-top: -7px;
    font-size: 12.5px;
    line-height: 1.55;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }

  .ins-endpoint {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    gap: 12px;
  }

  .ins-card-foot {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .ins-card-note {
    font-size: 12px;
    line-height: 1.5;
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }

  /* Status colours by name rather than by position, so a status this build does
     not colour still renders as a plain badge instead of inheriting the wrong
     one. daisyUI tokens, like the rest of the shell. */
  .badge[data-status="queued"] {
    background-color: var(--color-base-300);
    border-color: var(--color-base-300);
  }
  .badge[data-status="running"] {
    background-color: color-mix(in oklch, var(--color-primary) 22%, transparent);
    border-color: color-mix(in oklch, var(--color-primary) 40%, transparent);
  }
  .badge[data-status="succeeded"] {
    background-color: color-mix(in oklch, var(--color-success, oklch(70% 0.15 150)) 24%, transparent);
    border-color: color-mix(in oklch, var(--color-success, oklch(70% 0.15 150)) 45%, transparent);
  }
  .badge[data-status="failed"] {
    background-color: color-mix(in oklch, var(--color-error, oklch(62% 0.2 25)) 22%, transparent);
    border-color: color-mix(in oklch, var(--color-error, oklch(62% 0.2 25)) 45%, transparent);
  }
</style>
