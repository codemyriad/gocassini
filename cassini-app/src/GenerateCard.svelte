<script lang="ts">
  // Ask these meetings a question, and watch the answer take the minutes it
  // takes (D-700, D-720).
  //
  // It sits in the Prepare panel's `generate` slot, under Copy and Download,
  // because asking a question of the set you have just reviewed is the next
  // thing you do with it. It lives in cassini-app rather than in the viewing
  // layer for the reason the readiness card does: there is an operator behind
  // this build and none behind a standalone export, and the viewer has no
  // notion of one.
  //
  // The prototype models a run as 900ms of "Generating…" followed by success.
  // A local model over five meetings is minutes and sometimes fails, so what is
  // actually built here is the run list: a card appears the moment Generate is
  // pressed, carries queued -> running -> succeeded|failed, and a failed one
  // says what went wrong in the same words, and with the same deep link, that
  // every other unconfigured state in this app uses.
  import { createEventDispatcher, onDestroy, onMount } from "svelte";
  import { Sparkles } from "@lucide/svelte";
  import type { MeetingCatalogEntry } from "cassini-viewer/dataProvider";

  import NeedsSetupCard from "./NeedsSetupCard.svelte";
  import type { OperatorClient } from "./operator/client";
  import type { InsightWorkflow } from "./operator/types";
  import type { OperatorPanel } from "./surfaceRouting";
  import {
    buildRunFailureNotice,
    createInsight,
    describeRunProgress,
    isTerminalStatus,
    listInsights,
    pollDelayMs,
    readInsight,
    retryInsight,
    workflowTakesQuestion,
    InsightRequestError,
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

  let workflows: InsightWorkflow[] = [];
  // A registry that could not be read is not a registry with nothing in it.
  // Either way Generate still works — an insight with no template named runs
  // whatever the deployment configured — so this narrows the picker rather than
  // blocking the card.
  let workflowsError = "";
  let workflowsAsked = false;
  let chosenWorkflow = "";

  let question = "";

  let creating = false;
  let createError = "";

  // Newest first, which is the order the operator lists them in and the order a
  // run you just started has to appear in.
  let runs: InsightRun[] = [];
  let listError = "";
  // A poll that failed is not a run that failed: the run is still whatever the
  // operator says it is, and the only honest thing to report is that the
  // question could not be asked this time.
  let pollError = "";
  // The document arrives with the status — GET insights/<id> carries both — so
  // a finished insight can be read without a second round trip.
  let documents: Record<string, string> = {};
  let retrying: Record<string, boolean> = {};

  // At most five, because this is a side panel and not the insight list that
  // drop 6 builds. When there are more, the card says so rather than quietly
  // truncating.
  const VISIBLE_RUNS = 5;

  let pollTimer: ReturnType<typeof setTimeout> | null = null;
  let pollRound = 0;
  // Set on destroy, and checked after every await. Clearing the timer alone
  // would still let an in-flight poll reschedule itself after the panel closed,
  // which is a request loop with no component behind it.
  let stopped = false;

  $: meetingIds = entries.map((entry) => entry.id);
  $: generateLabel =
    entries.length === 1 ? "Generate from this meeting" : `Generate from ${entries.length} meetings`;
  $: visibleRuns = runs.slice(0, VISIBLE_RUNS);
  $: hiddenRunCount = Math.max(0, runs.length - VISIBLE_RUNS);
  // The probe that decides whether there is an operator surface at all is the
  // same one that produced this client. There is no second notion of admin here
  // to drift from the first.
  $: isAdmin = operatorClient !== null;
  $: if (operatorClient && !workflowsAsked) {
    void loadWorkflows();
  }

  // Whether this run may carry a question of your own, decided against the
  // registry's own bytes rather than assumed.
  //
  // `POST insights` refuses a question a workflow has no slot for AND a workflow
  // that needs one with none given, so an unconditional box is a control every
  // use of which is a 400: no prompt this image ships carries the placeholder
  // (internal/insight/workflows/workflows.go states that outright), so today
  // there is nothing to type into and the box is absent. The day a
  // question-taking workflow ships it appears by itself.
  //
  // It can only be answered for a workflow this card can actually see, which is
  // one an ADMIN picked: the registry is ADMIN at the proxy, and "this
  // deployment's default" is resolved server-side out of the LLM settings, which
  // are ADMIN too. So a deployment that later configures a question-taking
  // template as its default is told so by the operator's own 400 rather than
  // guessed at here — loud, and about the one thing this side cannot know.
  $: chosenWorkflowEntry = workflows.find((workflow) => workflow.id === chosenWorkflow) ?? null;
  $: questionAccepted = chosenWorkflow !== "" && workflowTakesQuestion(chosenWorkflowEntry);
  // A workflow with a slot for a question is a workflow that cannot run without
  // one, so Generate waits for it rather than sending a request the operator
  // will refuse.
  $: questionMissing = questionAccepted && question.trim() === "";

  onMount(() => {
    void loadRuns();
  });

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
    } catch (error) {
      workflowsError = describe(error);
    }
  }

  async function loadRuns() {
    listError = "";
    try {
      runs = await listInsights();
      schedulePoll({ reset: true });
    } catch (error) {
      // An insight route this operator does not serve answers 404, which reads
      // as "no insights" if it is swallowed. It is not: it is a deployment that
      // cannot run one, and the reader is owed the difference.
      listError = describe(error);
    }
  }

  async function generate() {
    if (creating || meetingIds.length === 0 || questionMissing) {
      return;
    }
    creating = true;
    createError = "";
    try {
      const run = await createInsight({
        meetingIds,
        workflow: chosenWorkflow,
        // Only ever sent to a workflow with somewhere to put it. Text left in
        // the box by a template that was then switched away from must not ride
        // along into one that would be refused for carrying it.
        question: questionAccepted ? question : "",
      });
      // In the list before anything has happened to it, which is the whole
      // point of a record that exists before its content does: otherwise the
      // row materialises out of nowhere a minute later.
      runs = [run, ...runs.filter((existing) => existing.id !== run.id)];
      question = "";
      schedulePoll({ reset: true });
    } catch (error) {
      createError = describe(error);
    } finally {
      creating = false;
    }
  }

  async function retry(run: InsightRun) {
    if (retrying[run.id]) {
      return;
    }
    retrying = { ...retrying, [run.id]: true };
    pollError = "";
    try {
      replaceRun(await retryInsight(run.id));
      schedulePoll({ reset: true });
    } catch (error) {
      if (error instanceof InsightRequestError && error.status === 409) {
        // The status is the lock, and 409 means the run is already moving —
        // which is what the reader wanted. Re-read it rather than paint an
        // error over a run that is doing the right thing.
        pollError = "";
        void refresh(run.id);
      } else {
        pollError = describe(error);
      }
    } finally {
      retrying = { ...retrying, [run.id]: false };
    }
  }

  async function refresh(id: string) {
    try {
      const { run, document } = await readInsight(id);
      if (stopped) {
        return;
      }
      replaceRun(run);
      if (document !== "") {
        documents = { ...documents, [run.id]: document };
      }
      schedulePoll({ reset: true });
    } catch (error) {
      if (!stopped) {
        pollError = describe(error);
      }
    }
  }

  function replaceRun(next: InsightRun) {
    const index = runs.findIndex((run) => run.id === next.id);
    if (index < 0) {
      runs = [next, ...runs];
      return;
    }
    runs = runs.map((run, i) => (i === index ? next : run));
  }

  function pendingRuns(): InsightRun[] {
    return runs.filter((run) => !isTerminalStatus(run.status));
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
    // No timer at all while everything is finished. Polling stops because there
    // is nothing left to ask about, not because a counter ran out.
    if (pendingRuns().length === 0) {
      return;
    }
    pollTimer = setTimeout(() => {
      pollTimer = null;
      void poll();
    }, pollDelayMs(pollRound));
  }

  async function poll() {
    const pending = pendingRuns();
    if (stopped || pending.length === 0) {
      return;
    }
    let moved = false;
    let failedToAsk = "";
    for (const run of pending) {
      if (stopped) {
        return;
      }
      try {
        const { run: fresh, document } = await readInsight(run.id);
        if (fresh.status !== run.status || fresh.attemptNumber !== run.attemptNumber) {
          moved = true;
        }
        replaceRun(fresh);
        if (document !== "") {
          documents = { ...documents, [fresh.id]: document };
        }
      } catch (error) {
        failedToAsk = describe(error);
      }
    }
    if (stopped) {
      return;
    }
    pollError = failedToAsk;
    // A run that moved is asked about promptly again; one that has not is asked
    // about less and less, up to the cap.
    pollRound = moved ? 0 : pollRound + 1;
    schedulePoll();
  }

  function describe(error: unknown): string {
    return error instanceof Error ? error.message : String(error);
  }

  // What names a run in the list: the question it asked, else the template's
  // display name.
  //
  // The registry is ADMIN at the proxy, so a non-admin has no names to look up
  // and the raw id would put "summarise" and "todos" in front of them where an
  // administrator reading the same rows sees "Meeting summary" and "Open
  // actions" — two vocabularies for one thing. Where there is no name to be had,
  // the picker's own substitute is the honest one: say which template ran
  // without inventing a word for it. An admin whose registry read merely failed
  // keeps the id, which is the most they can be told and is a word they can act
  // on.
  function workflowLabel(run: InsightRun): string {
    if (run.question.trim() !== "") {
      return `“${run.question.trim()}”`;
    }
    const known = workflows.find((workflow) => workflow.id === run.workflowId);
    if (known) {
      return known.name;
    }
    return isAdmin ? run.workflowId : "The template your administrator configured";
  }

  function handleOpenPanel(event: CustomEvent<{ panel: OperatorPanel; href: string }>) {
    dispatch("open", event.detail);
  }
</script>

<!-- Nothing to ask about, nothing to render: an empty selection is not a
     question, and the Prepare panel is closed by the browse surface as soon as
     the last meeting is unpicked anyway. -->
{#if entries.length > 0}
  <section class="rounded-box border border-base-300 bg-base-200 p-3" aria-label="Generate an insight">
    <h3 class="flex items-center gap-1.5 text-sm font-medium">
      <Sparkles size={14} class="shrink-0 text-primary" aria-hidden="true" />
      Ask these meetings a question
    </h3>

    <div class="mt-2 grid gap-2">
      {#if isAdmin}
        <label class="grid gap-1">
          <span class="text-xs text-base-content/70">Template</span>
          <select class="select select-sm select-bordered w-full" bind:value={chosenWorkflow}>
            <!-- The empty value is not "none": it is the deployment's own
                 configured template, which is exactly what a non-admin gets. -->
            <option value="">This deployment's default</option>
            {#each workflows as workflow (workflow.id)}
              <option value={workflow.id}>{workflow.name}</option>
            {/each}
          </select>
        </label>
        {#if workflowsError}
          <p class="text-xs text-warning">
            The template list could not be read, so only the deployment's default is offered here.
            {workflowsError}
          </p>
        {/if}
      {:else}
        <!-- The template registry is ADMIN at the proxy, so there is no picker
             to offer. Saying which template runs is the honest substitute for a
             control that would 403 when opened. -->
        <p class="text-xs text-base-content/70">
          This runs the template your administrator configured for insights.
        </p>
      {/if}

      <!-- Offered only by a template that has somewhere to put it. A box whose
           every submission is refused would be worse than none, and no prompt
           this image ships takes a question yet — see questionAccepted. -->
      {#if questionAccepted}
        <label class="grid gap-1">
          <span class="text-xs text-base-content/70">Ask your own question</span>
          <textarea
            class="textarea textarea-sm textarea-bordered w-full"
            rows="2"
            placeholder="What did we decide about pricing?"
            bind:value={question}
          ></textarea>
        </label>
        <!-- The prototype's freeform box reads as a saveable prompt. It is not:
             the question is recorded on the insight it produced and nowhere
             else, and templates ship with Cassini. -->
        <p class="text-xs text-base-content/60">
          Your question is recorded with the insight it produces. It is not saved as a template.
        </p>
      {/if}

      <button
        class="btn btn-primary btn-sm"
        type="button"
        disabled={creating || questionMissing}
        on:click={generate}
      >
        {creating ? "Starting…" : generateLabel}
      </button>
      {#if questionMissing}
        <p class="text-xs text-base-content/70">
          This template asks whatever you type above, so it needs a question.
        </p>
      {/if}

      <!-- The instance's key pays for this, so a run is attributable to the
           deployment rather than to the person who asked. Said here rather than
           discovered from a bill (D-700). -->
      <p class="text-xs text-base-content/60">
        The transcripts of these meetings are sent to this deployment's configured AI endpoint, and
        the insight is written into your own Nextcloud files.
      </p>

      {#if createError}
        <p class="text-xs text-error" role="alert">{createError}</p>
      {/if}
      {#if listError}
        <p class="text-xs text-warning" role="status">{listError}</p>
      {/if}
      {#if pollError}
        <!-- Deliberately not an error on the run: the run is whatever the
             operator says it is, and this says only that it could not be asked. -->
        <p class="text-xs text-warning" role="status">{pollError}</p>
      {/if}
    </div>

    {#if visibleRuns.length > 0}
      <ul class="mt-3 grid list-none gap-2 p-0">
        {#each visibleRuns as run (run.id)}
          <li class="rounded-box border border-base-300 bg-base-100 p-2.5">
            <div class="flex flex-wrap items-center justify-between gap-x-2 gap-y-1">
              <span class="min-w-0 truncate text-xs font-medium">{workflowLabel(run)}</span>
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
              {#if documents[run.id]}
                <details class="mt-1">
                  <summary class="cursor-pointer text-xs">Read it here</summary>
                  <pre
                    class="mt-1 max-h-72 overflow-auto rounded-box border border-base-300 bg-base-200 p-2 text-xs whitespace-pre-wrap">{documents[run.id]}</pre>
                </details>
              {/if}
            {/if}

            {#if run.status === "failed"}
              <div class="mt-1.5 grid gap-1.5">
                <!-- The same card, and the same route-preserving deep link,
                     that every other "this deployment cannot do that yet" state
                     renders. Its words come from buildRunFailureNotice, where a
                     test can reach them. -->
                <NeedsSetupCard notice={buildRunFailureNotice({ run, isAdmin })} on:open={handleOpenPanel} />
                <div class="flex items-center gap-2">
                  <button
                    class="btn btn-outline btn-xs"
                    type="button"
                    disabled={retrying[run.id]}
                    on:click={() => retry(run)}
                  >
                    {retrying[run.id] ? "Retrying…" : "Retry"}
                  </button>
                  <!-- Retry re-resolves the endpoint and model from current
                       settings, which is what makes "add a key" a fix rather
                       than a suggestion. Saying otherwise would promise a
                       replay of the run that failed. -->
                  <span class="text-xs text-base-content/60">
                    Uses the endpoint and model configured now.
                  </span>
                </div>
              </div>
            {/if}
          </li>
        {/each}
      </ul>
      {#if hiddenRunCount > 0}
        <p class="mt-1.5 text-xs text-base-content/60">
          {hiddenRunCount} older {hiddenRunCount === 1 ? "insight is" : "insights are"} not shown here.
        </p>
      {/if}
    {/if}
  </section>
{/if}

<style>
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
