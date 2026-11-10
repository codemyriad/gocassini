<script module lang="ts">
  import { get } from "svelte/store";
  import { dateFormat, formatCount, LL, relativeNow, relativeTimeFormat } from "cassini-viewer/i18n";
  import type { Job, JobAttempt } from "./operator/types";
  import type { OperatorPanel } from "./surfaceRouting";
  import {
    describeProcessing,
    formatSpan,
    meetingLengthMs,
    processesCapture,
    processingHint,
    processingSpeed,
    runProcessing,
    spanMs,
    stepTime,
    summaryTime,
  } from "./operator/processingSpeed";
  import { runStages, stepStatuses, type StageStatus, type StageView } from "./operator/runStages";

  // The operator surface's left nav (D-723). Configuration is not a surface of
  // its own: Console and Settings are two groups of ONE surface, so there is a
  // single admin boundary to probe and a single place to look for a knob.
  //
  // Exported because this list and surfaceRouting's OPERATOR_PANELS have to stay
  // the same set — a panel with a route and no nav row is unreachable, and a row
  // with no route cannot be deep-linked. Operator.test.ts holds them together.
  //
  // Labels are getters, read from the dictionary each time, so the list follows
  // the reader's language rather than freezing in the one loaded first.
  export interface OperatorNavItem {
    id: OperatorPanel;
    readonly label: string;
  }

  export interface OperatorNavGroup {
    readonly label: string;
    items: OperatorNavItem[];
  }

  const nav = () => get(LL).app.operator.console.nav;
  const c = () => get(LL).app.operator.console;

  export const OPERATOR_NAV: OperatorNavGroup[] = [
    {
      get label() { return nav().group_console(); },
      items: [{ id: "recordings", get label() { return nav().recordings(); } }],
    },
    {
      get label() { return nav().group_diagnostics(); },
      items: [{ id: "doctor", get label() { return get(LL).common.place.doctor(); } }],
    },
    {
      get label() { return nav().group_settings(); },
      items: [
        { id: "endpoints", get label() { return get(LL).common.place.ai_providers(); } },
        { id: "pipeline", get label() { return get(LL).common.place.publish_pipeline(); } },
        { id: "templates", get label() { return get(LL).common.place.insight_templates(); } },
        { id: "storage", get label() { return get(LL).common.place.storage(); } },
      ],
    },
    {
      // Last and on its own: asking for help, or helping the project, is not a
      // setting, and it sits where an administrator finds it without it ever
      // getting in the way. Help holds the installation's support bundle
      // (D-910); Browse's own Help links here for an administrator.
      get label() { return nav().group_support(); },
      items: [
        { id: "help", get label() { return nav().help(); } },
        { id: "support", get label() { return nav().support(); } },
      ],
    },
  ];

  // A job names its current attempt's kind, an attempt its own.
  type AttemptKindOf = { current_trigger_kind?: string; trigger_kind?: string };
  type JobStatus = Pick<Job, "stage" | "state" | "build_retry_not_before"> & AttemptKindOf;
  type RerunnableJob = Pick<Job, "stage" | "state" | "artifact_run_path">;

  // The steps a run's status can name, as the stage bar names them.
  const STATUS_STAGES = ["record", "build", "seal", "publish"] as const;
  type StatusStage = (typeof STATUS_STAGES)[number];
  const isStatusStage = (key: string): key is StatusStage => (STATUS_STAGES as readonly string[]).includes(key);

  const FAILURE_STOP_REASONS = new Set([
    "join_failed",
    "signaling_connection_error",
    "record_process_exit_nonzero",
  ]);

  export interface RequestMetadata {
    url?: string;
    baseURL?: string;
    roomToken?: string;
    guestName?: string;
    platform?: string;
    capture_mode?: "audio-only" | "audio-video";
  }

  export function parseRequestJSON(requestJSON: string): RequestMetadata {
    if (!requestJSON || typeof requestJSON !== "string") {
      return {};
    }
    try {
      const payload = JSON.parse(requestJSON);
      if (payload && typeof payload === "object") {
        return {
          capture_mode: payload.capture_mode === "audio-only" || payload.capture_mode === "audio-video" ? payload.capture_mode : undefined,
          url: typeof payload.url === "string" && payload.url.trim() !== "" ? payload.url.trim() : undefined,
          baseURL: typeof payload.baseURL === "string" && payload.baseURL.trim() !== "" ? payload.baseURL.trim() : undefined,
          roomToken: typeof payload.roomToken === "string" && payload.roomToken.trim() !== "" ? payload.roomToken.trim() : undefined,
          guestName: typeof payload.guestName === "string" && payload.guestName.trim() !== "" ? payload.guestName.trim() : undefined,
          platform: typeof payload.platform === "string" && payload.platform.trim() !== "" ? payload.platform.trim() : undefined,
        };
      }
    } catch {
      // Ignore invalid JSON payload
    }
    return {};
  }

  export function captureModeLabel(requestJSON: string): string {
    const mode = parseRequestJSON(requestJSON).capture_mode;
    const t = c().capture_mode;
    if (mode === "audio-only") return t.audio_only();
    if (mode === "audio-video") return t.audio_video();
    return t.unknown();
  }

  export function requestUrlLabel(requestJSON: string): string {
    const meta = parseRequestJSON(requestJSON);
    if (meta.url) {
      return meta.url;
    }
    if (meta.baseURL && meta.roomToken) {
      return `${meta.baseURL.replace(/\/+$/, "")}/call/${meta.roomToken}`;
    }
    if (meta.baseURL) {
      return meta.baseURL;
    }
    if (meta.roomToken) {
      return c().call_label({ token: meta.roomToken });
    }
    return "—";
  }

  /**
   * Names a job. The Talk conversation's own display name wins where the
   * operator has it (D-646); everything below is the fallback for a non-Talk
   * job, a job whose room-name lookup never completed, and every job recorded
   * before the operator promoted the room to a column — none of which say
   * which conversation it was, only which room token it used.
   */
  export function meetingLabel(requestJSON: string, fallbackId?: string, roomName?: string | null): string {
    const room = roomName?.trim();
    if (room) {
      return room;
    }
    const meta = parseRequestJSON(requestJSON);
    if (meta.url) {
      try {
        const token = new URL(meta.url).pathname.split("/").filter(Boolean).pop();
        if (token && token.toLowerCase() !== "call") {
          return c().call_label({ token });
        }
        return meta.url;
      } catch {
        return meta.url;
      }
    }
    if (meta.roomToken) {
      return c().call_label({ token: meta.roomToken });
    }
    if (fallbackId && typeof fallbackId === "string" && fallbackId.trim() !== "") {
      const cleanId = fallbackId.trim();
      const shortId = cleanId.length > 10 ? cleanId.slice(0, 8) : cleanId;
      return c().recording_label({ id: shortId });
    }
    return c().recording_unnamed();
  }

  export function hasJobError(
    item:
      | Pick<Job, "error" | "state" | "record_exit_code" | "stop_reason">
      | Pick<JobAttempt, "error" | "state" | "record_exit_code" | "stop_reason">,
  ): boolean {
    if (item.error != null && item.error.trim() !== "") return true;
    if (item.state === "failed" || item.state === "interrupted") return true;
    if (item.record_exit_code != null && item.record_exit_code !== 0) return true;
    if (item.stop_reason && FAILURE_STOP_REASONS.has(item.stop_reason)) return true;
    return false;
  }

  export function formatStopReason(reason: string | null | undefined): string {
    if (!reason || typeof reason !== "string" || reason.trim() === "") return "—";
    const trimmed = reason.trim();
    const t = c().stop_reason;
    switch (trimmed) {
      case "operator_requested":
        return t.operator_requested();
      case "room_empty":
        return t.room_empty();
      case "duration_limit":
        return t.duration_limit();
      case "signaling_connection_error":
        return t.signaling_connection_error();
      case "join_failed":
        return t.join_failed();
      case "record_process_exit_nonzero":
        return t.record_process_exit_nonzero();
      default:
        // An operator reason this app has no words for: its own name.
        return trimmed.replace(/_/g, " ");
    }
  }

  export function cleanStopDetail(
    detail: string | null | undefined,
    exitCode?: number | null,
    isError?: boolean,
  ): string | null {
    if (!detail || typeof detail !== "string") return null;
    const trimmed = detail.trim();
    if (!trimmed) return null;
    if (trimmed.toLowerCase() === "context canceled" && (!isError || exitCode === 0 || exitCode === null)) {
      return null;
    }
    return trimmed;
  }

  export function formatDuration(
    startStr: string | null | undefined,
    endStr: string | null | undefined,
  ): string | null {
    const ms = spanMs(startStr, endStr);
    return ms === null ? null : formatSpan(ms);
  }

  export function isHttpUrl(value: string | null | undefined): boolean {
    if (!value) return false;
    return value.startsWith("http://") || value.startsWith("https://");
  }

  // A deferred or blocked build is a resource decision, not necessarily a GPU
  // one: since D-702 a build can also wait on host RAM, or block because an
  // explicit CUDA override cannot be satisfied. The job's own error message
  // says which; these labels must not assert a cause they do not know.
  export function isBuildWaitingForResources(job: JobStatus): boolean {
    return job.stage === "build" && job.state === "queued" && job.build_retry_not_before != null;
  }

  export function isBuildBlocked(job: JobStatus): boolean {
    return job.stage === "build" && job.state === "blocked";
  }

  export function hasResourceNotice(job: JobStatus): boolean {
    return isBuildWaitingForResources(job) || isBuildBlocked(job);
  }

  export function attemptKind(job: AttemptKindOf): string {
    return job.trigger_kind ?? job.current_trigger_kind ?? "";
  }

  // A summarize attempt's build stage writes the summary into the published
  // meeting and transcribes nothing, so it is not called a build.
  export function stageLabel(key: string, kind: string): string {
    const stages = get(LL).app.operator.run_stages.stage;
    if (key === "build" && kind === "summarize") return stages.summary();
    return isStatusStage(key) ? stages[key]() : key;
  }

  export function jobStatusLabel(job: JobStatus): string {
    const t = c().status;
    if (isBuildBlocked(job)) return t.build_blocked();
    if (isBuildWaitingForResources(job)) return t.waiting_for_resources();
    if (job.state === "failed") return t.failed();
    if (job.state === "stopped") return t.stopped();
    if (job.state === "interrupted") return t.interrupted();
    if (job.state === "succeeded" && job.stage === "done") return t.published();
    if (job.state === "queued") return t.queued();
    if (job.state === "running") {
      if (job.stage === "build" && attemptKind(job) === "summarize") return t.writing_summary();
      const running: Record<StatusStage, () => string> = {
        record: t.recording,
        build: t.building,
        seal: t.sealing,
        publish: t.publishing,
      };
      return isStatusStage(job.stage) ? running[job.stage]() : t.running();
    }
    // A stage and state this app has no words for: the operator's own names.
    return `${job.stage} / ${job.state}`;
  }

  export function jobStatusToneClass(job: JobStatus): string {
    if (hasResourceNotice(job) || job.state === "stopped") return "text-warning";
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
  import { Activity, ArrowLeft, CassetteTape, ChevronRight, Download, FileText, Inbox, Info, PanelLeft, RefreshCw, Square, TriangleAlert, X } from "@lucide/svelte";
  import { fade } from "svelte/transition";
  import { guardLeave } from "./operator/unsaved";
  import { loadConfig } from "./operator/config";
  import {
    OperatorClient,
    OperatorHttpError,
    type OperatorStateChangeEvent,
  } from "./operator/client";
  import type { Job, JobAttempt, JobDetailResponse, SummaryPlan } from "./operator/types";
  import { findRecordingInFiles, recordingFilesUrl } from "./operator/recordingFiles";
  import { shouldShowDetailLoading } from "./operator/viewState";
  import { outcomeAttempt, reshareOutcome, shareNotice, summaryNotice, transcriptionNotice, type OutcomeNotice } from "./operator/stepOutcomes";
  import {
    attemptLogs,
    attemptReason,
    attemptSpeech,
    attemptSummary,
    speechModelLabel,
    type AttemptLog,
    type FieldValue,
  } from "./operator/attemptModels";
  import {
    summaryActionLabel,
    summaryConfirmTitle,
    summaryCostText,
    summaryEndpointText,
    summaryReplaceText,
    summaryUnavailableText,
  } from "./operator/summaryAction";
  import Settings from "./Settings.svelte";
  import { applyJob, applyPanel, readJob, readPanel } from "./surfaceRouting";

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

  // The run console splits into list + detail when the CONSOLE is wide enough,
  // not when the window is. It used to ask a viewport media query, which cannot
  // see anything taking width away from the console: not the 268px nav rail
  // added beside it (D-723), and not Nextcloud's own sidebar in the embedded
  // build — the shell mounts in a shadow root inside NC's page, so a 1400px
  // window can leave the app ~1050px wide while "(min-width: 981px)" still says
  // desktop. Both panes then render into a console with room for one, and the
  // detail pane's stage bars, action row and provenance line get squeezed to a
  // few hundred pixels.
  //
  // consoleWidth is the console wrapper's own border box; the wrapper carries a
  // matching @container query for the CSS half of the split, so the same number
  // decides both. Nothing is measured before the first paint, so the console
  // starts single-pane and widens on the first observation, exactly as it did
  // when isDesktop was seeded in onMount.
  const CONSOLE_SPLIT_MIN_WIDTH = 981;
  let consoleWidth = 0;
  let isDesktop = false;
  // A width of 0 is "not laid out" — before the first observation, and again
  // every time a settings panel hides the console — not "very narrow". Reading
  // it as narrow would collapse the split for one frame each time the console
  // comes back, which the container query itself never does.
  $: if (consoleWidth > 0) {
    isDesktop = consoleWidth >= CONSOLE_SPLIT_MIN_WIDTH;
  }

  // Which nav row is showing. The run console stays mounted underneath a
  // settings panel — it holds list scroll, the open run and its expanded
  // attempts, the same reason the shell keeps browse mounted behind the
  // operator — so switching rows hides it rather than tearing it down.
  let panel = readPanel(window.location.hash);
  let shellElement: HTMLElement | null = null;

  onMount(async () => {
    window.addEventListener("popstate", handleJobPopState);
    window.addEventListener("popstate", handlePanelPopState);
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
    window.removeEventListener("popstate", handlePanelPopState);
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
    if (jobId !== selectedJobId) dropReshareAnswer(selectedJobId);
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
    dropReshareAnswer(selectedJobId);
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
      dropReshareAnswer(selectedJobId);
      selectedJobId = "";
      selectedJob = null;
      detailError = "";
      return;
    }
    void selectJob(urlJobId, { allowJobsRefresh: false });
  }

  function panelHref(next: OperatorPanel): string {
    const url = new URL(window.location.href);
    url.hash = applyPanel(window.location.hash, next).replace(/^#/, "");
    return url.toString();
  }

  let navOpen = false;
  let navButton: HTMLButtonElement;
  let navViewTop = 0;
  let navViewHeight = 0;

  function measureNavView() {
    const scroller = shellElement?.closest(".cassini-shell-scroll") as HTMLElement | null;
    if (!scroller || !shellElement) return;
    navViewTop = Math.max(0, scroller.scrollTop - shellElement.offsetTop);
    navViewHeight = scroller.clientHeight;
  }

  function openNav() {
    measureNavView();
    navOpen = true;
  }

  let navScrollCleanup: (() => void) | undefined;
  $: {
    navScrollCleanup?.();
    navScrollCleanup = undefined;
    if (navOpen) {
      const scroller = shellElement?.closest(".cassini-shell-scroll") as HTMLElement | null;
      const previousOverflow = scroller?.style.overflowY ?? "";
      if (scroller) scroller.style.overflowY = "hidden";
      window.addEventListener("resize", measureNavView);
      navScrollCleanup = () => {
        if (scroller) scroller.style.overflowY = previousOverflow;
        window.removeEventListener("resize", measureNavView);
      };
    }
  }
  onDestroy(() => navScrollCleanup?.());
  $: currentPanelLabel =
    OPERATOR_NAV.flatMap((group) => group.items).find((item) => item.id === panel)?.label ?? "";

  function closeNav() {
    if (!navOpen) return;
    navOpen = false;
    navButton?.focus();
  }

  function handleNavKeydown(event: KeyboardEvent) {
    if (navOpen && event.key === "Escape") {
      event.preventDefault();
      closeNav();
    }
  }

  function selectPanel(next: OperatorPanel) {
    navOpen = false;
    if (next === panel) {
      return;
    }
    guardLeave(() => goToPanel(next));
  }

  function goToPanel(next: OperatorPanel) {
    panel = next;
    // Fragment-only pushState, exactly as the job deep-link does it, so the
    // back button walks the nav and a panel can be linked to (D-722).
    window.history.pushState({}, "", panelHref(next));
    resetPanelScroll();
  }

  function handlePanelPopState() {
    const next = readPanel(window.location.hash);
    if (next === panel) {
      return;
    }
    panel = next;
    resetPanelScroll();
  }

  // "Each panel is its own page, so it starts at its own top" — the design
  // prototype's rule, and the reason a switch cannot just swap the content: a
  // long run list leaves the next panel opening halfway down itself.
  //
  // The scroll pane belongs to the SHELL, not to the operator (App.svelte: the
  // operator was authored for page-level scroll, which the fixed-height shell
  // removes), so the reset has to reach the ancestor that actually scrolls —
  // scrolling this element would be a no-op. The window fallback is the
  // standalone build, where there is no shell and the page scrolls itself.
  function resetPanelScroll() {
    const scroller = shellElement?.closest(".cassini-shell-scroll");
    if (scroller instanceof HTMLElement) {
      scroller.scrollTop = 0;
      return;
    }
    window.scrollTo({ top: 0 });
  }

  async function handleStartJob() {
    const trimmedUrl = meetingUrl.trim();
    if (!operatorClient || trimmedUrl === "") {
      actionError = c().meeting_url_required();
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

  // Re-sharing a published meeting with its room's audience (D-885), once
  // whatever refused some of it is fixed.
  // Kept per run: the request outlives a click on another run, and its
  // answer belongs to the run it was made for. Said beside the button, since
  // the page's own action error sits in the new-recording form.
  let reshareJobId = "";
  let reshareError: { jobId: string; message: string } | null = null;
  // Who is still refused after a re-share, by id, for the sentence beside
  // the button; empty is "Shared with everyone." — said on its own line,
  // because a re-share that reached everyone takes the notice, and the
  // button under it, away with the next read of the run.
  let reshareResult: { jobId: string; refused: string[]; deferred: number } | null = null;
  $: reshareLine = reshareResult && reshareResult.jobId === selectedJob?.job.id ? reshareOutcome(reshareResult.refused, reshareResult.deferred) : "";
  // The answer is dropped when the person leaves the run it was made for:
  // they have read it there, and the run's next read says what is true now.
  // Otherwise "Shared with everyone." would come back with every reselection
  // for the rest of the session, beside a new warning if the run turned
  // partial again. An answer that arrives while another run is open is kept
  // for its run until that one is opened and left.
  function dropReshareAnswer(jobId: string) {
    if (reshareResult?.jobId === jobId) reshareResult = null;
    if (reshareError?.jobId === jobId) reshareError = null;
  }
  $: shareNoticeShown = outcomeNotices.some((notice) => notice.action?.kind === "reshare");
  async function handleReshare() {
    if (!operatorClient || !selectedJob?.job || reshareJobId) return;
    const jobId = selectedJob.job.id;
    reshareJobId = jobId;
    reshareError = null;
    reshareResult = null;
    try {
      const answer = await operatorClient.reshareJob(jobId);
      reshareResult = {
        jobId,
        refused: answer.sharing?.status === "partial" ? (answer.sharing.skipped ?? []).map((entry) => entry.id) : [],
        deferred: answer.sharing?.deferred?.length ?? 0,
      };
      await refreshJobs();
      if (selectedJobId === jobId) await selectJob(jobId, { allowJobsRefresh: false });
    } catch (error) { reshareError = { jobId, message: asMessage(error) }; }
    finally { reshareJobId = ""; }
  }

  let submittingCleanup = false;
  let cleanupQueuedJob = "";
  async function handleRetryCleanup() {
    if (!operatorClient || !selectedJob?.job || submittingCleanup) return;
    submittingCleanup = true;
    actionError = "";
    try {
      await operatorClient.retryMediaCleanup(selectedJob.job.id);
      cleanupQueuedJob = selectedJob.job.id;
      await refreshJobs();
      await selectJob(selectedJob.job.id, { allowJobsRefresh: false });
    } catch (error) { actionError = asMessage(error); }
    finally { submittingCleanup = false; }
  }

  function cleanupLabel(status: string): string {
    const t = c().cleanup;
    return ({ waiting: t.waiting, pending: t.pending, error: t.error, completed: t.completed } as Record<string, () => string>)[status]?.() ?? status;
  }

  // Writing a run's summary (D-814): what it would cost is loaded for a
  // finished run, and nothing is queued before the administrator has read
  // that and confirmed.
  let summaryPlan: SummaryPlan | null = null;
  let summaryPlanKey = "";
  let confirmingSummary = false;
  let submittingSummary = false;
  let summaryError = "";

  async function loadSummaryPlan(key: string, jobId: string) {
    if (!operatorClient) return;
    try {
      const plan = await operatorClient.getJobSummary(jobId);
      if (summaryPlanKey === key) summaryPlan = plan;
    } catch (error) {
      // No plan means no action offered; say why in the console rather than
      // hide it.
      console.error("summary plan unavailable", jobId, error);
      if (summaryPlanKey === key) summaryPlan = null;
    }
  }

  async function handleWriteSummary() {
    if (!operatorClient || !selectedJob?.job || !summaryPlan?.available) return;
    submittingSummary = true;
    summaryError = "";
    try {
      await operatorClient.writeJobSummary(selectedJob.job.id, summaryPlan.has_summary);
      confirmingSummary = false;
      await refreshJobs();
      await selectJob(selectedJob.job.id, { allowJobsRefresh: false });
    } catch (error) {
      const message = asMessage(error);
      summaryError =
        message === "has_summary" || message === "unavailable"
          ? c().summary_changed()
          : message;
    } finally {
      submittingSummary = false;
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
    // The detail on screen is still the previous run's while this one loads:
    // merging into it would show that run's sharing and Files link under this
    // one's name. The load brings this run's own detail.
    if (selectedJob.job.id !== event.job_id) {
      return;
    }
    // Fields only the detail carries are kept until the event says otherwise.
    selectedJob = {
      ...selectedJob,
      job: event.job,
      availability: event.availability ?? selectedJob.availability,
      attempts: upsertAttempt(selectedJob.attempts, event.attempt),
      sharing: event.sharing ?? selectedJob.sharing,
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
      (selectedJob?.job != null && (isJobActive(selectedJob.job) || (selectedJob.availability?.media_cleanup != null && selectedJob.availability.media_cleanup.status !== "completed"))) ||
      jobs.some(job => isJobActive(job) || (job.media_cleanup != null && job.media_cleanup.status !== "completed"));
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
    return dateFormat({
      dateStyle: "medium",
      timeStyle: "medium",
    }).format(date);
  }

  // stageTimes reads `${key}_queued_at` / `_started_at` / `_finished_at`, so a
  // stage appears here only if the operator names its columns to match.
  const STAGES = STATUS_STAGES.map((key) => ({ key }));

  // One attempt's bar: its own four steps, each with how long it took.
  // An attempt's own bar has no Summary step, so its Build keeps the summary
  // and says so (D-899); the run's bar times them apart.
  function stageProgress(row: Job | JobAttempt): StageView[] {
    const statuses = stepStatuses(row);
    const summaryMs = summaryTime(row);
    return STAGES.map((stage) => ({
      key: stage.key,
      label: stageLabel(stage.key, attemptKind(row)),
      status: statuses[stage.key],
      ms: stepTime(row, stage.key),
      hint: stage.key === "build" && summaryMs !== null && attemptKind(row) !== "summarize"
        ? c().build_includes_summary({ span: formatSpan(summaryMs) })
        : undefined,
    }));
  }

  // Said under a step that has not finished, so a coloured bar is not the
  // only sign of what is happening.
  function unfinishedNote(status: StageStatus): string | undefined {
    const t = c().unfinished;
    if (status === "active") return t.active();
    if (status === "queued") return t.queued();
    if (status === "blocked") return t.blocked();
    return undefined;
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
      case "skipped":
        return "bg-base-content/35";
      default:
        return "bg-base-content/12";
    }
  }

  function relativeTime(value: string | null | undefined): string {
    if (!value) return "—";
    const then = Date.parse(value);
    if (Number.isNaN(then)) return "—";
    const seconds = Math.round((Date.now() - then) / 1000);
    if (seconds < 60) return relativeNow();
    const ago = relativeTimeFormat({ style: "narrow" });
    const minutes = Math.round(seconds / 60);
    if (minutes < 60) return ago.format(-minutes, "minute");
    const hours = Math.round(minutes / 60);
    if (hours < 24) return ago.format(-hours, "hour");
    return ago.format(-Math.round(hours / 24), "day");
  }

  // The one log open in the run detail, read from the operator (D-816). Shown
  // inline under its attempt: the app runs in a shadow root on Nextcloud's
  // page, where a top-layer <dialog> does not reliably keep its styling.
  interface OpenLog {
    jobId: string;
    attempt: number;
    stage: AttemptLog["stage"];
    label: string;
    text: string;
    truncated: boolean;
    loading: boolean;
    error: string;
  }
  let openLog: OpenLog | null = null;

  async function toggleLog(jobId: string, attempt: number, log: AttemptLog) {
    if (openLog && openLog.jobId === jobId && openLog.attempt === attempt && openLog.stage === log.stage) {
      openLog = null;
      return;
    }
    if (!operatorClient) return;
    const opening: OpenLog = { jobId, attempt, stage: log.stage, label: log.label, text: "", truncated: false, loading: true, error: "" };
    openLog = opening;
    try {
      const { text, truncated } = await operatorClient.getAttemptLog(jobId, attempt, log.stage);
      if (openLog === opening) openLog = { ...opening, text, truncated, loading: false };
    } catch (error) {
      console.error("attempt log read failed", jobId, attempt, log.stage, error);
      if (openLog === opening) openLog = { ...opening, loading: false, error: asMessage(error) };
    }
  }

  $: if (openLog && openLog.jobId !== selectedJobId) openLog = null;

  // Where an attempt's artifacts sit inside the operator's container, for
  // whoever debugs it there. Its logs are read from the Logs row instead.
  function attemptPaths(attempt: JobAttempt): { label: string; path: string | null; missing: boolean }[] {
    const t = c().path;
    const entries: [string, string | null, string][] = [
      [t.run(), attempt.artifact_run_path, "run"],
      [t.meeting(), attempt.artifact_meeting_path, "meeting"],
      [t.opus(), attempt.artifact_opus_path, "opus"],
      [t.site(), attempt.artifact_site_path, "site"],
    ];
    return entries.map(([label, path, key]) => ({ label, path, missing: !!path && attempt.files_present?.[key] === false }));
  }

  function attemptStageLabel(attempt: JobAttempt): string {
    return `${attempt.attempt_number} · ${attempt.stage} / ${attempt.state}`;
  }

  // What started an attempt; a kind this app has no word for keeps its own name.
  function triggerKindLabel(kind: string): string {
    const t = c().trigger_kind;
    const known: Record<string, () => string> = { initial: t.initial, rerun: t.rerun, summarize: t.summarize, refine: t.refine };
    return known[kind]?.() ?? kind;
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
    const t = c().connection;
    switch (status) {
      case "connected":
      case "unavailable":
        return t.live();
      case "connecting":
        return t.connecting();
      case "reconnecting":
        return t.reconnecting();
      case "disconnected":
        return t.offline();
      default:
        return t.unknown();
    }
  }

  function streamStatusLabel(status: typeof streamStatus): string {
    const t = c().stream;
    switch (status) {
      case "connected":
        return t.live();
      case "connecting":
        return t.connecting();
      case "reconnecting":
        return t.reconnecting();
      case "unavailable":
        // The normal mode in Nextcloud (AppAPI can't stream SSE), so state the
        // refresh cadence plainly rather than framing routine operation as a
        // failure.
        return t.auto_refreshing({ seconds: formatCount(POLL_INTERVAL_MS / 1000) });
      case "disconnected":
        return t.disconnected();
      default:
        return t.idle();
    }
  }

  $: canStartJob = !submittingStart && meetingUrl.trim() !== "";
  $: stopApplies = selectedJob?.job.stage === "record" && selectedJob?.job.state === "running";

  // The run's recording in Nextcloud Files (D-825), linked only when Nextcloud
  // finds it for the signed-in administrator. Looked up once per file id, not
  // on every poll of the detail.
  let recordingFileId = 0;
  let recordingHref = "";
  $: void lookUpRecording(selectedJob?.recording_file_id ?? 0);

  async function lookUpRecording(fileId: number) {
    if (fileId === recordingFileId) {
      return;
    }
    recordingFileId = fileId;
    recordingHref = "";
    if (fileId <= 0) {
      return;
    }
    const path = await findRecordingInFiles(fileId);
    if (fileId === recordingFileId && path !== "") {
      recordingHref = recordingFilesUrl(window.location, path, fileId);
    }
  }
  $: jobFinished = selectedJob?.job.stage === "done";
  $: rerunVisible = !!selectedJob?.job && (jobFinished || isBuildBlocked(selectedJob.job));
  $: rerunApplies = !!selectedJob?.job && isRerunnableJob(selectedJob.job);
  $: recordingDeleted = selectedJob?.availability?.source === "deleted" || selectedJob?.availability?.source === "expired" || selectedJob?.job.source_expired === true;
  $: rerunBlockedReason =
    (selectedJob?.availability?.source === "expired" || selectedJob?.job.source_expired ? $LL.app.operator.console.rerun_blocked_expired() : selectedJob?.availability?.rerun_blocked_reason) || (rerunVisible && !selectedJob?.job.artifact_run_path
      ? $LL.app.operator.console.rerun_no_recording()
      : "");
  $: canStopSelectedJob = !submittingStop && stopApplies;
  $: canRerunSelectedJob = !submittingRerun && rerunApplies && !rerunBlockedReason;
  // A run that published without a transcript or a summary says so (D-854,
  // D-814), from the attempt it published.
  $: outcomeSource = selectedJob ? outcomeAttempt(selectedJob) : null;
  $: runSpeed = selectedJob ? runProcessing(selectedJob) : null;
  $: meetingMs = selectedJob ? meetingLengthMs(selectedJob.job) : null;
  // Who it could not be shared with (D-885): the meeting's, not an attempt's,
  // because a rerun keeps the file and its shares.
  $: outcomeNotices = [
    shareNotice(
      selectedJob?.sharing,
      rerunApplies && !rerunBlockedReason,
      !!selectedJob && (!isJobActive(selectedJob.job) || selectedJob.job.state === "interrupted"),
    ),
    ...(outcomeSource
      ? [transcriptionNotice(outcomeSource, rerunApplies && !rerunBlockedReason), summaryNotice(outcomeSource)]
      : []),
  ].filter((notice): notice is OutcomeNotice => notice !== null);
  $: {
    // One plan per finished run and state: a new attempt or an update reloads
    // it, and leaves any open confirmation, which priced the old one.
    const key =
      selectedJob && jobFinished ? `${selectedJob.job.id}:${selectedJob.job.current_attempt_number}:${selectedJob.job.updated_at}` : "";
    if (key !== summaryPlanKey) {
      summaryPlanKey = key;
      summaryPlan = null;
      confirmingSummary = false;
      summaryError = "";
      if (key && selectedJob) void loadSummaryPlan(key, selectedJob.job.id);
    }
  }
  const outcomeToneClasses: Record<OutcomeNotice["tone"], string> = {
    error: "border-error/40 bg-error/10",
    warning: "border-warning/50 bg-warning/10",
    info: "border-base-content/15 bg-base-200/60",
  };
  const outcomeIconClasses: Record<OutcomeNotice["tone"], string> = {
    error: "text-error",
    warning: "text-warning",
    info: "text-base-content/60",
  };
</script>

<!-- A stage bar: each step's progress and how long it took once finished
     (D-817), or a note when it has no time of its own. No multiplier: only
     the run's overall figure gets one. -->
{#snippet stageBar(stages: StageView[])}
  <div>
    <div class="flex gap-1">
      {#each stages as stage (stage.key)}
        <span class="h-1.5 flex-1 rounded-full {stageBarClass(stage.status)}" aria-hidden="true"></span>
      {/each}
    </div>
    <div class="mt-1 flex gap-1 text-xs text-base-content/60">
      {#each stages as stage (stage.key)}
        {@const title = [stage.hint, stage.attempt ? $LL.app.operator.console.from_attempt({ attempt: stage.attempt }) : null].filter(Boolean).join(". ")}
        <span class="min-w-0 flex-1" title={title || undefined}>
          <span
            class="block"
            class:font-medium={stage.status === "active" || stage.status === "failed" || stage.status === "blocked"}
          >
            {stage.label}
          </span>
          {#if stage.ms !== null}
            <span class="block whitespace-nowrap text-base-content/45">{formatSpan(stage.ms)}</span>
          {:else if stage.note ?? unfinishedNote(stage.status)}
            <span class="block whitespace-nowrap text-base-content/45">{stage.note ?? unfinishedNote(stage.status)}</span>
          {/if}
        </span>
      {/each}
    </div>
  </div>
{/snippet}

<svelte:head>
  <title>{$LL.app.operator.console.page_title()}</title>
  <meta
    name="description"
    content={$LL.app.operator.console.page_description()}
  />
</svelte:head>

<!-- Two columns: the nav on the left, one panel on the right. The panels are
     siblings in the grid rather than nested in a content column, because exactly
     one of them is ever laid out — the run console is display:none when it is
     not the active panel, and the settings host does not exist unless it is — so
     auto-placement puts whichever survives in the second column. -->
<svelte:window on:keydown={handleNavKeydown} />

<!-- The run detail shows a fixed set of fields in fixed places (D-816); one
     with nothing to show says so rather than disappearing. -->
{#snippet notAvailable()}
  <span class="text-base-content/45">{$LL.app.operator.console.not_available()}</span>
{/snippet}

<!-- A known value, or a muted account of why there is none (reused,
     skipped, kept); "Not available" only when nothing is known. -->
{#snippet fieldValue(value: FieldValue | null)}
  {#if value?.muted}
    <span class="text-base-content/60">{value.text}</span>
  {:else if value}
    {value.text}
  {:else}
    {@render notAvailable()}
  {/if}
{/snippet}

<div
  class="op-shell grid min-h-full grid-cols-1 bg-base-200 text-base-content min-[721px]:grid-cols-[268px_minmax(0,1fr)]"
  bind:this={shellElement}
>
  <!-- Below 721px the two-column layout has nowhere to put a 268px rail, so the
       rail becomes a drawer behind a section button, the way the viewing
       layer's rooms rail does: the same headings and rows as on desktop, where
       a native select would restyle them per platform. -->
  <!-- The rail stays put while the content scrolls, which is what the prototype
       asks for. The scroll pane belongs to the SHELL (App.svelte), not to this
       surface, so the rail cannot be a non-scrolling sibling of a scrolling
       column without taking overflow off the shared pane — sticky gets the same
       result and leaves the shell's single-pane contract alone. self-start is
       load-bearing: a stretched grid item is as tall as its area and has
       nowhere to stick. It carries the surface background because content now
       passes underneath it, and none of it applies below 721px, where the nav
       is a drawer. -->
  <div class="op-nav-bar">
    <button
      bind:this={navButton}
      type="button"
      class="op-nav-button"
      aria-label={$LL.app.operator.console.nav.open_label()}
      aria-expanded={navOpen}
      on:click={openNav}
    >
      <PanelLeft size={15} aria-hidden="true" />
      <span>{currentPanelLabel}</span>
    </button>
  </div>

  {#if navOpen}
    <button
      type="button"
      class="op-nav-scrim"
      style:--op-nav-top="{navViewTop}px"
      style:--op-nav-height="{navViewHeight}px"
      aria-label={$LL.app.operator.console.nav.close_label()}
      transition:fade={{ duration: 200 }}
      on:click={closeNav}
    ></button>
  {/if}

  <nav
    class="op-nav flex flex-col pt-[16px] pb-3.5 min-[721px]:sticky min-[721px]:top-0 min-[721px]:self-start"
    aria-label={$LL.app.operator.console.nav.label()}
    data-open={navOpen}
    style:--op-nav-top="{navViewTop}px"
    style:--op-nav-height="{navViewHeight}px"
  >
    {#each OPERATOR_NAV as group, groupIndex}
      <div
        class="flex h-[12px] items-center px-4 text-[10px] leading-none font-semibold tracking-[0.09em] uppercase text-base-content/45 mb-[0.75rem] {groupIndex > 0 ? 'mt-4' : ''}"
      >
        {group.label}
      </div>
      {#each group.items as item}
        <!-- The active row is marked the way the rooms rail marks the selected
             room: a 35% primary wash and a 3px primary edge, base-content text
             at the same weight. In Nextcloud --color-primary is whatever accent
             the instance chose, and primary text on a primary wash is the pair
             that stops being legible. -->
        <button
          type="button"
          class="op-nav-item"
          aria-current={panel === item.id ? "page" : undefined}
          on:click={() => selectPanel(item.id)}
        >
          {item.label}
        </button>
      {/each}
    {/each}
  </nav>

  <!-- The content scrolls, the rail does not: on one shared scroller the rail
       is sticky, and a bounce at either end of the content drags it along with
       everything else. -->
  <div class="op-content">
  <div
    class="op-settings op-recordings mx-auto flex min-h-full w-full flex-col gap-4"
    class:cassini-op-panel-inactive={panel !== "recordings"}
  >
    <header class="op-panel-head">
      <div>
        <div class="op-panel-title">
          <h1>{$LL.app.operator.console.title()}</h1>
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
            {$LL.app.operator.console.checking_every({ seconds: formatCount(POLL_INTERVAL_MS / 1000) })}
          </span>
        {:else if streamStatus === "disconnected"}
          <span class="flex items-center gap-1 text-xs text-base-content/60">
            <span class="cassini-tick" style="--cassini-tick: {POLL_INTERVAL_MS}ms">
              <RefreshCw size={12} aria-hidden="true" />
            </span>
            {$LL.app.operator.console.retrying_every({ seconds: formatCount(POLL_INTERVAL_MS / 1000) })}
          </span>
        {/if}
        </div>
      </div>
      <div class="op-panel-actions">
        <button class="op-btn" type="button" on:click={openComposer}>
          {$LL.app.operator.console.record_meeting()}
        </button>
      </div>
    </header>

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
              <span class="font-semibold">{$LL.app.operator.console.meeting_link()}</span>
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
                {$LL.app.operator.console.starting()}
              {:else}
                {$LL.app.operator.console.start_recording()}
              {/if}
            </button>
          </div>
          {#if actionError}
            <div class="mt-3 alert alert-error text-sm">{actionError}</div>
          {/if}
        </section>
      {/if}

      <!-- A wrapper with no padding or border of its own, so its measured width
           IS the console's: the @container query below and the isDesktop gate
           above read the same box, and the CSS split and the JS "which panes
           exist" decision cannot disagree. -->
      <div class="@container mt-6" bind:clientWidth={consoleWidth}>
      <div class="op-split grid @min-[981px]:grid-cols-[minmax(0,26rem)_minmax(0,1fr)]">
        {#if isDesktop || !selectedJobId}
        <section class="flex min-w-0 flex-col overflow-hidden">
          <header class="op-pane-head flex min-h-14 items-center justify-between gap-3 px-4 py-3 @min-[981px]:h-14">
            <h2 class="font-semibold">{$LL.app.operator.console.list_heading()}</h2>
            <button class="icon-btn" on:click={refreshJobs} type="button" aria-label={$LL.app.operator.console.refresh_jobs()}>
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
            <div class="flex flex-1 items-center justify-center p-6 text-sm text-base-content/60">{$LL.app.operator.console.loading_jobs()}</div>
          {:else if jobs.length === 0}
            <div class="flex min-h-0 flex-1 p-3">
              <div
                class="flex flex-1 flex-col items-center justify-center gap-2 rounded-box border border-dashed border-base-content/20 p-6 text-center"
              >
                <Inbox size={20} class="text-base-content/40" aria-hidden="true" />
                <p class="text-sm font-medium">{$LL.app.operator.console.empty_title()}</p>
                <p class="text-xs text-base-content/60">{$LL.app.operator.console.empty_body()}</p>
              </div>
            </div>
          {:else}
            <div class="min-h-0 min-w-0 flex-1 @min-[981px]:relative">
              <div
                class="overflow-x-hidden p-3 @min-[981px]:absolute @min-[981px]:inset-0 @min-[981px]:overflow-y-auto"
              >
                <div class="recordings-list grid min-w-0 gap-2">
                {#each jobs as job (job.id)}
                  <button
                    class="card w-full min-w-0 overflow-hidden border text-left {job.id === selectedJobId
                      ? 'run-card run-card-selected cursor-default border-base-300'
                      : 'run-card cursor-pointer border-base-300'}"
                    type="button"
                    aria-current={job.id === selectedJobId ? "page" : undefined}
                    on:click={() => openJob(job.id)}
                  >
                    <div class="card-body min-w-0 gap-1 p-3">
                      <div class="flex min-w-0 items-start justify-between gap-2">
                        <p class="min-w-0 flex-1 truncate text-sm font-medium" title={requestUrlLabel(job.request_json)}>
                          {meetingLabel(job.request_json, job.id, job.room_display_name || job.room_name)}
                        </p>
                        {#if job.current_attempt_number > 1}
                          <span class="badge badge-outline badge-sm shrink-0 border-base-content/20 text-base-content"
                            >{$LL.app.operator.console.attempt_badge({ number: formatCount(job.current_attempt_number) })}</span
                          >
                        {/if}
                      </div>
                      <div class="min-w-0 text-xs">
                        <p class="truncate">
                          <span class={jobStatusToneClass(job)}>{jobStatusLabel(job)}</span>
                          <span class="text-base-content/50" title={formatTimestamp(job.record_started_at ?? job.created_at)}> · {relativeTime(job.record_started_at ?? job.created_at)}</span>
                          {#if formatDuration(job.record_started_at, job.record_finished_at)}
                            <span class="text-base-content/50"> · {formatDuration(job.record_started_at, job.record_finished_at)}</span>
                          {/if}
                        </p>
                        {#if isBuildWaitingForResources(job)}
                          <p class="truncate text-warning" title={job.error || formatTimestamp(job.build_retry_not_before)}>
                            {job.build_deferral_count === 1
                              ? $LL.app.operator.console.next_retry_deferrals_one({ time: formatTimestamp(job.build_retry_not_before), count: formatCount(job.build_deferral_count) })
                              : $LL.app.operator.console.next_retry_deferrals_other({ time: formatTimestamp(job.build_retry_not_before), count: formatCount(job.build_deferral_count) })}
                          </p>
                        {/if}
                        {#if job.media_cleanup}
                          <span class="badge badge-outline badge-sm mt-1" class:badge-warning={job.media_cleanup.status === "error"} title={job.media_cleanup.last_error || $LL.app.operator.console.cleanup_hint()}>{cleanupLabel(job.media_cleanup.status)}</span>
                        {/if}
                        {#if job.source_expired}
                          <span class="badge badge-outline badge-warning badge-sm mt-1" title={$LL.app.operator.console.source_expired_title()}>{$LL.app.operator.console.source_expired_badge()}</span>
                        {/if}
                        {#if job.meeting_deleted}
                          <span class="badge badge-outline badge-warning badge-sm mt-1" title={$LL.app.operator.console.meeting_deleted_title()}>{$LL.app.operator.console.meeting_deleted_badge()}</span>
                        {/if}
                        {#if job.unshared_count}
                          <span class="badge badge-outline badge-warning badge-sm mt-1" title={job.unshared_count === 1 ? $LL.app.operator.console.not_shared_title_one() : $LL.app.operator.console.not_shared_title_other({ count: formatCount(job.unshared_count) })}>{$LL.app.operator.console.not_shared_badge()}</span>
                        {/if}
                        {#if job.error}
                          <p class="truncate {hasResourceNotice(job) ? 'text-warning' : 'text-base-content/55'}" title={job.error}>
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
          class="op-detail-pane flex min-w-0 flex-col @min-[981px]:min-h-[calc(100vh-13rem)] @min-[981px]:border-l"
        >
          <header class="op-pane-head flex min-h-14 items-center justify-between gap-3 px-4 py-3 @min-[981px]:h-14">
            <div class="flex min-w-0 flex-1 items-center gap-2">
              {#if !isDesktop}
                <button
                  class="btn btn-ghost btn-sm btn-square shrink-0"
                  type="button"
                  on:click={handleBackToList}
                  aria-label={$LL.app.operator.console.back_to_recordings()}
                >
                  <ArrowLeft size={16} aria-hidden="true" />
                </button>
              {/if}
              <div class="min-w-0 @min-[981px]:flex @min-[981px]:items-center @min-[981px]:gap-2">
                <h2 class="truncate font-semibold @min-[981px]:shrink-0">{$LL.app.operator.console.detail_heading()}</h2>
                <p class="truncate text-xs text-base-content/60 @min-[981px]:min-w-0">
                  {$LL.app.operator.console.detail_subtitle()}
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
                    {$LL.app.operator.console.stopping()}
                  {:else}
                    <Square size={14} aria-hidden="true" />
                    {$LL.app.operator.console.stop()}
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
                    {$LL.app.operator.console.rerunning()}
                  {:else}
                    <RefreshCw size={14} aria-hidden="true" />
                    {$LL.app.operator.console.rerun()}
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
                <p class="text-sm font-medium">{$LL.app.operator.console.no_selection_title()}</p>
                <p class="text-xs text-base-content/60">{$LL.app.operator.console.no_selection_body()}</p>
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
                <section class="op-tint run-detail-card grid gap-3">
                  <div class="min-w-0">
                    <h3 class="truncate text-lg font-semibold" title={requestUrlLabel(selectedJob.job.request_json)}>
                      {meetingLabel(selectedJob.job.request_json, selectedJob.job.id, selectedJob.job.room_display_name || selectedJob.job.room_name)}
                    </h3>
                    <p class="text-sm {jobStatusToneClass(selectedJob.job)}">
                      {jobStatusLabel(selectedJob.job)}
                      <span class="text-base-content/50">· {$LL.app.operator.console.updated_ago({ age: relativeTime(selectedJob.job.updated_at) })}</span>
                    </p>
                  </div>

                  {@render stageBar(runStages(selectedJob, recordingDeleted))}
                  {#if selectedJob.availability?.media_cleanup}
                    {@const cleanup = selectedJob.availability.media_cleanup}
                    <div class="op-tint p-3 grid gap-2" role="status">
                      <p class="font-semibold">{cleanupLabel(cleanup.status)}</p>
                      <p class="text-sm">{$LL.app.operator.console.cleanup.body()}</p>
                      {#if cleanup.completed_at}<p class="text-sm">{$LL.app.operator.console.cleanup.completed_at({ time: cleanup.completed_at })}</p>{/if}
                      {#if cleanup.last_error}<p class="text-sm text-warning">{cleanup.last_error}</p>{/if}
                      {#if cleanup.status === "error"}
                        <button class="btn btn-sm" type="button" disabled={submittingCleanup} on:click={handleRetryCleanup}>{$LL.app.operator.console.cleanup.retry()}</button>
                        {#if cleanupQueuedJob === selectedJob.job.id}<p class="text-sm">{$LL.app.operator.console.cleanup.retry_requested()}</p>{/if}
                      {/if}
                    </div>
                  {/if}
                  {#each outcomeNotices as notice (notice.title)}
                    <div class="flex items-start gap-2 rounded-box border p-3 text-sm {outcomeToneClasses[notice.tone]}" role="status">
                      {#if notice.tone === "info"}
                        <Info size={16} class="mt-0.5 shrink-0 {outcomeIconClasses[notice.tone]}" aria-hidden="true" />
                      {:else}
                        <TriangleAlert size={16} class="mt-0.5 shrink-0 {outcomeIconClasses[notice.tone]}" aria-hidden="true" />
                      {/if}
                      <div class="grid min-w-0 gap-1">
                        <p class="font-medium">{notice.title}</p>
                        {#if notice.detail}<p class="font-mono text-xs break-words text-base-content/75">{notice.detail}</p>{/if}
                        {#if notice.items}
                          <!-- Closed by default; the summary carries the count. Each
                               row: the id, its kind as a small label, the reason muted. -->
                          <details class="text-xs">
                            <summary class="cursor-pointer text-base-content/75">{notice.items.summary}</summary>
                            <ul class="mt-1 grid gap-0.5 border-l-2 border-base-300 pl-2">
                              {#each notice.items.entries as entry}
                                <li class="break-words"><strong class="font-semibold text-base-content">{entry.id}</strong> <span class="text-[0.7rem] uppercase tracking-wide text-base-content/60">{entry.kind}</span>{#if entry.reason}<span class="text-base-content/60">{" · "}{entry.reason}</span>{/if}</li>
                              {/each}
                            </ul>
                          </details>
                        {/if}
                        {#if notice.hint}<p class="text-xs text-base-content/70">{notice.hint}</p>{/if}
                        {#if notice.heldBack}<p class="font-medium">{notice.heldBack}</p>{/if}
                        {#if notice.action?.kind === "reshare"}
                          <div class="mt-1">
                            <button class="btn btn-sm btn-outline btn-outline-hover" type="button" disabled={reshareJobId !== ""} on:click={handleReshare}>
                              {#if reshareJobId && reshareJobId === selectedJob.job.id}<span class="loading loading-spinner loading-xs" aria-hidden="true"></span>{/if}
                              {notice.action.label}
                            </button>
                            <p class="mt-1 text-xs text-base-content/60">{notice.action.note}</p>
                            {#if reshareError && reshareError.jobId === selectedJob.job.id}<p class="mt-1 text-xs text-error" role="alert">{reshareError.message}</p>{/if}
                            {#if reshareLine}
                              <p class="mt-1 text-xs text-base-content/75" role="status">{reshareLine}</p>
                            {/if}
                          </div>
                        {/if}
                      </div>
                    </div>
                  {/each}
                  {#if reshareLine && !shareNoticeShown}
                    <div class="flex items-start gap-2 rounded-box border p-3 text-sm {outcomeToneClasses.info}" role="status">
                      <Info size={16} class="mt-0.5 shrink-0 {outcomeIconClasses.info}" aria-hidden="true" />
                      <p class="font-medium">{reshareLine}</p>
                    </div>
                  {/if}
                  {#if summaryPlan}
                    {#if confirmingSummary}
                      <div
                        class="grid gap-2 rounded-box border border-warning/50 bg-warning/10 p-3"
                        role="alertdialog"
                        aria-label={summaryConfirmTitle(summaryPlan)}
                      >
                        <p class="text-sm font-semibold">{summaryConfirmTitle(summaryPlan)}</p>
                        <p class="text-sm">{summaryCostText(summaryPlan)}</p>
                        <p class="text-xs text-base-content/70">{summaryEndpointText(summaryPlan)}</p>
                        {#if summaryReplaceText(summaryPlan)}
                          <p class="text-xs text-base-content/70">{summaryReplaceText(summaryPlan)}</p>
                        {/if}
                        <div class="flex flex-wrap items-center gap-2">
                          <button class="btn btn-sm btn-primary" type="button" disabled={submittingSummary} on:click={handleWriteSummary}>
                            {summaryActionLabel(summaryPlan)}
                          </button>
                          <button class="btn btn-sm btn-ghost" type="button" disabled={submittingSummary} on:click={() => (confirmingSummary = false)}>
                            {$LL.common.action.cancel()}
                          </button>
                        </div>
                      </div>
                    {:else if summaryPlan.available}
                      <div>
                        <button class="btn btn-sm" type="button" on:click={() => (confirmingSummary = true)}>
                          <FileText size={15} aria-hidden="true" />{summaryActionLabel(summaryPlan)}
                        </button>
                      </div>
                    {:else if summaryUnavailableText(summaryPlan) && !(summaryPlan.reason === "no_local_copy" && rerunBlockedReason && rerunVisible)}
                      <!-- With the meeting's files gone, the rerun warning below
                           already says why; the summary note would repeat it (D-816). -->
                      <p class="text-xs text-base-content/60">{summaryUnavailableText(summaryPlan)}</p>
                    {/if}
                  {/if}
                  {#if summaryError}
                    <p class="text-sm text-error" role="alert">{summaryError}</p>
                  {/if}
                  {#if rerunBlockedReason && rerunVisible}
                    <div class="flex items-start gap-2 rounded-box border border-warning/50 bg-warning/10 p-3 text-sm" role="status">
                      <TriangleAlert size={16} class="mt-0.5 shrink-0 text-warning" aria-hidden="true" />
                      <p>{rerunBlockedReason}</p>
                    </div>
                  {/if}
                  {#if hasResourceNotice(selectedJob.job)}
                    <div class="grid gap-2 rounded-box border border-warning/50 bg-warning/10 p-3">
                      <div class="flex items-center gap-2 text-warning">
                        <TriangleAlert size={16} class="shrink-0" aria-hidden="true" />
                        <p class="text-sm font-semibold">{jobStatusLabel(selectedJob.job)}</p>
                      </div>
                      {#if selectedJob.job.error}
                        <p class="text-xs break-words text-base-content/75">{selectedJob.job.error}</p>
                      {/if}
                      <dl class="flex flex-wrap gap-x-4 gap-y-1 text-xs">
                        {#if isBuildWaitingForResources(selectedJob.job)}
                          <div class="flex gap-1">
                            <dt class="text-base-content/50">{$LL.app.operator.console.next_retry()}</dt>
                            <dd>{formatTimestamp(selectedJob.job.build_retry_not_before)}</dd>
                          </div>
                        {/if}
                        <div class="flex gap-1">
                          <dt class="text-base-content/50">{$LL.app.operator.console.resource_deferrals()}</dt>
                          <dd>{selectedJob.job.build_deferral_count}</dd>
                        </div>
                      </dl>
                    </div>
                  {:else if hasJobError(selectedJob.job)}
                    <div class="grid gap-1 rounded-box border border-error/40 bg-error/10 p-3">
                      {#if selectedJob.job.error}
                        <p class="text-sm font-medium text-error">{selectedJob.job.error}</p>
                      {:else}
                        <p class="text-sm font-medium text-error">{jobStatusLabel(selectedJob.job)}</p>
                      {/if}
                      {#if cleanStopDetail(selectedJob.job.record_stop_detail, selectedJob.job.record_exit_code, true)}
                        <p class="text-xs break-words text-base-content/70">{cleanStopDetail(selectedJob.job.record_stop_detail, selectedJob.job.record_exit_code, true)}</p>
                      {/if}
                      <dl class="flex flex-wrap gap-x-4 gap-y-1 text-xs">
                        {#if selectedJob.job.record_exit_code !== null}
                          <div class="flex gap-1">
                            <dt class="text-base-content/50">{$LL.app.operator.console.exit_code()}</dt>
                            <dd class="font-mono">{selectedJob.job.record_exit_code}</dd>
                          </div>
                        {/if}
                        {#if selectedJob.job.stop_reason}
                          <div class="flex gap-1">
                            <dt class="text-base-content/50">{$LL.app.operator.console.reason()}</dt>
                            <dd class="font-mono">{formatStopReason(selectedJob.job.stop_reason)}</dd>
                          </div>
                        {/if}
                        {#if selectedJob.job.stop_requested_at}
                          <div class="flex gap-1">
                            <dt class="text-base-content/50">{$LL.app.operator.console.stop_requested()}</dt>
                            <dd>{formatTimestamp(selectedJob.job.stop_requested_at)}</dd>
                          </div>
                        {/if}
                      </dl>
                    </div>
                  {:else if selectedJob.job.state === "stopped"}
                    <div class="grid gap-1 rounded-box border border-warning/40 bg-warning/10 p-3">
                      <p class="text-sm font-medium text-warning">
                        {selectedJob.job.stop_reason ? formatStopReason(selectedJob.job.stop_reason) : $LL.app.operator.console.recording_stopped()}
                      </p>
                      {#if cleanStopDetail(selectedJob.job.record_stop_detail, selectedJob.job.record_exit_code, false)}
                        <p class="text-xs break-words text-base-content/70">{cleanStopDetail(selectedJob.job.record_stop_detail, selectedJob.job.record_exit_code, false)}</p>
                      {/if}
                      <dl class="flex flex-wrap gap-x-4 gap-y-1 text-xs">
                        {#if selectedJob.job.stop_requested_at}
                          <div class="flex gap-1">
                            <dt class="text-base-content/50">{$LL.app.operator.console.stop_requested()}</dt>
                            <dd>{formatTimestamp(selectedJob.job.stop_requested_at)}</dd>
                          </div>
                        {/if}
                      </dl>
                    </div>
                  {/if}
                  <!-- What an administrator checks a run for. The rest is folded
                       into Technical details below (D-816). -->
                  <dl class="mt-2 grid grid-cols-2 gap-x-6 gap-y-3 lg:grid-cols-3">
                    <div>
                      <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.field.recorded()}</dt>
                      <dd class="text-sm">
                        {#if selectedJob.job.record_started_at}
                          {formatTimestamp(selectedJob.job.record_started_at)}{#if formatDuration(selectedJob.job.record_started_at, selectedJob.job.record_finished_at)}<span class="text-base-content/60">{" · "}{formatDuration(selectedJob.job.record_started_at, selectedJob.job.record_finished_at)}</span>{/if}
                        {:else}
                          {@render notAvailable()}
                        {/if}
                      </dd>
                    </div>
                    <div>
                      <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.field.processing()}</dt>
                      <dd class="text-sm" class:text-warning={runSpeed !== null && runSpeed.factor !== null && runSpeed.factor < 1} title={runSpeed ? processingHint(runSpeed) : undefined}>
                        {#if runSpeed}{describeProcessing(runSpeed)}{:else}{@render notAvailable()}{/if}
                      </dd>
                    </div>
                    <div>
                      <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.field.completed()}</dt>
                      <dd class="text-sm">
                        {#if selectedJob.job.completed_at}{formatTimestamp(selectedJob.job.completed_at)}{:else}{@render notAvailable()}{/if}
                      </dd>
                    </div>
                    <div class="min-w-0">
                      <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.field.speech_model()}</dt>
                      <dd class="text-sm break-words">
                        {#if outcomeSource && speechModelLabel(outcomeSource)}{speechModelLabel(outcomeSource)}{:else}{@render notAvailable()}{/if}
                      </dd>
                    </div>
                    <div class="min-w-0">
                      <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.field.summary_model()}</dt>
                      <dd class="text-sm break-words">
                        {@render fieldValue(outcomeSource ? attemptSummary(outcomeSource) : null)}
                      </dd>
                    </div>
                    <div>
                      <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.field.capture()}</dt>
                      <dd class="text-sm">{captureModeLabel(selectedJob.job.request_json)}</dd>
                    </div>
                    <div>
                      <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.field.stopped_because()}</dt>
                      <dd class="text-sm">
                        {#if selectedJob.job.stop_reason}{formatStopReason(selectedJob.job.stop_reason)}{:else}{@render notAvailable()}{/if}
                      </dd>
                    </div>
                    <div class="min-w-0">
                      <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.field.conversation()}</dt>
                      <dd class="text-sm">
                        {#if isHttpUrl(requestUrlLabel(selectedJob.job.request_json))}
                          <a
                            class="link link-hover text-primary"
                            href={requestUrlLabel(selectedJob.job.request_json)}
                            title={requestUrlLabel(selectedJob.job.request_json)}
                            target="_blank"
                            rel="noreferrer noopener"
                          >
                            {$LL.app.operator.console.open_in_talk()}
                          </a>
                        {:else}
                          {@render notAvailable()}
                        {/if}
                      </dd>
                    </div>
                    <div class="min-w-0">
                      <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.field.recording()}</dt>
                      <dd class="text-sm">
                        {#if recordingHref}
                          <a class="link link-hover text-primary" href={recordingHref} target="_blank" rel="noreferrer noopener">
                            {$LL.app.operator.console.open_in_files()}
                          </a>
                        {:else}
                          {@render notAvailable()}
                        {/if}
                      </dd>
                    </div>
                  </dl>

                  <details class="group">
                    <summary class="flex w-fit cursor-pointer list-none items-center gap-1 text-xs text-base-content/60 hover:text-base-content">
                      <ChevronRight size={12} class="shrink-0 transition-transform group-open:rotate-90" aria-hidden="true" />
                      {$LL.app.operator.console.technical_details()}
                    </summary>
                    <dl class="mt-2 grid grid-cols-2 gap-x-6 gap-y-3 lg:grid-cols-3">
                      <div class="col-span-2 min-w-0 lg:col-span-3">
                        <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.field.run_id()}</dt>
                        <dd class="font-mono text-xs break-all">{selectedJob.job.id}</dd>
                      </div>
                      <div>
                        <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.field.created()}</dt>
                        <dd class="text-sm">{formatTimestamp(selectedJob.job.created_at)}</dd>
                      </div>
                      <div>
                        <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.field.updated()}</dt>
                        <dd class="text-sm">{formatTimestamp(selectedJob.job.updated_at)}</dd>
                      </div>
                      <div>
                        <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.stop_requested()}</dt>
                        <dd class="text-sm">
                          {#if selectedJob.job.stop_requested_at}{formatTimestamp(selectedJob.job.stop_requested_at)}{:else}{@render notAvailable()}{/if}
                        </dd>
                      </div>
                      <div>
                        <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.field.interrupted()}</dt>
                        <dd class="text-sm">
                          {#if selectedJob.job.interrupted_at}{formatTimestamp(selectedJob.job.interrupted_at)}{:else}{@render notAvailable()}{/if}
                        </dd>
                      </div>
                    </dl>
                  </details>
                </section>

                <section class="mt-4 grid gap-3">
                  <div class="flex items-center gap-2">
                    <h3 class="font-semibold">{$LL.app.operator.console.attempt_history()}</h3>
                    <span class="badge badge-outline badge-sm border-base-content/20 text-base-content">
                      {selectedJob.attempts.length}
                    </span>
                  </div>
                  <div class="grid gap-3">
                    {#each selectedJob.attempts as attempt (attempt.attempt_number)}
                      <!-- Only a transcription is timed against the meeting: a summary
                           written in a minute is not the server running at 58×. -->
                      {@const attemptSpeed = attempt.state === "succeeded" ? processingSpeed(attempt, processesCapture(attempt) ? meetingMs : null) : null}
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
                            <span class="text-sm font-medium text-base-content">{$LL.app.operator.console.attempt({ number: formatCount(attempt.attempt_number) })}</span>
                            <span class="ml-2">{triggerKindLabel(attempt.trigger_kind)}</span>
                            <span class="mx-1.5 text-base-content/40">·</span>
                            <span class={jobStatusToneClass(attempt)}>{jobStatusLabel(attempt)}</span>
                            {#if attemptSpeed}
                              <span class="text-base-content/50"> · {$LL.app.operator.console.processed_in({ span: formatSpan(attemptSpeed.ms) })}</span>
                            {:else if formatDuration(attempt.record_started_at, attempt.record_finished_at)}
                              <span class="text-base-content/50"> · {$LL.app.operator.console.recorded_for({ span: formatDuration(attempt.record_started_at, attempt.record_finished_at) ?? "" })}</span>
                            {/if}
                          </span>
                        </summary>

                        <div class="grid gap-3 bg-base-200 px-3 pt-3 pb-3">
                          {#if hasResourceNotice(attempt)}
                            <div class="grid gap-2 rounded-box border border-warning/50 bg-warning/10 p-3">
                              <div class="flex items-center gap-2 text-warning">
                                <TriangleAlert size={16} class="shrink-0" aria-hidden="true" />
                                <p class="text-sm font-semibold">{jobStatusLabel(attempt)}</p>
                              </div>
                              {#if attempt.error}
                                <p class="text-xs break-words text-base-content/75">{attempt.error}</p>
                              {/if}
                              <dl class="flex flex-wrap gap-x-4 gap-y-1 text-xs">
                                {#if isBuildWaitingForResources(attempt)}
                                  <div class="flex gap-1">
                                    <dt class="text-base-content/50">{$LL.app.operator.console.next_retry()}</dt>
                                    <dd>{formatTimestamp(attempt.build_retry_not_before)}</dd>
                                  </div>
                                {/if}
                                <div class="flex gap-1">
                                  <dt class="text-base-content/50">{$LL.app.operator.console.resource_deferrals()}</dt>
                                  <dd>{attempt.build_deferral_count}</dd>
                                </div>
                              </dl>
                            </div>
                          {:else if hasJobError(attempt)}
                            <div class="grid gap-1 rounded-box border border-error/40 bg-error/10 p-3">
                              {#if attempt.error}
                                <p class="text-sm font-medium text-error">{attempt.error}</p>
                              {:else}
                                <p class="text-sm font-medium text-error">{jobStatusLabel(attempt)}</p>
                              {/if}
                              {#if cleanStopDetail(attempt.record_stop_detail, attempt.record_exit_code, true)}
                                <p class="text-xs break-words text-base-content/70">{cleanStopDetail(attempt.record_stop_detail, attempt.record_exit_code, true)}</p>
                              {/if}
                              <dl class="flex flex-wrap gap-x-4 gap-y-1 text-xs">
                                {#if attempt.record_exit_code !== null}
                                  <div class="flex gap-1">
                                    <dt class="text-base-content/50">{$LL.app.operator.console.exit_code()}</dt>
                                    <dd class="font-mono">{attempt.record_exit_code}</dd>
                                  </div>
                                {/if}
                                {#if attempt.stop_reason}
                                  <div class="flex gap-1">
                                    <dt class="text-base-content/50">{$LL.app.operator.console.reason()}</dt>
                                    <dd class="font-mono">{formatStopReason(attempt.stop_reason)}</dd>
                                  </div>
                                {/if}
                              </dl>
                            </div>
                          {:else if attempt.state === "stopped"}
                            <div class="grid gap-1 rounded-box border border-warning/40 bg-warning/10 p-3">
                              <p class="text-sm font-medium text-warning">
                                {attempt.stop_reason ? formatStopReason(attempt.stop_reason) : $LL.app.operator.console.recording_stopped()}
                              </p>
                              {#if cleanStopDetail(attempt.record_stop_detail, attempt.record_exit_code, false)}
                                <p class="text-xs break-words text-base-content/70">{cleanStopDetail(attempt.record_stop_detail, attempt.record_exit_code, false)}</p>
                              {/if}
                            </div>
                          {/if}

                          {@render stageBar(stageProgress(attempt))}

                          <dl class="grid grid-cols-3 gap-x-6 gap-y-3">
                            <div>
                              <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.field.queued()}</dt>
                              <dd class="text-sm">
                                {#if attempt.record_queued_at ?? attempt.build_queued_at}{formatTimestamp(attempt.record_queued_at ?? attempt.build_queued_at)}{:else}{@render notAvailable()}{/if}
                              </dd>
                            </div>
                            <div>
                              <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.field.processing()}</dt>
                              <dd class="text-sm" class:text-warning={attemptSpeed !== null && attemptSpeed.factor !== null && attemptSpeed.factor < 1} title={attemptSpeed ? processingHint(attemptSpeed) : undefined}>
                                {#if attemptSpeed}{describeProcessing(attemptSpeed)}{:else}{@render notAvailable()}{/if}
                              </dd>
                            </div>
                            <div>
                              <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.field.completed()}</dt>
                              <dd class="text-sm">
                                {#if attempt.completed_at}{formatTimestamp(attempt.completed_at)}{:else}{@render notAvailable()}{/if}
                              </dd>
                            </div>
                            <div>
                              <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.reason()}</dt>
                              <dd class="text-sm">{attemptReason(attempt, formatStopReason)}</dd>
                            </div>
                            <div class="min-w-0">
                              <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.field.speech_model()}</dt>
                              <dd class="text-sm break-words" title={attempt.transcription_revision && speechModelLabel(attempt) ? $LL.app.operator.console.revision({ revision: attempt.transcription_revision }) : undefined}>
                                {@render fieldValue(attemptSpeech(attempt, selectedJob.attempts))}
                              </dd>
                            </div>
                            <div class="min-w-0">
                              <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{$LL.app.operator.console.field.summary_model()}</dt>
                              <dd class="text-sm break-words">{@render fieldValue(attemptSummary(attempt))}</dd>
                            </div>
                          </dl>

                          <div class="grid gap-2">
                            <div class="flex flex-wrap items-center gap-1.5">
                              {#each attemptLogs(attempt) as log (log.stage)}
                                {@const isOpen = openLog?.attempt === attempt.attempt_number && openLog?.stage === log.stage}
                                <button
                                  class="btn btn-sm {isOpen ? 'btn-active' : 'btn-outline border-base-content/20'} font-normal"
                                  type="button"
                                  disabled={!log.present}
                                  title={log.present
                                    ? $LL.app.operator.console.show_log[log.stage]()
                                    : log.written
                                      ? $LL.app.operator.console.log_gone()
                                      : log.stage === "record" && attempt.trigger_kind !== "initial"
                                        ? $LL.app.operator.console.log_not_part()
                                        : $LL.app.operator.console.log_not_written()}
                                  aria-expanded={isOpen}
                                  on:click={() => selectedJob && toggleLog(selectedJob.job.id, attempt.attempt_number, log)}
                                >
                                  <FileText size={14} aria-hidden="true" />{$LL.app.operator.console.log_name[log.stage]()}
                                </button>
                              {/each}
                            </div>
                            {#if openLog && openLog.attempt === attempt.attempt_number}
                              <div class="grid gap-1 rounded-box border border-base-300 bg-base-100" role="region" aria-label={$LL.app.operator.console.log_name[openLog.stage]()}>
                                <div class="flex items-center gap-2 border-b border-base-300 px-3 py-1.5 text-xs">
                                  <span class="font-medium">{$LL.app.operator.console.log_name[openLog.stage]()}</span>
                                  {#if openLog.truncated}
                                    <span class="text-base-content/55">· {$LL.app.operator.console.log_truncated()}</span>
                                  {/if}
                                  <span class="flex-1"></span>
                                  {#if operatorClient}
                                    <a class="btn btn-ghost btn-xs" href={operatorClient.attemptLogUrl(openLog.jobId, openLog.attempt, openLog.stage, true)} download>
                                      <Download size={12} aria-hidden="true" />{$LL.app.operator.console.download()}
                                    </a>
                                  {/if}
                                  <button class="btn btn-ghost btn-xs btn-square" type="button" aria-label={$LL.app.operator.console.close_log()} on:click={() => (openLog = null)}>
                                    <X size={12} aria-hidden="true" />
                                  </button>
                                </div>
                                {#if openLog.loading}
                                  <p class="px-3 py-2 text-xs text-base-content/60">{$LL.app.operator.console.loading()}</p>
                                {:else if openLog.error}
                                  <p class="px-3 py-2 text-xs text-error" role="alert">{openLog.error}</p>
                                {:else if openLog.text === ""}
                                  <p class="px-3 py-2 text-xs text-base-content/60">{$LL.app.operator.console.log_empty()}</p>
                                {:else}
                                  <pre class="attempt-log max-h-80 overflow-auto px-3 py-2 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all">{openLog.text}</pre>
                                {/if}
                              </div>
                            {/if}
                          </div>

                          <!-- On-disk paths are for debugging inside the
                               container; folded so they do not bury the outcome (D-816). -->
                          <details class="group/tech">
                            <summary class="flex w-fit cursor-pointer list-none items-center gap-1 text-xs text-base-content/60 hover:text-base-content">
                              <ChevronRight size={12} class="shrink-0 transition-transform group-open/tech:rotate-90" aria-hidden="true" />
                              {$LL.app.operator.console.technical_details()}
                            </summary>
                            <dl class="mt-2 grid gap-x-6 gap-y-3">
                              {#each attemptPaths(attempt) as entry (entry.label)}
                                <div class="min-w-0">
                                  <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">{entry.label}</dt>
                                  <dd class="font-mono text-xs break-all">
                                    {#if entry.path}{entry.path}{entry.missing ? ` ${$LL.app.operator.console.no_longer_on_disk()}` : ""}{:else}{@render notAvailable()}{/if}
                                  </dd>
                                </div>
                              {/each}
                            </dl>
                          </details>
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
      </div>
    {/if}
  </div>

  {#if panel !== "recordings"}
    <!-- Mounted only while its row is selected, so a panel's settings fetch
         happens on entry — the same contract the shell applies to this surface. -->
    <Settings {panel} on:panel={(event) => selectPanel(event.detail)} />
  {/if}
  </div>
</div>

<style>
  .op-recordings {
    padding: 16px 20px 48px;
  }
  .op-split {
    border: 1px solid color-mix(in oklch, var(--color-base-content) 12%, var(--color-base-200));
    border-radius: var(--radius-box, 0.5rem);
  }
  .op-detail-pane {
    border-left-color: color-mix(in oklch, var(--color-base-content) 7%, var(--color-base-200));
  }
  .run-card {
    background-color: color-mix(in oklch, var(--color-base-content) 4%, var(--color-base-200));
    border: 1px solid color-mix(in oklch, var(--color-base-content) 9%, var(--color-base-200));
    border-radius: var(--radius-box, 0.5rem);
    box-shadow: none;
  }
  .run-card:hover {
    background-color: color-mix(in oklch, var(--color-base-content) 8%, var(--color-base-200));
    border-color: color-mix(in oklch, var(--color-base-content) 16%, var(--color-base-200));
  }
  .run-card.run-card-selected,
  .run-card.run-card-selected:hover {
    background-color: color-mix(in oklch, var(--color-base-content) 10%, var(--color-base-200));
    border: 2px solid var(--color-base-content);
    box-shadow: inset 3px 0 0 var(--color-base-content);
  }
  .op-pane-head h2 {
    font-size: 14px;
    font-weight: 650;
  }
  .run-detail-card {
    padding: 14px;
  }
  @media (max-width: 720px) {
    .op-recordings {
      padding: 8px 16px 40px;
    }
  }
  .op-nav-item {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 6px 16px;
    text-align: left;
    cursor: pointer;
    background: none;
    border: 0;
    color: var(--color-base-content);
    font-size: 0.875rem;
  }
  .op-shell {
    position: relative;
  }
  .op-content {
    display: contents;
  }
  @media (min-width: 721px) {
    .op-shell {
      height: 100%;
      min-height: 0;
      overflow: hidden;
    }
    .op-content {
      display: block;
      min-width: 0;
      height: 100%;
      overflow-y: auto;
      overscroll-behavior: contain;
    }
    .op-nav {
      height: 100%;
      overflow-y: auto;
      overscroll-behavior: contain;
    }
  }
  .op-nav {
    background-color: var(--color-base-200);
  }
  .op-nav-bar {
    display: none;
  }
  @media (min-width: 721px) {
    .op-shell::before {
      content: "";
      position: absolute;
      top: 0;
      bottom: 0;
      left: 267px;
      width: 1px;
      background-color: var(--color-base-300);
      pointer-events: none;
      z-index: 2;
    }
  }
  /* Browse's rooms button in the Operator: the same button, at the height of
     the search field it sits beside there (MeetingList's .search-field). */
  .op-nav-button {
    display: flex;
    align-items: center;
    gap: 6px;
    max-width: 100%;
    height: 40px;
    padding: 0 12px;
    cursor: pointer;
    background-color: var(--color-base-100);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-field, 0.5rem);
    font-size: 0.8125rem;
    font-weight: 550;
    color: var(--color-base-content);
  }
  .op-nav-button:hover {
    border-color: color-mix(in oklch, var(--color-base-content) 35%, transparent);
  }
  .op-nav-button span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .op-nav-scrim {
    display: none;
  }
  @media (max-width: 720px) {
    .op-shell {
      align-content: start;
    }
    .op-nav-bar {
      position: sticky;
      top: 0;
      z-index: 30;
      display: flex;
      padding: 1rem;
      background-color: var(--color-base-200);
    }
    .op-nav-scrim {
      position: absolute;
      top: var(--op-nav-top, 0);
      left: 0;
      right: 0;
      height: var(--op-nav-height, 100%);
      z-index: 39;
      display: block;
      padding: 0;
      border: 0;
      cursor: pointer;
      background-color: oklch(0% 0 0 / 0.55);
      -webkit-backdrop-filter: blur(3px);
      backdrop-filter: blur(3px);
    }
    .op-nav {
      position: absolute;
      top: var(--op-nav-top, 0);
      height: var(--op-nav-height, 100%);
      left: 0;
      z-index: 40;
      width: min(268px, 82vw);
      overflow-y: auto;
      overscroll-behavior: contain;
      border-right: 1px solid var(--color-base-300);
      box-shadow: 6px 0 24px oklch(0% 0 0 / 0.18);
      transform: translateX(-100%);
      visibility: hidden;
      transition:
        transform 0.3s cubic-bezier(0.32, 0.72, 0, 1),
        visibility 0s linear 0.3s;
    }
    .op-nav[data-open="true"] {
      transform: none;
      visibility: visible;
      transition:
        transform 0.3s cubic-bezier(0.32, 0.72, 0, 1),
        visibility 0s;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .op-nav {
      transition: none;
    }
  }
  .op-nav-item:hover {
    background-color: color-mix(in oklch, var(--color-base-content) 6%, transparent);
  }
  .op-nav-item[aria-current="page"] {
    background-color: color-mix(in oklch, var(--color-primary) 35%, transparent);
    box-shadow: inset 3px 0 0 var(--color-primary);
  }

  /* The run console keeps its DOM while a settings panel is showing (list
     scroll, the open run, its expanded attempts), so it is hidden rather than
     unmounted. Scoped CSS, not a utility: display:none has to win against the
     element's own `flex`, and Svelte's scoping class is what guarantees it. */
  .cassini-op-panel-inactive {
    display: none;
  }

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
