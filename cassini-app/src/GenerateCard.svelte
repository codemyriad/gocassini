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
  // There is no MODEL picker. One endpoint has one default model, set by the
  // administrator in AI providers, and that is what a run asks for (D-749):
  // a per-run combobox was a second place to choose a model for one job, and
  // an empty one — the default — let the child inherit a model chosen for a
  // different endpoint. The request still carries `model`, empty, so a per-run
  // override can return without a wire change.
  import { createEventDispatcher } from "svelte";
  import type { MeetingCatalogEntry } from "cassini-viewer/dataProvider";

  import { loadConfig } from "./operator/config";
  import type { OperatorClient } from "./operator/client";
  import type { InsightWorkflow } from "./operator/types";
  import type { OperatorPanel } from "./surfaceRouting";
  import {
    createInsight,
    listAIProviders,
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
  // should get without touching anything. The model is the endpoint's own
  // default, shown rather than chosen.
  let providers: AIProviderChoice[] = [];
  let providersAsked = false;
  let providersError = "";
  let chosenProvider = "";
  // Always empty: the operator resolves the chosen endpoint's default model.
  // Kept on the wire so a per-run override can come back without a change to
  // the request shape.
  const chosenModel = "";

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
  $: chosenProviderEntry = providers.find((provider) => provider.id === chosenProvider) ?? null;

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
        chosenProvider = providers[0].id;
      }
    } catch (error) {
      // Narrows the card rather than blocking it: with no list, the run carries
      // no provider and the deployment's configured endpoint answers, which is
      // exactly what happened before there was a picker.
      providersError = describe(error);
    }
  }

  function chooseProvider(id: string) {
    chosenProvider = id;
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

    <!-- Where this question goes. Offered to EVERYBODY, not only
         administrators: it is the asker's own transcripts being sent, so the
         choice is theirs. Absent only where there is nothing to choose — no
         endpoint at all, or a list that could not be read. The model is the
         endpoint's default, read-only: choosing an endpoint is choosing it. -->
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
        <p class="ins-card-note ins-model">
          Model: {#if chosenProviderEntry?.model}<code>{chosenProviderEntry.model}</code
            >{:else}the endpoint's own default{/if}
        </p>
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
    gap: 6px;
  }
  .ins-model code {
    font-family: monospace;
    overflow-wrap: anywhere;
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
</style>
