<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import { List, RefreshCw, Server, Square, TriangleAlert } from "@lucide/svelte";
  import { loadConfig } from "./operator/config";
  import { OperatorClient, OperatorHttpError } from "./operator/client";
  import type { Job, JobAttempt, JobDetailResponse } from "./operator/types";

  const POLL_INTERVAL_MS = 2000;

  let operatorUrl = "";
  let operatorClient: OperatorClient | null = null;
  let configError = "";
  let jobsError = "";
  let detailError = "";
  let actionError = "";
  let loadingJobs = true;
  let loadingDetail = false;
  let submittingStart = false;
  let submittingStop = false;
  let jobs: Job[] = [];
  let selectedJobId = "";
  let selectedJob: JobDetailResponse | null = null;
  let meetingUrl = "";
  let pollTimer = 0;

  onMount(async () => {
    try {
      const config = loadConfig();
      operatorUrl = config.operatorUrl;
      operatorClient = new OperatorClient(config.operatorUrl);
      await refreshJobs();
    } catch (error) {
      configError = error instanceof Error ? error.message : String(error);
      loadingJobs = false;
    }
  });

  onDestroy(() => {
    clearPolling();
  });

  async function refreshJobs() {
    if (!operatorClient) {
      return;
    }
    loadingJobs = true;
    jobsError = "";
    try {
      jobs = await operatorClient.listJobs();
      if (jobs.length === 0) {
        selectedJobId = "";
        selectedJob = null;
        return;
      }
      const preferredJobId = selectedJobId || jobs[0]?.id || "";
      if (preferredJobId) {
        await selectJob(preferredJobId, { allowJobsRefresh: false });
      }
    } catch (error) {
      jobsError = asMessage(error);
    } finally {
      loadingJobs = false;
      updatePolling();
    }
  }

  async function selectJob(jobId: string, options: { allowJobsRefresh?: boolean } = {}) {
    if (!operatorClient || jobId === "") {
      return;
    }
    if (options.allowJobsRefresh !== false) {
      const matchingJob = jobs.find((job) => job.id === jobId);
      if (!matchingJob) {
        await refreshJobs();
      }
    }
    selectedJobId = jobId;
    detailError = "";
    loadingDetail = true;
    try {
      selectedJob = await operatorClient.getJobDetail(jobId);
    } catch (error) {
      detailError = asMessage(error);
      selectedJob = null;
    } finally {
      loadingDetail = false;
      updatePolling();
    }
  }

  async function handleStartJob() {
    const trimmedUrl = meetingUrl.trim();
    if (!operatorClient || trimmedUrl === "") {
      actionError = "Meeting URL is required.";
      return;
    }
    submittingStart = true;
    actionError = "";
    try {
      const { id } = await operatorClient.startJob(trimmedUrl);
      meetingUrl = "";
      await refreshJobs();
      await selectJob(id, { allowJobsRefresh: false });
    } catch (error) {
      actionError = asMessage(error);
    } finally {
      submittingStart = false;
      updatePolling();
    }
  }

  async function handleStopJob() {
    if (!operatorClient || !selectedJob?.job || !canStopSelectedJob) {
      return;
    }
    submittingStop = true;
    actionError = "";
    try {
      await operatorClient.stopJob(selectedJob.job.id);
      await refreshJobs();
      await selectJob(selectedJob.job.id, { allowJobsRefresh: false });
    } catch (error) {
      actionError = asMessage(error);
    } finally {
      submittingStop = false;
      updatePolling();
    }
  }

  function updatePolling() {
    clearPolling();
    if (!selectedJob || !isJobActive(selectedJob.job)) {
      return;
    }
    pollTimer = window.setTimeout(async () => {
      await refreshJobs();
    }, POLL_INTERVAL_MS);
  }

  function clearPolling() {
    if (pollTimer !== 0) {
      window.clearTimeout(pollTimer);
      pollTimer = 0;
    }
  }

  function isJobActive(job: Job): boolean {
    return job.stage !== "done";
  }

  function asMessage(error: unknown): string {
    if (error instanceof OperatorHttpError) {
      return error.message;
    }
    return error instanceof Error ? error.message : String(error);
  }

  function formatTimestamp(value: string | null): string {
    if (!value) {
      return "—";
    }
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) {
      return value;
    }
    return new Intl.DateTimeFormat(undefined, {
      dateStyle: "medium",
      timeStyle: "medium",
    }).format(date);
  }

  function formatStageState(job: Pick<Job, "stage" | "state">): string {
    return `${job.stage} / ${job.state}`;
  }

  function attemptStageLabel(attempt: JobAttempt): string {
    return `${attempt.attempt_number} · ${attempt.stage} / ${attempt.state}`;
  }

  function requestUrlLabel(requestJSON: string): string {
    try {
      const payload = JSON.parse(requestJSON) as { url?: unknown };
      return typeof payload.url === "string" && payload.url.trim() !== "" ? payload.url : "—";
    } catch {
      return "—";
    }
  }

  $: canStartJob = !submittingStart && meetingUrl.trim() !== "";
  $: canStopSelectedJob =
    !submittingStop &&
    selectedJob?.job.stage === "record" &&
    selectedJob?.job.state === "running";
</script>

<svelte:head>
  <title>Cassini Control Panel</title>
  <meta
    name="description"
    content="Operator control panel for Cassini jobs, history, and selected-run inspection."
  />
</svelte:head>

<div class="min-h-screen bg-base-200 text-base-content">
  <div class="mx-auto flex min-h-screen max-w-7xl flex-col gap-6 px-4 py-6 lg:px-6">
    <header class="flex flex-col gap-3 rounded-box border border-base-300 bg-base-100 p-4 shadow-sm">
      <div class="flex items-center gap-3">
        <div class="rounded-box border border-base-300 bg-base-200 p-2">
          <Server size={18} aria-hidden="true" />
        </div>
        <div>
          <h1 class="text-xl font-semibold">Cassini Control Panel</h1>
          <p class="text-sm text-base-content/70">
            Snapshot-driven history, trigger, and stop controls over the existing operator API.
          </p>
        </div>
      </div>
      <div class="text-xs text-base-content/60">
        Operator URL: <code>{operatorUrl || "(not configured)"}</code>
      </div>
    </header>

    {#if configError}
      <section class="alert alert-error">
        <TriangleAlert size={16} aria-hidden="true" />
        <span>{configError}</span>
      </section>
    {:else}
      <section class="rounded-box border border-base-300 bg-base-100 p-4 shadow-sm">
        <div class="grid gap-3 lg:grid-cols-[minmax(0,1fr)_auto] lg:items-end">
          <label class="form-control w-full gap-2">
            <span class="label-text font-medium">Meeting URL</span>
            <input
              bind:value={meetingUrl}
              type="url"
              class="input input-bordered w-full"
              placeholder="https://nextcloud.example.test/call/..."
            />
          </label>
          <div class="flex flex-col gap-2">
            <button class="btn btn-primary" disabled={!canStartJob} type="button" on:click={handleStartJob}>
              {#if submittingStart}
                <span class="loading loading-spinner loading-sm" aria-hidden="true"></span>
                Starting…
              {:else}
                Start job
              {/if}
            </button>
            <p class="text-xs text-base-content/60 lg:text-right">
              Uses <code>POST /jobs?provider=nextcloud-talk</code> and refreshes snapshots while active.
            </p>
          </div>
        </div>
        {#if actionError}
          <div class="mt-3 alert alert-error text-sm">{actionError}</div>
        {/if}
      </section>

      <div class="grid min-h-0 flex-1 gap-6 lg:grid-cols-[minmax(22rem,30rem)_minmax(0,1fr)]">
        <section class="flex min-h-[24rem] flex-col rounded-box border border-base-300 bg-base-100 shadow-sm">
          <header class="flex items-center justify-between gap-3 border-b border-base-300 px-4 py-3">
            <div class="flex items-center gap-2">
              <List size={16} aria-hidden="true" />
              <div>
                <h2 class="font-semibold">Jobs history</h2>
                <p class="text-xs text-base-content/60">Newest-first logical runs from <code>GET /jobs</code>.</p>
              </div>
            </div>
            <button class="btn btn-ghost btn-sm" on:click={refreshJobs} type="button" aria-label="Refresh jobs">
              <RefreshCw size={16} aria-hidden="true" />
            </button>
          </header>

          {#if jobsError}
            <div class="px-4 py-4">
              <div class="alert alert-error text-sm">{jobsError}</div>
            </div>
          {:else if loadingJobs}
            <div class="flex flex-1 items-center justify-center p-6 text-sm text-base-content/60">Loading jobs…</div>
          {:else if jobs.length === 0}
            <div class="flex flex-1 items-center justify-center p-6 text-sm text-base-content/60">No jobs yet.</div>
          {:else}
            <div class="min-h-0 flex-1 overflow-y-auto p-3">
              <div class="grid gap-2">
                {#each jobs as job}
                  <button
                    class="card border border-base-300 bg-base-100 text-left transition hover:border-primary {job.id === selectedJobId ? 'border-primary bg-primary/5' : ''}"
                    type="button"
                    on:click={() => selectJob(job.id)}
                  >
                    <div class="card-body gap-2 p-4">
                      <div class="flex items-start justify-between gap-3">
                        <div class="min-w-0">
                          <p class="truncate font-mono text-xs">{job.id}</p>
                          <p class="text-sm font-medium">{formatStageState(job)}</p>
                        </div>
                        <div class="flex items-center gap-2">
                          {#if isJobActive(job)}
                            <span class="badge badge-warning badge-outline">Active</span>
                          {/if}
                          <span class="badge badge-outline">Attempt {job.current_attempt_number}</span>
                        </div>
                      </div>
                      <div class="text-xs text-base-content/70">
                        <p class="truncate">{requestUrlLabel(job.request_json)}</p>
                        <p>Updated {formatTimestamp(job.updated_at)}</p>
                        {#if job.error}
                          <p class="truncate text-error">{job.error}</p>
                        {/if}
                      </div>
                    </div>
                  </button>
                {/each}
              </div>
            </div>
          {/if}
        </section>

        <section class="flex min-h-[24rem] flex-col rounded-box border border-base-300 bg-base-100 shadow-sm">
          <header class="flex items-center justify-between gap-3 border-b border-base-300 px-4 py-3">
            <div>
              <h2 class="font-semibold">Selected run detail</h2>
              <p class="text-xs text-base-content/60">Summary row + attempt history from <code>GET /jobs/:id</code>.</p>
            </div>
            <button
              class="btn btn-outline btn-sm"
              disabled={!canStopSelectedJob}
              type="button"
              on:click={handleStopJob}
            >
              {#if submittingStop}
                <span class="loading loading-spinner loading-sm" aria-hidden="true"></span>
                Stopping…
              {:else}
                <Square size={14} aria-hidden="true" />
                Stop
              {/if}
            </button>
          </header>

          {#if detailError}
            <div class="px-4 py-4">
              <div class="alert alert-error text-sm">{detailError}</div>
            </div>
          {:else if loadingDetail}
            <div class="flex flex-1 items-center justify-center p-6 text-sm text-base-content/60">Loading selected run…</div>
          {:else if !selectedJob}
            <div class="flex flex-1 items-center justify-center p-6 text-sm text-base-content/60">Select a run from the history list.</div>
          {:else}
            <div class="min-h-0 flex-1 overflow-y-auto p-4">
              <div class="grid gap-4">
                <section class="grid gap-3 rounded-box border border-base-300 bg-base-200/50 p-4">
                  <div>
                    <p class="font-mono text-xs">{selectedJob.job.id}</p>
                    <h3 class="text-lg font-semibold">{formatStageState(selectedJob.job)}</h3>
                  </div>
                  <dl class="grid gap-3 md:grid-cols-2">
                    <div>
                      <dt class="text-xs uppercase tracking-wide text-base-content/60">Provider</dt>
                      <dd class="text-sm">{selectedJob.job.provider}</dd>
                    </div>
                    <div>
                      <dt class="text-xs uppercase tracking-wide text-base-content/60">Meeting URL</dt>
                      <dd class="text-sm break-all">{requestUrlLabel(selectedJob.job.request_json)}</dd>
                    </div>
                    <div>
                      <dt class="text-xs uppercase tracking-wide text-base-content/60">Current attempt</dt>
                      <dd class="text-sm">{selectedJob.job.current_attempt_number}</dd>
                    </div>
                    <div>
                      <dt class="text-xs uppercase tracking-wide text-base-content/60">Reruns</dt>
                      <dd class="text-sm">{selectedJob.job.rerun_count}</dd>
                    </div>
                    <div>
                      <dt class="text-xs uppercase tracking-wide text-base-content/60">Created</dt>
                      <dd class="text-sm">{formatTimestamp(selectedJob.job.created_at)}</dd>
                    </div>
                    <div>
                      <dt class="text-xs uppercase tracking-wide text-base-content/60">Updated</dt>
                      <dd class="text-sm">{formatTimestamp(selectedJob.job.updated_at)}</dd>
                    </div>
                    <div>
                      <dt class="text-xs uppercase tracking-wide text-base-content/60">Completed</dt>
                      <dd class="text-sm">{formatTimestamp(selectedJob.job.completed_at)}</dd>
                    </div>
                    <div>
                      <dt class="text-xs uppercase tracking-wide text-base-content/60">Interrupted</dt>
                      <dd class="text-sm">{formatTimestamp(selectedJob.job.interrupted_at)}</dd>
                    </div>
                  </dl>
                  {#if selectedJob.job.stop_reason || selectedJob.job.stop_requested_at}
                    <div class="rounded-box border border-base-300 bg-base-100 p-3 text-sm">
                      <p><span class="font-medium">Stop reason:</span> {selectedJob.job.stop_reason ?? "—"}</p>
                      <p><span class="font-medium">Stop requested:</span> {formatTimestamp(selectedJob.job.stop_requested_at)}</p>
                    </div>
                  {/if}
                  {#if selectedJob.job.error}
                    <div class="alert alert-error text-sm">{selectedJob.job.error}</div>
                  {/if}
                </section>

                <section class="grid gap-3">
                  <div class="flex items-center justify-between gap-3">
                    <h3 class="font-semibold">Attempt history</h3>
                    <span class="badge badge-outline">{selectedJob.attempts.length} attempts</span>
                  </div>
                  <div class="grid gap-3">
                    {#each selectedJob.attempts as attempt}
                      <article class="rounded-box border border-base-300 bg-base-100 p-4">
                        <div class="flex items-start justify-between gap-3">
                          <div>
                            <p class="font-medium">Attempt {attempt.attempt_number}</p>
                            <p class="text-sm text-base-content/70">{attempt.trigger_kind} · {attemptStageLabel(attempt)}</p>
                          </div>
                          <span class="badge badge-outline">{attempt.state}</span>
                        </div>
                        <dl class="mt-3 grid gap-3 md:grid-cols-2">
                          <div>
                            <dt class="text-xs uppercase tracking-wide text-base-content/60">Queued</dt>
                            <dd class="text-sm">{formatTimestamp(attempt.record_queued_at)}</dd>
                          </div>
                          <div>
                            <dt class="text-xs uppercase tracking-wide text-base-content/60">Completed</dt>
                            <dd class="text-sm">{formatTimestamp(attempt.completed_at)}</dd>
                          </div>
                          <div>
                            <dt class="text-xs uppercase tracking-wide text-base-content/60">Run artifact</dt>
                            <dd class="text-sm break-all">{attempt.artifact_run_path ?? "—"}</dd>
                          </div>
                          <div>
                            <dt class="text-xs uppercase tracking-wide text-base-content/60">Meeting artifact</dt>
                            <dd class="text-sm break-all">{attempt.artifact_meeting_path ?? "—"}</dd>
                          </div>
                        </dl>
                        {#if attempt.error}
                          <div class="mt-3 alert alert-error text-sm">{attempt.error}</div>
                        {/if}
                      </article>
                    {/each}
                  </div>
                </section>
              </div>
            </div>
          {/if}
        </section>
      </div>
    {/if}
  </div>
</div>
