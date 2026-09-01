<script module lang="ts">
  import type { Job } from "./operator/types";

  type JobStatus = Pick<Job, "stage" | "state" | "build_retry_not_before">;
  type RerunnableJob = Pick<Job, "stage" | "state" | "artifact_run_path">;

  const STATUS_STAGE_LABELS: Record<string, string> = {
    record: "Record",
    build: "Build",
    seal: "Seal",
    publish: "Publish",
  };

  export function isBuildWaitingForGPU(job: JobStatus): boolean {
    return job.stage === "build" && job.state === "queued" && job.build_retry_not_before != null;
  }

  export function isBuildGPUBlocked(job: JobStatus): boolean {
    return job.stage === "build" && job.state === "blocked";
  }

  export function hasGPUResourceNotice(job: JobStatus): boolean {
    return isBuildWaitingForGPU(job) || isBuildGPUBlocked(job);
  }

  export function jobStatusLabel(job: JobStatus): string {
    if (isBuildGPUBlocked(job)) return "GPU unavailable";
    if (isBuildWaitingForGPU(job)) return "Waiting for GPU";
    if (job.state === "failed") return "Failed";
    if (job.state === "stopped") return "Stopped";
    if (job.state === "interrupted") return "Interrupted";
    if (job.state === "succeeded" && job.stage === "done") return "Published";
    if (job.state === "queued") return "Queued";
    if (job.state === "running") {
      const stage = STATUS_STAGE_LABELS[job.stage];
      return stage ? `${stage}ing` : "Running";
    }
    return `${job.stage} / ${job.state}`;
  }

  export function jobStatusToneClass(job: JobStatus): string {
    if (hasGPUResourceNotice(job) || job.state === "stopped") return "text-warning";
    if (job.state === "failed" || job.state === "interrupted") return "text-error";
    if (job.state === "succeeded") return "text-success";
    return "text-base-content/70";
  }

  export function isRerunnableJob(job: RerunnableJob): boolean {
    const terminalState = job.stage === "done" || (job.stage === "build" && job.state === "blocked");
    return terminalState && !!job.artifact_run_path;
  }

  export function isJobActive(job: Pick<Job, "stage" | "state">): boolean {
    return job.stage !== "done" && job.state !== "blocked";
  }
</script>

<script lang="ts">
  import { onDestroy, onMount, tick } from "svelte";
  import { Activity, ArrowLeft, CassetteTape, ChevronRight, Inbox, RefreshCw, Square, TriangleAlert } from "@lucide/svelte";
  import { loadConfig } from "./operator/config";
  import {
    OperatorClient,
    OperatorHttpError,
    type OperatorStateChangeEvent,
  } from "./operator/client";
  import type { Job, JobAttempt, JobDetailResponse } from "./operator/types";
  import { shouldShowDetailLoading } from "./operator/viewState";
  import { applyJob, readJob } from "./surfaceRouting";
  import SettingsPanel from "./SettingsPanel.svelte";

  const POLL_INTERVAL_MS = 2000;
  // If the SSE stream doesn't reach "open" within this window, assume the
  // environment can't stream it — notably Nextcloud's AppAPI proxy, which
  // buffers Server-Sent Events so the response headers never reach the browser
  // and EventSource sits in CONNECTING silently — and fall back to polling
  // instead of showing "connecting" forever.
  const STREAM_OPEN_TIMEOUT_MS = 5000;

  let operatorBasePath = "";
  let operatorClient: OperatorClient | null = null;
  let eventSource: EventSource | null = null;
  let hasSeenStreamOpen = false;
  let streamStatus:
    | "idle"
    | "connecting"
    | "connected"
    | "reconnecting"
    | "disconnected"
    | "unavailable" = "idle";

  let configError = "";
  let jobsError = "";
  let detailError = "";
  let actionError = "";
  let loadingJobs = true;
  let loadingDetail = false;
  let submittingStart = false;
  let submittingStop = false;
  let submittingRerun = false;
  let jobs: Job[] = [];
  let selectedJobId = "";
  let selectedJob: JobDetailResponse | null = null;
  let meetingUrl = "";

  let showComposer = false;
  let meetingUrlInput: HTMLInputElement | null = null;
  let pollTimer = 0;
  let streamOpenTimer = 0;

  const DESKTOP_MEDIA_QUERY = "(min-width: 981px)";
  let isDesktop = false;
  let viewportMedia: MediaQueryList | null = null;

  onMount(async () => {
    if (typeof window.matchMedia === "function") {
      viewportMedia = window.matchMedia(DESKTOP_MEDIA_QUERY);
      isDesktop = viewportMedia.matches;
      viewportMedia.addEventListener("change", handleViewportChange);
    }
    window.addEventListener("popstate", handleJobPopState);
    seedListHistoryEntry();
    try {
      const config = loadConfig();
      operatorBasePath = config.operatorBasePath;
      operatorClient = new OperatorClient(config.operatorBasePath);
      await refreshJobs();
      openEventStream();
    } catch (error) {
      configError = error instanceof Error ? error.message : String(error);
      loadingJobs = false;
    }
  });

  onDestroy(() => {
    clearPolling();
    closeEventStream();
    window.removeEventListener("popstate", handleJobPopState);
    viewportMedia?.removeEventListener("change", handleViewportChange);
  });

  function seedListHistoryEntry() {
    const deepLinkedJob = readJob(window.location.hash);
    if (deepLinkedJob === "") {
      return;
    }
    window.history.replaceState({}, "", operatorHref(""));
    window.history.pushState({}, "", operatorHref(deepLinkedJob));
  }

  async function refreshJobs() {
    if (!operatorClient) {
      return;
    }
    loadingJobs = true;
    jobsError = "";
    try {
      jobs = sortJobs(await operatorClient.listJobs());
      if (jobs.length === 0) {
        selectedJobId = "";
        selectedJob = null;
        return;
      }
      const deepLinkedJob = readJob(window.location.hash);
      const preferredJobId = deepLinkedJob || selectedJobId;
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

  async function openComposer() {
    showComposer = true;
    await tick();
    meetingUrlInput?.focus();
  }

  function operatorHref(jobId: string): string {
    const url = new URL(window.location.href);
    url.hash = applyJob(window.location.hash, jobId).replace(/^#/, "");
    return url.toString();
  }

  function pushJobUrl(jobId: string) {
    window.history.pushState({}, "", operatorHref(jobId));
  }

  async function openJob(jobId: string) {
    if (jobId === selectedJobId) {
      return;
    }
    pushJobUrl(jobId);
    await selectJob(jobId);
  }

  function handleBackToList() {
    pushJobUrl("");
    selectedJobId = "";
    selectedJob = null;
    detailError = "";
  }

  function handleJobPopState() {
    const urlJobId = readJob(window.location.hash);
    if (urlJobId === selectedJobId) {
      return;
    }
    if (urlJobId === "") {
      selectedJobId = "";
      selectedJob = null;
      detailError = "";
      return;
    }
    void selectJob(urlJobId, { allowJobsRefresh: false });
  }

  function handleViewportChange(event: MediaQueryListEvent) {
    isDesktop = event.matches;
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
      showComposer = false;
      await refreshJobs();
      pushJobUrl(id);
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

  async function handleRerunJob() {
    if (!operatorClient || !selectedJob?.job || !canRerunSelectedJob) {
      return;
    }
    submittingRerun = true;
    actionError = "";
    try {
      await operatorClient.rerunJob(selectedJob.job.id);
      await refreshJobs();
      await selectJob(selectedJob.job.id, { allowJobsRefresh: false });
    } catch (error) {
      actionError = asMessage(error);
    } finally {
      submittingRerun = false;
      updatePolling();
    }
  }

  function openEventStream() {
    if (!operatorClient || typeof EventSource === "undefined") {
      streamStatus = "unavailable";
      updatePolling();
      return;
    }
    closeEventStream();
    streamStatus = hasSeenStreamOpen ? "reconnecting" : "connecting";
    eventSource = operatorClient.openEventStream({
      onOpen: () => {
        clearStreamOpenTimer();
        hasSeenStreamOpen = true;
        streamStatus = "connected";
        void refreshJobs();
      },
      onError: () => {
        streamStatus = hasSeenStreamOpen ? "reconnecting" : "connecting";
        updatePolling();
      },
      onStateChange: (event) => {
        applyStateChangeEvent(event);
      },
    });
    // Guard against a stream that connects but never opens (AppAPI buffers SSE,
    // so no headers arrive and EventSource fires neither onopen nor onerror):
    // after a timeout, give up on live push and rely on polling.
    clearStreamOpenTimer();
    streamOpenTimer = window.setTimeout(() => {
      if (streamStatus !== "connected") {
        closeEventStream();
        streamStatus = "unavailable";
        updatePolling();
      }
    }, STREAM_OPEN_TIMEOUT_MS);
    updatePolling();
  }

  function closeEventStream() {
    clearStreamOpenTimer();
    eventSource?.close();
    eventSource = null;
  }

  function clearStreamOpenTimer() {
    if (streamOpenTimer !== 0) {
      window.clearTimeout(streamOpenTimer);
      streamOpenTimer = 0;
    }
  }

  function applyStateChangeEvent(event: OperatorStateChangeEvent) {
    jobsError = "";
    applyEventToJobsHistory(event);
    applyEventToSelectedJob(event);
  }

  function applyEventToJobsHistory(event: OperatorStateChangeEvent) {
    const nextJobs = jobs.filter((job) => job.id !== event.job.id);
    nextJobs.push(event.job);
    jobs = sortJobs(nextJobs);
  }

  function applyEventToSelectedJob(event: OperatorStateChangeEvent) {
    if (selectedJobId !== event.job_id) {
      return;
    }
    detailError = "";
    if (!selectedJob) {
      void selectJob(event.job_id, { allowJobsRefresh: false });
      return;
    }
    selectedJob = {
      job: event.job,
      attempts: upsertAttempt(selectedJob.attempts, event.attempt),
    };
    updatePolling();
  }

  function upsertAttempt(attempts: JobAttempt[], nextAttempt?: JobAttempt): JobAttempt[] {
    if (!nextAttempt) {
      return attempts;
    }
    const remaining = attempts.filter((attempt) => attempt.attempt_number !== nextAttempt.attempt_number);
    remaining.push(nextAttempt);
    return remaining.sort((a, b) => b.attempt_number - a.attempt_number);
  }

  function sortJobs(nextJobs: Job[]): Job[] {
    return [...nextJobs].sort((left, right) => {
      const created = right.created_at.localeCompare(left.created_at);
      if (created !== 0) {
        return created;
      }
      return right.id.localeCompare(left.id);
    });
  }

  function updatePolling() {
    clearPolling();
    if (streamStatus === "connected") {
      return;
    }
    // Live push isn't carrying updates — poll while anything is still in flight
    // (the selected job, or any active job in the list). refreshJobs re-arms
    // this, so it keeps polling until the work settles.
    const hasActiveWork =
      (selectedJob != null && isJobActive(selectedJob.job)) || jobs.some(isJobActive);
    if (!hasActiveWork) {
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

  // stageTimes below reads `${key}_queued_at` / `_started_at` / `_finished_at`,
  // so a stage appears here only if the operator names its columns to match.
  const STAGES = [
    { key: "record", label: "Record" },
    { key: "build", label: "Build" },
    { key: "seal", label: "Seal" },
    { key: "publish", label: "Publish" },
  ] as const;

  type StageStatus = "pending" | "queued" | "active" | "done" | "failed" | "blocked";

  function stageTimes(job: Job | JobAttempt, key: string) {
    const row = job as unknown as Record<string, string | null | undefined>;
    return {
      queued: row[`${key}_queued_at`] ?? null,
      started: row[`${key}_started_at`] ?? null,
      finished: row[`${key}_finished_at`] ?? null,
    };
  }

  function stageProgress(job: Job | JobAttempt): Array<{ label: string; status: StageStatus }> {
    const gpuBlocked = isBuildGPUBlocked(job);
    const halted = gpuBlocked || job.state === "failed" || job.state === "stopped" || job.state === "interrupted";
    let lastTouched = -1;
    const times = STAGES.map((stage, index) => {
      const t = stageTimes(job, stage.key);
      if (t.queued || t.started || t.finished) {
        lastTouched = index;
      }
      return t;
    });
    return STAGES.map((stage, index) => {
      const t = times[index];
      let status: StageStatus = "pending";
      if (t.finished) {
        status = "done";
      } else if (t.started) {
        status = "active";
      } else if (t.queued) {
        status = "queued";
      }
      if (halted && index === lastTouched) {
        status = gpuBlocked ? "blocked" : "failed";
      }
      return { label: stage.label, status };
    });
  }

  function stageBarClass(status: StageStatus): string {
    switch (status) {
      case "done":
        return "bg-success";
      case "active":
        return "bg-primary";
      case "failed":
        return "bg-error";
      case "blocked":
        return "bg-warning";
      case "queued":
        return "bg-primary/55";
      default:
        return "bg-base-content/12";
    }
  }

  function meetingLabel(requestJSON: string): string {
    const url = requestUrlLabel(requestJSON);
    if (url === "\u2014") {
      return "Recording";
    }
    try {
      const token = new URL(url).pathname.split("/").filter(Boolean).pop();
      return token ? `Call ${token}` : url;
    } catch {
      return url;
    }
  }

  function relativeTime(value: string | null | undefined): string {
    if (!value) return "—";
    const then = Date.parse(value);
    if (Number.isNaN(then)) return "—";
    const seconds = Math.round((Date.now() - then) / 1000);
    if (seconds < 60) return "just now";
    const minutes = Math.round(seconds / 60);
    if (minutes < 60) return `${minutes}m ago`;
    const hours = Math.round(minutes / 60);
    if (hours < 24) return `${hours}h ago`;
    return `${Math.round(hours / 24)}d ago`;
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

  function streamStatusTone(status: typeof streamStatus): string {
    switch (status) {
      case "connected":
        return "badge-success";
      case "reconnecting":
      case "connecting":
        return "badge-warning";
      case "disconnected":
        return "badge-error";
      case "unavailable":
        return "badge-success";
      default:
        return "badge-outline border-base-300 text-base-content";
    }
  }

  function connectionLabel(status: typeof streamStatus): string {
    switch (status) {
      case "connected":
      case "unavailable":
        return "Live";
      case "connecting":
        return "Connecting";
      case "reconnecting":
        return "Reconnecting";
      case "disconnected":
        return "Offline";
      default:
        return "Unknown";
    }
  }

  function streamStatusLabel(status: typeof streamStatus): string {
    switch (status) {
      case "connected":
        return "live";
      case "connecting":
        return "connecting…";
      case "reconnecting":
        return "reconnecting…";
      case "unavailable":
        // The normal mode in Nextcloud (AppAPI can't stream SSE), so state the
        // refresh cadence plainly rather than framing routine operation as a
        // failure.
        return `auto-refreshing every ${POLL_INTERVAL_MS / 1000}s`;
      case "disconnected":
        return "disconnected";
      default:
        return "idle";
    }
  }

  $: canStartJob = !submittingStart && meetingUrl.trim() !== "";
  $: stopApplies = selectedJob?.job.stage === "record" && selectedJob?.job.state === "running";
  $: jobFinished = selectedJob?.job.stage === "done";
  $: rerunVisible = !!selectedJob?.job && (jobFinished || isBuildGPUBlocked(selectedJob.job));
  $: rerunApplies = !!selectedJob?.job && isRerunnableJob(selectedJob.job);
  $: rerunBlockedReason =
    rerunVisible && !selectedJob?.job.artifact_run_path
      ? "This run produced no recording to rerun from."
      : "";
  $: canStopSelectedJob = !submittingStop && stopApplies;
  $: canRerunSelectedJob = !submittingRerun && rerunApplies;
</script>

<svelte:head>
  <title>Cassini Control Panel</title>
  <meta
    name="description"
    content="Operator control panel for Cassini jobs, history, and selected-run inspection."
  />
</svelte:head>

<div class="flex min-h-full flex-col bg-base-200 text-base-content">
  <div class="mx-auto flex min-h-full w-full flex-col gap-4 px-4 pt-4 pb-10">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex flex-wrap items-center gap-2.5 text-sm">
        <span
          class={`badge badge-sm h-auto font-medium rounded-lg gap-1.5 py-1 ${streamStatusTone(streamStatus)}`}
          title={streamStatusLabel(streamStatus)}
        >
          {#if streamStatus === "connecting" || streamStatus === "reconnecting"}
            <span class="loading loading-spinner h-3 w-3" aria-hidden="true"></span>
          {:else}
            <Activity size={12} aria-hidden="true" />
          {/if}
          {connectionLabel(streamStatus)}
        </span>
        {#if streamStatus === "unavailable"}
          <span class="flex items-center gap-1 text-xs text-base-content/60">
            <span class="cassini-tick" style="--cassini-tick: {POLL_INTERVAL_MS}ms">
              <RefreshCw size={12} aria-hidden="true" />
            </span>
            Checking every {POLL_INTERVAL_MS / 1000}s
          </span>
        {:else if streamStatus === "disconnected"}
          <span class="flex items-center gap-1 text-xs text-base-content/60">
            <span class="cassini-tick" style="--cassini-tick: {POLL_INTERVAL_MS}ms">
              <RefreshCw size={12} aria-hidden="true" />
            </span>
            Retrying every {POLL_INTERVAL_MS / 1000}s
          </span>
        {/if}
      </div>
      <button class="btn btn-sm btn-primary md:text-sm" type="button" on:click={openComposer}>
        Record a meeting
      </button>
    </div>

    {#if configError}
      <section class="alert alert-error">
        <TriangleAlert size={16} aria-hidden="true" />
        <span>{configError}</span>
      </section>
    {:else}
      {#if showComposer}
        <section class="mb-6 rounded-box border border-base-300 bg-base-100 px-4 pt-3 pb-4 shadow-sm">
          <div class="grid gap-3 lg:grid-cols-[minmax(0,1fr)_auto] lg:items-end">
            <label class="flex w-full flex-col gap-2">
              <span class="font-semibold">Meeting link</span>
              <input
                bind:value={meetingUrl}
                bind:this={meetingUrlInput}
                type="url"
                class="input input-bordered w-full"
                placeholder="https://nextcloud.example.test/call/..."
              />
            </label>
            <button class="btn btn-primary" disabled={!canStartJob} type="button" on:click={handleStartJob}>
              {#if submittingStart}
                <span class="loading loading-spinner loading-sm" aria-hidden="true"></span>
                Starting…
              {:else}
                Start recording
              {/if}
            </button>
          </div>
          {#if actionError}
            <div class="mt-3 alert alert-error text-sm">{actionError}</div>
          {/if}
        </section>
      {/if}

      {#if operatorClient}
        {#key operatorClient}
          <SettingsPanel {operatorClient} />
        {/key}
      {/if}

      <div class="mt-6 grid rounded-box border border-base-300 bg-base-100 shadow-sm min-[981px]:grid-cols-[minmax(0,26rem)_minmax(0,1fr)]">
        {#if isDesktop || !selectedJobId}
        <section class="flex min-w-0 flex-col overflow-hidden">
          <header class="flex min-h-14 items-center justify-between gap-3 px-4 py-3 min-[981px]:h-14">
            <h2 class="font-semibold">Recordings</h2>
            <button class="btn btn-ghost btn-sm" on:click={refreshJobs} type="button" aria-label="Refresh jobs">
              <RefreshCw size={16} aria-hidden="true" />
            </button>
          </header>

          {#if jobsError}
            <div class="px-4 py-4">
              <div class="alert alert-error text-sm">{jobsError}</div>
            </div>
          {:else if loadingJobs && jobs.length === 0}
            <!-- Only blank to a spinner when there is nothing rendered yet.
                 Background refreshes (the 2s poll fallback and SSE-reconnect
                 reconciles) also flip loadingJobs, but must update the list in
                 place instead of flashing it to "Loading…" every tick (D-494). -->
            <div class="flex flex-1 items-center justify-center p-6 text-sm text-base-content/60">Loading jobs…</div>
          {:else if jobs.length === 0}
            <div class="flex min-h-0 flex-1 p-3">
              <div
                class="flex flex-1 flex-col items-center justify-center gap-2 rounded-box border border-dashed border-base-content/20 p-6 text-center"
              >
                <Inbox size={20} class="text-base-content/40" aria-hidden="true" />
                <p class="text-sm font-medium">No recordings yet</p>
                <p class="text-xs text-base-content/60">Start one with "Record a meeting".</p>
              </div>
            </div>
          {:else}
            <div class="min-h-0 min-w-0 flex-1 min-[981px]:relative">
              <div
                class="overflow-x-hidden p-3 min-[981px]:absolute min-[981px]:inset-0 min-[981px]:overflow-y-auto"
              >
                <div class="recordings-list grid min-w-0 gap-2">
                {#each jobs as job (job.id)}
                  <button
                    class="card w-full min-w-0 overflow-hidden border text-left {job.id === selectedJobId
                      ? 'cursor-default border-base-300 bg-base-200'
                      : 'cursor-pointer border-base-300 bg-base-100 hover:bg-base-200/60'}"
                    type="button"
                    aria-current={job.id === selectedJobId ? "page" : undefined}
                    on:click={() => openJob(job.id)}
                  >
                    <div class="card-body min-w-0 gap-1 p-3">
                      <div class="flex min-w-0 items-start justify-between gap-2">
                        <p class="min-w-0 flex-1 truncate text-sm font-medium" title={requestUrlLabel(job.request_json)}>
                          {meetingLabel(job.request_json)}
                        </p>
                        {#if job.current_attempt_number > 1}
                          <span class="badge badge-outline badge-sm shrink-0 border-base-content/20 text-base-content"
                            >Attempt {job.current_attempt_number}</span
                          >
                        {/if}
                      </div>
                      <div class="min-w-0 text-xs">
                        <p class="truncate">
                          <span class={jobStatusToneClass(job)}>{jobStatusLabel(job)}</span>
                          <span class="text-base-content/50"> · {relativeTime(job.updated_at)}</span>
                        </p>
                        {#if isBuildWaitingForGPU(job)}
                          <p class="truncate text-warning" title={formatTimestamp(job.build_retry_not_before)}>
                            Next retry {formatTimestamp(job.build_retry_not_before)} · {job.build_deferral_count}
                            {job.build_deferral_count === 1 ? "deferral" : "deferrals"}
                          </p>
                        {/if}
                        {#if job.error}
                          <p class="truncate {hasGPUResourceNotice(job) ? 'text-warning' : 'text-base-content/55'}" title={job.error}>
                            {job.error}
                          </p>
                        {/if}
                      </div>
                    </div>
                  </button>
                {/each}
              </div>
            </div>
            </div>
          {/if}
        </section>
        {/if}

        {#if isDesktop || selectedJobId}
        <section
          class="flex min-w-0 flex-col min-[981px]:min-h-[calc(100vh-13rem)] min-[981px]:border-l min-[981px]:border-base-300"
        >
          <header class="flex min-h-14 items-center justify-between gap-3 px-4 py-3 min-[981px]:h-14">
            <div class="flex min-w-0 flex-1 items-center gap-2">
              {#if !isDesktop}
                <button
                  class="btn btn-ghost btn-sm btn-square shrink-0"
                  type="button"
                  on:click={handleBackToList}
                  aria-label="Back to recordings"
                >
                  <ArrowLeft size={16} aria-hidden="true" />
                </button>
              {/if}
              <div class="min-w-0 min-[981px]:flex min-[981px]:items-center min-[981px]:gap-2">
                <h2 class="truncate font-semibold min-[981px]:shrink-0">Run detail</h2>
                <p class="truncate text-xs text-base-content/60 min-[981px]:min-w-0">
                  Status and history for the selected recording
                </p>
              </div>
            </div>
            <div class="flex shrink-0 items-center gap-2">
              {#if stopApplies || submittingStop}
                <button
                  class="btn btn-outline btn-sm text-sm"
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
              {:else if rerunVisible || submittingRerun}
                <button
                  class="btn btn-outline btn-sm text-sm"
                  disabled={!canRerunSelectedJob}
                  title={rerunBlockedReason}
                  type="button"
                  on:click={handleRerunJob}
                >
                  {#if submittingRerun}
                    <span class="loading loading-spinner loading-sm" aria-hidden="true"></span>
                    Rerunning…
                  {:else}
                    <RefreshCw size={14} aria-hidden="true" />
                    Rerun
                  {/if}
                </button>
              {/if}
            </div>
          </header>

          {#if detailError}
            <div class="px-4 py-4">
              <div class="alert alert-error text-sm">{detailError}</div>
            </div>
          {:else if !selectedJob}
            <div class="flex min-h-0 flex-1 px-4 pt-3 pb-4">
              <div
                class="flex flex-1 flex-col items-center justify-center gap-2 rounded-box border border-dashed border-base-content/20 p-6 text-center"
              >
                <CassetteTape size={20} class="text-base-content/40" aria-hidden="true" />
                <p class="text-sm font-medium">No recording selected</p>
                <p class="text-xs text-base-content/60">Choose one from the list.</p>
              </div>
            </div>
          {:else}
            <!-- Dimmed rather than blanked while a different run loads: replacing
                 the content collapsed the pane and jumped the page. The dim still
                 marks it as not-yet-the-new-run (D-494). -->
            <div
              class="px-4 pt-3 pb-4 transition-opacity"
              class:opacity-50={shouldShowDetailLoading(loadingDetail, selectedJob, selectedJobId)}
            >
              <div class="grid gap-4">
                <section class="grid gap-3 rounded-box border border-base-300 bg-base-200 p-4">
                  <div class="min-w-0">
                    <h3 class="truncate text-lg font-semibold" title={requestUrlLabel(selectedJob.job.request_json)}>
                      {meetingLabel(selectedJob.job.request_json)}
                    </h3>
                    <p class="text-sm {jobStatusToneClass(selectedJob.job)}">
                      {jobStatusLabel(selectedJob.job)}
                      <span class="text-base-content/50">· updated {relativeTime(selectedJob.job.updated_at)}</span>
                    </p>
                  </div>

                  <div>
                    <div class="flex gap-1">
                      {#each stageProgress(selectedJob.job) as stage}
                        <span
                          class="h-1.5 flex-1 rounded-full {stageBarClass(stage.status)}"
                          aria-hidden="true"
                        ></span>
                      {/each}
                    </div>
                    <div class="mt-1 flex gap-1 text-xs text-base-content/60">
                      {#each stageProgress(selectedJob.job) as stage}
                        <span
                          class="flex-1"
                          class:font-medium={stage.status === "active" || stage.status === "failed" || stage.status === "blocked"}
                        >
                          {stage.label}
                        </span>
                      {/each}
                    </div>
                  </div>
                  {#if hasGPUResourceNotice(selectedJob.job)}
                    <div class="grid gap-2 rounded-box border border-warning/50 bg-warning/10 p-3">
                      <div class="flex items-center gap-2 text-warning">
                        <TriangleAlert size={16} class="shrink-0" aria-hidden="true" />
                        <p class="text-sm font-semibold">{jobStatusLabel(selectedJob.job)}</p>
                      </div>
                      {#if selectedJob.job.error}
                        <p class="text-xs break-words text-base-content/75">{selectedJob.job.error}</p>
                      {/if}
                      <dl class="flex flex-wrap gap-x-4 gap-y-1 text-xs">
                        {#if isBuildWaitingForGPU(selectedJob.job)}
                          <div class="flex gap-1">
                            <dt class="text-base-content/50">Next retry</dt>
                            <dd>{formatTimestamp(selectedJob.job.build_retry_not_before)}</dd>
                          </div>
                        {/if}
                        <div class="flex gap-1">
                          <dt class="text-base-content/50">GPU deferrals</dt>
                          <dd>{selectedJob.job.build_deferral_count}</dd>
                        </div>
                      </dl>
                    </div>
                  {:else if selectedJob.job.error || selectedJob.job.stop_reason || selectedJob.job.record_stop_detail}
                    <div class="grid gap-1 rounded-box border border-error/40 bg-error/10 p-3">
                      {#if selectedJob.job.error}
                        <p class="text-sm font-medium text-error">{selectedJob.job.error}</p>
                      {/if}
                      {#if selectedJob.job.record_stop_detail}
                        <p class="text-xs break-words text-base-content/70">{selectedJob.job.record_stop_detail}</p>
                      {/if}
                      <dl class="flex flex-wrap gap-x-4 gap-y-1 text-xs">
                        {#if selectedJob.job.record_exit_code !== null}
                          <div class="flex gap-1">
                            <dt class="text-base-content/50">Exit code</dt>
                            <dd class="font-mono">{selectedJob.job.record_exit_code}</dd>
                          </div>
                        {/if}
                        {#if selectedJob.job.stop_reason}
                          <div class="flex gap-1">
                            <dt class="text-base-content/50">Reason</dt>
                            <dd class="font-mono">{selectedJob.job.stop_reason}</dd>
                          </div>
                        {/if}
                        {#if selectedJob.job.stop_requested_at}
                          <div class="flex gap-1">
                            <dt class="text-base-content/50">Stop requested</dt>
                            <dd>{formatTimestamp(selectedJob.job.stop_requested_at)}</dd>
                          </div>
                        {/if}
                      </dl>
                    </div>
                  {/if}
                  <dl class="mt-2 grid gap-x-6 gap-y-3 sm:grid-cols-2 lg:grid-cols-3">
                    <div>
                      <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Provider</dt>
                      <dd class="text-sm">{selectedJob.job.provider}</dd>
                    </div>
                    <div class="min-w-0">
                      <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Meeting URL</dt>
                      <dd class="text-sm break-all">{requestUrlLabel(selectedJob.job.request_json)}</dd>
                    </div>
                    <div class="min-w-0">
                      <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Run id</dt>
                      <dd class="font-mono text-xs break-all">{selectedJob.job.id}</dd>
                    </div>
                    <div>
                      <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Current attempt</dt>
                      <dd class="text-sm">{selectedJob.job.current_attempt_number}</dd>
                    </div>
                    <div>
                      <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Reruns</dt>
                      <dd class="text-sm">{selectedJob.job.rerun_count}</dd>
                    </div>
                    <div>
                      <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Interrupted</dt>
                      <dd class="text-sm">{formatTimestamp(selectedJob.job.interrupted_at)}</dd>
                    </div>
                  </dl>

                  <div class="my-2 h-px w-full bg-base-content/12" aria-hidden="true"></div>
                  <dl class="mt-1 grid gap-x-6 gap-y-3 grid-cols-2 lg:grid-cols-3">
                    <div>
                      <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Created</dt>
                      <dd class="text-sm">{formatTimestamp(selectedJob.job.created_at)}</dd>
                    </div>
                    <div>
                      <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Updated</dt>
                      <dd class="text-sm">{formatTimestamp(selectedJob.job.updated_at)}</dd>
                    </div>
                    <div>
                      <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Completed</dt>
                      <dd class="text-sm">{formatTimestamp(selectedJob.job.completed_at)}</dd>
                    </div>
                  </dl>
                </section>

                <section class="mt-4 grid gap-3">
                  <div class="flex items-center gap-2">
                    <h3 class="font-semibold">Attempt history</h3>
                    <span class="badge badge-outline badge-sm border-base-content/20 text-base-content">
                      {selectedJob.attempts.length}
                    </span>
                  </div>
                  <div class="grid gap-3">
                    {#each selectedJob.attempts as attempt (attempt.attempt_number)}
                      <details class="group overflow-hidden rounded-box border border-base-300">
                        <summary
                          class="flex cursor-pointer list-none items-center gap-2 bg-base-200/50 px-3 py-2 transition group-open:bg-base-200 hover:bg-base-200"
                        >
                          <ChevronRight
                            size={14}
                            class="shrink-0 text-base-content/50 transition-transform group-open:rotate-90"
                            aria-hidden="true"
                          />
                          <span class="min-w-0 flex-1 truncate text-xs text-base-content/60">
                            <span class="text-sm font-medium text-base-content">Attempt {attempt.attempt_number}</span
                            ><span class="ml-2 capitalize">{attempt.trigger_kind}</span><span
                              class="mx-1.5 text-base-content/40">·</span
                            ><span class={jobStatusToneClass(attempt)}>{jobStatusLabel(attempt)}</span>
                          </span>
                        </summary>

                        <div class="grid gap-3 bg-base-200 px-3 pt-3 pb-3">
                          {#if hasGPUResourceNotice(attempt)}
                            <div class="grid gap-2 rounded-box border border-warning/50 bg-warning/10 p-3">
                              <div class="flex items-center gap-2 text-warning">
                                <TriangleAlert size={16} class="shrink-0" aria-hidden="true" />
                                <p class="text-sm font-semibold">{jobStatusLabel(attempt)}</p>
                              </div>
                              {#if attempt.error}
                                <p class="text-xs break-words text-base-content/75">{attempt.error}</p>
                              {/if}
                              <dl class="flex flex-wrap gap-x-4 gap-y-1 text-xs">
                                {#if isBuildWaitingForGPU(attempt)}
                                  <div class="flex gap-1">
                                    <dt class="text-base-content/50">Next retry</dt>
                                    <dd>{formatTimestamp(attempt.build_retry_not_before)}</dd>
                                  </div>
                                {/if}
                                <div class="flex gap-1">
                                  <dt class="text-base-content/50">GPU deferrals</dt>
                                  <dd>{attempt.build_deferral_count}</dd>
                                </div>
                              </dl>
                            </div>
                          {:else if attempt.error || attempt.stop_reason || attempt.record_stop_detail}
                            <div class="grid gap-1 rounded-box border border-error/40 bg-error/10 p-3">
                              {#if attempt.error}
                                <p class="text-sm font-medium text-error">{attempt.error}</p>
                              {/if}
                              {#if attempt.record_stop_detail}
                                <p class="text-xs break-words text-base-content/70">{attempt.record_stop_detail}</p>
                              {/if}
                              <dl class="flex flex-wrap gap-x-4 gap-y-1 text-xs">
                                {#if attempt.record_exit_code !== null}
                                  <div class="flex gap-1">
                                    <dt class="text-base-content/50">Exit code</dt>
                                    <dd class="font-mono">{attempt.record_exit_code}</dd>
                                  </div>
                                {/if}
                                {#if attempt.stop_reason}
                                  <div class="flex gap-1">
                                    <dt class="text-base-content/50">Reason</dt>
                                    <dd class="font-mono">{attempt.stop_reason}</dd>
                                  </div>
                                {/if}
                              </dl>
                            </div>
                          {/if}

                          <div>
                            <div class="flex gap-1">
                              {#each stageProgress(attempt) as stage}
                                <span class="h-1.5 flex-1 rounded-full {stageBarClass(stage.status)}" aria-hidden="true"
                                ></span>
                              {/each}
                            </div>
                            <div class="mt-1 flex gap-1 text-xs text-base-content/60">
                              {#each stageProgress(attempt) as stage}
                                <span
                                  class="flex-1"
                                  class:font-medium={stage.status === "active" || stage.status === "failed" || stage.status === "blocked"}
                                >
                                  {stage.label}
                                </span>
                              {/each}
                            </div>
                          </div>

                          <dl class="grid gap-x-6 gap-y-3 grid-cols-2">
                            <div>
                              <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Queued</dt>
                              <dd class="text-sm">{formatTimestamp(attempt.record_queued_at)}</dd>
                            </div>
                            <div>
                              <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Completed</dt>
                              <dd class="text-sm">{formatTimestamp(attempt.completed_at)}</dd>
                            </div>
                          </dl>

                          {#if attempt.artifact_run_path || attempt.artifact_meeting_path || attempt.artifact_opus_path || attempt.artifact_site_path || attempt.record_log_path || attempt.build_log_path || attempt.seal_log_path || attempt.publish_log_path}
                            <div class="my-1 h-px w-full bg-base-content/12" aria-hidden="true"></div>
                            <dl class="grid gap-3">
                              {#if attempt.artifact_run_path}
                                <div class="min-w-0">
                                  <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Run artifact</dt>
                                  <dd class="font-mono text-xs break-all">{attempt.artifact_run_path}</dd>
                                </div>
                              {/if}
                              {#if attempt.artifact_meeting_path}
                                <div class="min-w-0">
                                  <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Meeting artifact</dt>
                                  <dd class="font-mono text-xs break-all">{attempt.artifact_meeting_path}</dd>
                                </div>
                              {/if}
                              {#if attempt.artifact_opus_path}
                                <div class="min-w-0">
                                  <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Sealed meeting</dt>
                                  <dd class="font-mono text-xs break-all">{attempt.artifact_opus_path}</dd>
                                  {#if attempt.artifact_opus_sha256}
                                    <dd class="font-mono text-xs break-all text-base-content/45">sha256 {attempt.artifact_opus_sha256}</dd>
                                  {/if}
                                </div>
                              {/if}
                              {#if attempt.artifact_site_path}
                                <div class="min-w-0">
                                  <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Site artifact</dt>
                                  <dd class="font-mono text-xs break-all">{attempt.artifact_site_path}</dd>
                                </div>
                              {/if}
                              {#if attempt.record_log_path}
                                <div class="min-w-0">
                                  <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Record log</dt>
                                  <dd class="font-mono text-xs break-all">{attempt.record_log_path}</dd>
                                </div>
                              {/if}
                              {#if attempt.build_log_path}
                                <div class="min-w-0">
                                  <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Build log</dt>
                                  <dd class="font-mono text-xs break-all">{attempt.build_log_path}</dd>
                                </div>
                              {/if}
                              {#if attempt.seal_log_path}
                                <div class="min-w-0">
                                  <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Seal log</dt>
                                  <dd class="font-mono text-xs break-all">{attempt.seal_log_path}</dd>
                                </div>
                              {/if}
                              {#if attempt.publish_log_path}
                                <div class="min-w-0">
                                  <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Publish log</dt>
                                  <dd class="font-mono text-xs break-all">{attempt.publish_log_path}</dd>
                                </div>
                              {/if}
                            </dl>
                          {/if}
                        </div>
                      </details>
                    {/each}
                  </div>
                </section>
              </div>
            </div>
          {/if}
        </section>
        {/if}
      </div>
    {/if}
  </div>
</div>

<style>
  .cassini-tick {
    display: inline-flex;
    animation: cassini-tick var(--cassini-tick, 2000ms) infinite;
  }

  @keyframes cassini-tick {
    0%,
    75% {
      transform: rotate(0deg);
      animation-timing-function: cubic-bezier(0.4, 0, 0.2, 1);
    }
    100% {
      transform: rotate(180deg);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .cassini-tick {
      animation: none;
    }
  }
</style>
