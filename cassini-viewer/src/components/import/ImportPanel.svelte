<script lang="ts">
  // The Import sheet (D-828): a side sheet over the meeting list, opened from
  // the Import button beside search. It stays mounted while closed, so uploads,
  // the jobs poll and the button's status keep going.
  //
  // Dropped files parse LOCALLY (viewer/imports.ts) and list below the drop
  // zone with per-meeting sharing; "Import" stages each file into the caller's
  // own Files and hands the operator only the staged paths. "Recent imports"
  // shows the durable server-side jobs — they survive navigation and reload,
  // and "Clear finished" is a display action only (the rows are retention
  // provenance and never leave the operator's database).
  import { createEventDispatcher, onDestroy, onMount } from "svelte";
  import { Check, ChevronDown, ChevronUp, CircleAlert, CloudUpload, FileAudio, FileText, Lock, Users, X } from "@lucide/svelte";

  import type { DataProvider } from "../../viewer/dataProvider";
  import {
    describeImportSharing,
    formatImportDate,
    formatImportDuration,
    importJobTerminal,
    parseImportPreview,
    type ImportJobRecord,
    type ImportPreview,
    type ImportSharee,
  } from "../../viewer/imports";
  import ShareePicker from "./ShareePicker.svelte";
  import type { ImportStatus } from "./importStatus";

  export let dataProvider: DataProvider;
  // Whether the sheet is showing. Opening it refreshes the jobs and counts
  // every failure on screen as seen, which is what clears the button's alert.
  export let open = false;
  // Meeting ids the catalog lists, so a finished row offers Open only when the
  // meeting is actually there to open.
  export let openableIds: ReadonlySet<string> = new Set();

  const dispatch = createEventDispatcher<{
    status: ImportStatus;
    imported: void;
    close: void;
    openMeeting: string;
  }>();

  type PendingStatus = "parsing" | "ready" | "invalid" | "staging" | "failed";
  interface PendingItem {
    key: string;
    file: File;
    status: PendingStatus;
    reason?: string;
    preview?: ImportPreview;
    audience: ImportSharee[];
    sentBytes: number;
  }

  let pending: PendingItem[] = [];
  let jobs: ImportJobRecord[] = [];
  let jobsError = "";
  let submitError = "";
  let jobsLoaded = false;
  let submitting = false;
  let dragOver = false;
  // The one row whose sharing list is unfolded.
  let expandedKey = "";
  let fileInput: HTMLInputElement;
  let keySequence = 0;

  // "Clear finished" hides done/canceled rows in THIS browser only, and the
  // failures already seen stop alerting. Plain per-viewer conveniences: absent
  // or blocked storage simply shows them again.
  const CLEARED_KEY = "cassini.imports.cleared.v1";
  const SEEN_KEY = "cassini.imports.seenFailures.v1";
  let cleared: Set<string> = readSet(CLEARED_KEY);
  let seenFailures: Set<string> = readSet(SEEN_KEY);

  function readSet(key: string): Set<string> {
    try {
      const raw = localStorage.getItem(key);
      return new Set(raw ? (JSON.parse(raw) as string[]) : []);
    } catch {
      return new Set();
    }
  }

  function persistSet(key: string, values: Set<string>) {
    try {
      localStorage.setItem(key, JSON.stringify([...values]));
    } catch {
      // A private window or blocked storage: this lasts for this page only.
    }
  }

  // --- the jobs poll -------------------------------------------------------

  let pollTimer: ReturnType<typeof setInterval> | null = null;
  let previousStates = new Map<string, string>();
  // How many imports finished since the last "N imported" went quiet.
  let justImported = 0;
  let justImportedTimer: ReturnType<typeof setTimeout> | null = null;
  const JUST_IMPORTED_MS = 6000;

  async function refreshJobs() {
    if (!dataProvider.listImports) return;
    try {
      const listed = await dataProvider.listImports();
      let landed = 0;
      for (const job of listed) {
        const before = previousStates.get(job.id);
        if (before && before !== "done" && job.state === "done") landed++;
      }
      if (landed > 0) {
        // A meeting just landed: the list should not wait a minute for its
        // poll to notice, and the button says so for a few seconds.
        dispatch("imported");
        justImported += landed;
        if (justImportedTimer !== null) clearTimeout(justImportedTimer);
        justImportedTimer = setTimeout(() => {
          justImported = 0;
          justImportedTimer = null;
        }, JUST_IMPORTED_MS);
      }
      previousStates = new Map(listed.map((job) => [job.id, job.state]));
      jobs = listed;
      jobsError = "";
    } catch (error) {
      jobsError = error instanceof Error ? error.message : String(error);
    } finally {
      jobsLoaded = true;
    }
    syncPolling();
  }

  function syncPolling() {
    // refreshJobs calls this before Svelte recomputes activeJobs. Read the
    // fresh jobs directly so retrying an idle job immediately restarts polling.
    const wanted = jobs.some((job) => !importJobTerminal(job.state)) || submitting;
    if (wanted && pollTimer === null) {
      pollTimer = setInterval(() => void refreshJobs(), 2500);
    } else if (!wanted && pollTimer !== null) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  $: if (open) {
    // Opening refreshes once; the interval only runs while work is live.
    void refreshJobs();
  }

  onMount(() => void refreshJobs());
  onDestroy(() => {
    if (pollTimer !== null) clearInterval(pollTimer);
    if (justImportedTimer !== null) clearTimeout(justImportedTimer);
  });

  // --- what the Import button says ------------------------------------------

  // A failure is keyed by its last update, so the same attempt failing again
  // after a Retry alerts again.
  const failureKey = (job: ImportJobRecord) => `${job.id}@${job.updatedAt}`;
  const isFailure = (job: ImportJobRecord) => job.state === "failed" || job.state === "upload_failed";

  $: if (open) markFailuresSeen(jobs);

  function markFailuresSeen(list: ImportJobRecord[]) {
    let changed = false;
    for (const job of list) {
      if (isFailure(job) && !seenFailures.has(failureKey(job))) {
        seenFailures.add(failureKey(job));
        changed = true;
      }
    }
    if (changed) {
      seenFailures = seenFailures;
      persistSet(SEEN_KEY, seenFailures);
    }
  }

  $: activeJobs = jobs.filter((job) => !importJobTerminal(job.state));
  $: stagingCount = pending.filter((item) => item.status === "staging").length;
  $: unseenFailureCount = jobs.filter((job) => isFailure(job) && !seenFailures.has(failureKey(job))).length;
  $: runningCount = activeJobs.length + stagingCount;
  $: status = (
    unseenFailureCount > 0
      ? { kind: "failed", count: unseenFailureCount }
      : runningCount > 0 || submitting
        ? { kind: "running", count: Math.max(runningCount, 1) }
        : justImported > 0
          ? { kind: "done", count: justImported }
          : { kind: "idle", count: 0 }
  ) satisfies ImportStatus;
  $: dispatch("status", status);

  // --- drop & parse --------------------------------------------------------

  function acceptFiles(files: FileList | File[] | null) {
    if (!files) return;
    for (const file of Array.from(files)) {
      const item: PendingItem = {
        key: `pf_${++keySequence}`,
        file,
        status: "parsing",
        audience: [],
        sentBytes: 0,
      };
      pending = [...pending, item];
      void parseOne(item);
    }
  }

  async function parseOne(item: PendingItem) {
    try {
      const preview = await parseImportPreview(item.file);
      patch(item.key, { status: "ready", preview });
    } catch (error) {
      patch(item.key, {
        status: "invalid",
        reason: error instanceof Error ? error.message : String(error),
      });
    }
  }

  function patch(key: string, changes: Partial<PendingItem>) {
    pending = pending.map((item) => (item.key === key ? { ...item, ...changes } : item));
  }

  function removePending(key: string) {
    pending = pending.filter((item) => item.key !== key);
    if (expandedKey === key) expandedKey = "";
  }

  function handleDrop(event: DragEvent) {
    event.preventDefault();
    dragOver = false;
    acceptFiles(event.dataTransfer?.files ?? null);
  }

  // --- sharing selection ----------------------------------------------------

  // A meeting is shared exactly when someone besides the importer is on it:
  // removing the last person makes it private again.
  function addSharee(key: string, sharee: ImportSharee) {
    const item = pending.find((candidate) => candidate.key === key);
    if (!item || item.audience.some((have) => have.type === sharee.type && have.id === sharee.id)) return;
    patch(key, { audience: [...item.audience, sharee] });
  }

  function removeSharee(key: string, sharee: ImportSharee) {
    const item = pending.find((candidate) => candidate.key === key);
    if (!item) return;
    patch(key, { audience: item.audience.filter((have) => !(have.type === sharee.type && have.id === sharee.id)) });
  }

  // Adds one person to every meeting still waiting to be imported.
  function addShareeToAll(sharee: ImportSharee) {
    pending = pending.map((item) =>
      item.status === "ready" && !item.audience.some((have) => have.type === sharee.type && have.id === sharee.id)
        ? { ...item, audience: [...item.audience, sharee] }
        : item,
    );
  }

  const SHAREE_GROUPS: Array<{ type: ImportSharee["type"]; heading: string }> = [
    { type: "user", heading: "People" },
    { type: "group", heading: "Groups" },
    { type: "circle", heading: "Teams" },
  ];

  // --- submit ---------------------------------------------------------------

  $: readyCount = pending.filter((item) => item.status === "ready").length;
  $: skippedCount = pending.filter((item) => item.status === "invalid").length;

  async function submit() {
    if (submitting || readyCount === 0) return;
    if (!dataProvider.stageImportUpload || !dataProvider.startImports) return;
    submitting = true;
    expandedKey = "";
    syncPolling();
    submitError = "";
    try {
      for (const item of pending.filter((candidate) => candidate.status === "ready")) {
        patch(item.key, { status: "staging", sentBytes: 0 });
        try {
          // Sharing travels with the registration, so a retry of this attempt
          // reuses it; the submit below names the staged path only.
          const sharing = {
            share: item.audience.length > 0,
            audience: item.audience.map(({ type, id, label }) => ({ type, id, label })),
          };
          const stagedPath = await dataProvider.stageImportUpload(item.file, (sent) => {
            patch(item.key, { sentBytes: sent });
          }, sharing);
          // This upload is already durable. Queue it independently so a large
          // selection cannot upload fully and then hit the 50-file batch cap.
          removePending(item.key);
          await dataProvider.startImports([{ stagedPath }]);
        } catch (error) {
          submitError = error instanceof Error ? error.message : String(error);
          if (error instanceof Error && "importJobId" in error) removePending(item.key);
          else patch(item.key, {
            status: "failed",
            reason: error instanceof Error ? error.message : String(error),
          });
        }
      }
      await refreshJobs();
    } catch (error) {
      submitError = error instanceof Error ? error.message : String(error);
    } finally {
      submitting = false;
      syncPolling();
    }
  }

  // --- recent imports -------------------------------------------------------

  // "Finished" is everything settled that needs nothing more from you: done,
  // canceled, and failures nothing can be done about.
  const finished = (job: ImportJobRecord) => job.state === "done" || job.state === "canceled" || dismissable(job);
  $: shownJobs = jobs.filter((job) => !(finished(job) && cleared.has(job.id)));
  $: clearableCount = jobs.filter((job) => finished(job) && !cleared.has(job.id)).length;
  // Before anything is dropped or imported, the sheet explains itself and the
  // drop zone is large; after, both step back for the lists.
  $: compact = pending.length > 0 || shownJobs.length > 0;

  function clearFinished() {
    for (const job of jobs) {
      if (finished(job)) cleared.add(job.id);
    }
    cleared = cleared;
    persistSet(CLEARED_KEY, cleared);
  }

  async function cancelJob(job: ImportJobRecord) {
    if (!dataProvider.cancelImport) return;
    try {
      await dataProvider.cancelImport(job.id);
      submitError = "";
    } catch (error) {
      submitError = error instanceof Error ? error.message : String(error);
    }
    await refreshJobs();
  }

  async function retryJob(job: ImportJobRecord) {
    if (!dataProvider.retryImport) return;
    try {
      await dataProvider.retryImport(job.id);
      submitError = "";
      cleared.delete(job.id);
      persistSet(CLEARED_KEY, cleared);
    } catch (error) {
      submitError = error instanceof Error ? error.message : String(error);
    }
    await refreshJobs();
  }

  async function removeJob(job: ImportJobRecord) {
    if (!dataProvider.removeImport) return;
    try { await dataProvider.removeImport(job.id); submitError = ""; await refreshJobs(); }
    catch (error) { submitError = error instanceof Error ? error.message : String(error); }
  }

  const isCLI = (job: ImportJobRecord) => job.origin === "cli" || job.stagedPath.startsWith("local:");
  const canRetry = (job: ImportJobRecord) =>
    !isCLI(job) && (job.state === "failed" || job.state === "canceled" || job.state === "staged") && job.stagingState !== "removed";
  const canRemove = (job: ImportJobRecord) =>
    !!dataProvider.removeImport &&
    (job.state === "upload_failed" || job.state === "failed" || job.state === "canceled" || job.state === "staged") &&
    job.stagedPath.startsWith(".CassiniImports/");
  // A settled row with nothing to do on it (a server-side import, or one whose
  // upload was swept) can only be dismissed, which hides it in this browser.
  const dismissable = (job: ImportJobRecord) =>
    importJobTerminal(job.state) && job.state !== "done" && !canRetry(job) && !canRemove(job);

  // Rows whose Details are open.
  let detailsOpen = new Set<string>();
  function toggleDetails(id: string) {
    if (detailsOpen.has(id)) detailsOpen.delete(id);
    else detailsOpen.add(id);
    detailsOpen = detailsOpen;
  }

  function dismiss(job: ImportJobRecord) {
    cleared.add(job.id);
    cleared = cleared;
    persistSet(CLEARED_KEY, cleared);
  }

  // What a running row says. The operator's stages after the upload are one
  // step to the person importing: their meeting is being added.
  const RUNNING_LABELS: Partial<Record<ImportJobRecord["state"], string>> = {
    uploading: "Uploading…",
    queued: "Adding…",
    fetching: "Adding…",
    validating: "Adding…",
    delivering: "Adding…",
    sharing: "Adding…",
    indexing: "Adding…",
  };

  // One plain line per settled row. The operator's own error text is for
  // diagnosis, so it sits behind Details rather than in the row.
  function settledLine(job: ImportJobRecord): string {
    switch (job.state) {
      case "upload_failed":
        return "Upload didn't finish";
      case "failed":
        return isCLI(job) ? "Import on the server didn't finish" : "Couldn't import this file";
      case "staged":
        return "Uploaded, not started";
      case "canceled":
        return "Canceled";
      default:
        return "Imported";
    }
  }

  function stagingPercent(item: PendingItem): number {
    if (item.file.size <= 0) return 0;
    return Math.min(100, Math.round((item.sentBytes / item.file.size) * 100));
  }

  function previewLine(preview: ImportPreview): string {
    const parts = [
      preview.hasAudio ? "" : "Transcript only",
      formatImportDate(preview.dateLabel),
      formatImportDuration(preview.durationMs),
    ];
    if (preview.speakerCount > 0) parts.push(`${preview.speakerCount} speaker${preview.speakerCount === 1 ? "" : "s"}`);
    if (preview.tagCount > 0) parts.push(`${preview.tagCount} tag${preview.tagCount === 1 ? "" : "s"}`);
    if (preview.hasSummary) parts.push("summary");
    return parts.filter(Boolean).join(" · ");
  }

  const canSearchSharees = typeof dataProvider.searchImportSharees === "function";
  const searchSharees = (query: string) =>
    dataProvider.searchImportSharees ? dataProvider.searchImportSharees(query) : Promise.resolve([]);
</script>

<div class="import-panel">
  <header class="import-head">
    <h2 class="text-[17px] font-semibold">Import meetings</h2>
    <button type="button" class="btn btn-ghost btn-square btn-sm" aria-label="Close import" on:click={() => dispatch("close")}>
      <X size={18} aria-hidden="true" />
    </button>
  </header>

  {#if !compact}
    <p class="import-intro">
      Add meetings downloaded from Cassini, here or on another Nextcloud: the recording (.opus), or just its
      transcript (.json).
    </p>
  {/if}

  <div
    class="import-drop"
    class:import-drop-compact={compact}
    class:drag-over={dragOver}
    role="region"
    aria-label="Drop files to import"
    on:dragover|preventDefault={() => (dragOver = true)}
    on:dragleave={() => (dragOver = false)}
    on:drop={handleDrop}
  >
    <CloudUpload size={compact ? 20 : 32} aria-hidden="true" />
    <span class="import-drop-label">{compact ? "Drop more files" : "Drop files here"}</span>
    <button type="button" class="btn btn-sm" on:click={() => fileInput.click()}>Choose files</button>
    <input
      bind:this={fileInput}
      id="import-file-input"
      type="file"
      multiple
      accept=".opus,.ogg,.oga,.ogx,.json,audio/ogg,application/ogg,application/json"
      class="hidden"
      on:change={(event) => {
        acceptFiles(event.currentTarget.files);
        event.currentTarget.value = "";
      }}
    />
  </div>

  {#if !compact}
    <p class="import-note">
      Speaker names, tags and the summary come with the file. You choose who can see each meeting before it is added.
    </p>
  {/if}

  <div class="import-body">
    {#if pending.length > 0}
      {#if readyCount > 1 && canSearchSharees}
        <div class="import-bulk">
          <span>Share all with</span>
          <ShareePicker search={searchSharees} wide on:pick={(event) => addShareeToAll(event.detail)} placeholder="A person, group or team" />
        </div>
      {/if}

      <ul class="import-list" aria-label="Files to import">
        {#each pending as item (item.key)}
          {@const bad = item.status === "invalid" || item.status === "failed"}
          {@const expanded = expandedKey === item.key && item.status === "ready"}
          <li class="import-item" class:import-item-error={bad}>
            <div class="import-row">
              {#if bad}
                <CircleAlert size={16} class="text-error flex-none" aria-hidden="true" />
              {:else if item.preview?.format === "json"}
                <FileText size={16} class="opacity-60 flex-none" aria-hidden="true" />
              {:else}
                <FileAudio size={16} class="opacity-60 flex-none" aria-hidden="true" />
              {/if}
              <div class="import-text">
                <span class="import-title">{item.preview?.title ?? item.file.name}</span>
                {#if item.status === "invalid"}
                  <!-- The parser's sentence is precise but technical; it stays
                       on hover for whoever needs it. -->
                  <span class="import-meta import-meta-error" title={item.reason}>Not a meeting file from Cassini, so it was skipped.</span>
                {:else if bad}
                  <span class="import-meta import-meta-error">{item.reason}</span>
                {:else if item.status === "parsing"}
                  <span class="import-meta">Reading…</span>
                {:else if item.status === "staging"}
                  <span class="import-meta">Uploading {stagingPercent(item)}%</span>
                  <progress class="progress progress-primary import-bar" value={stagingPercent(item)} max="100"></progress>
                {:else if item.preview}
                  <span class="import-meta">{previewLine(item.preview)}</span>
                {/if}
              </div>
              {#if item.status === "ready"}
                <button
                  type="button"
                  class="import-pill"
                  class:import-pill-on={item.audience.length > 0}
                  aria-expanded={expanded}
                  on:click={() => (expandedKey = expanded ? "" : item.key)}
                >
                  {#if item.audience.length > 0}
                    <Users size={12} aria-hidden="true" /> Shared
                  {:else}
                    <Lock size={12} aria-hidden="true" /> Only me
                  {/if}
                  {#if expanded}<ChevronUp size={12} aria-hidden="true" />{:else}<ChevronDown size={12} aria-hidden="true" />{/if}
                </button>
              {/if}
              {#if item.status !== "staging"}
                <button type="button" class="btn btn-ghost btn-square btn-xs" aria-label={`Remove ${item.file.name}`} on:click={() => removePending(item.key)}>
                  <X size={14} aria-hidden="true" />
                </button>
              {/if}
            </div>

            {#if expanded}
              <div class="import-share">
                {#if canSearchSharees}
                  <ShareePicker search={searchSharees} wide on:pick={(event) => addSharee(item.key, event.detail)} placeholder="Add a person, group or team" />
                {/if}
                {#each SHAREE_GROUPS as group (group.type)}
                  {@const members = item.audience.filter((sharee) => sharee.type === group.type)}
                  {#if group.type === "user" || members.length > 0}
                    <div class="import-share-heading">{group.heading}</div>
                    {#if group.type === "user"}
                      <div class="import-share-row"><span>You</span><span class="opacity-60 text-xs">importer</span></div>
                    {/if}
                    {#each members as sharee (sharee.type + sharee.id)}
                      <div class="import-share-row">
                        <span class="truncate">{sharee.label}</span>
                        <button type="button" class="btn btn-ghost btn-square btn-xs" aria-label={`Remove ${sharee.label}`} on:click={() => removeSharee(item.key, sharee)}>
                          <X size={12} aria-hidden="true" />
                        </button>
                      </div>
                    {/each}
                  {/if}
                {/each}
              </div>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}

    {#if submitError}
      <p role="alert" class="alert alert-warning alert-soft py-2 text-sm">{submitError}</p>
    {/if}
    {#if jobsError}
      <p role="alert" class="alert alert-warning alert-soft py-2 text-sm">{jobsError}</p>
    {/if}

    {#if jobsLoaded && shownJobs.length > 0}
      <section aria-label="Recent imports" class="import-recent">
        <div class="import-recent-head">
          <h3>Recent imports</h3>
          {#if clearableCount > 0}
            <button type="button" class="import-link import-link-quiet" on:click={clearFinished}>Clear finished</button>
          {/if}
        </div>
        <ul class="import-list">
          {#each shownJobs as job (job.id)}
            {@const fromCLI = isCLI(job)}
            {@const running = !importJobTerminal(job.state)}
            {@const canOpen = job.state === "done" && !!job.contentId && openableIds.has(job.contentId)}
            <li class="import-item" class:import-item-error={isFailure(job)}>
              <div class="import-row">
                {#if isFailure(job)}
                  <CircleAlert size={15} class="text-error flex-none" aria-hidden="true" />
                {:else if job.state === "done"}
                  <Check size={15} class="text-success flex-none" aria-hidden="true" />
                {/if}
                <div class="import-text">
                  <span class="import-title">{job.sourceName}</span>
                  {#if running}
                    <span class="import-meta">{RUNNING_LABELS[job.state] ?? "Adding…"}</span>
                    <progress class="progress progress-primary import-bar"></progress>
                  {:else if isFailure(job)}
                    <span class="import-meta">
                      <span class="text-error">{settledLine(job)}</span>
                      {#if job.error}
                        ·
                        <button type="button" class="import-link import-link-quiet" aria-expanded={detailsOpen.has(job.id)} on:click={() => toggleDetails(job.id)}>Details</button>
                      {/if}
                    </span>
                  {:else}
                    <!-- A settled row says how it is (or would be) shared, and
                         who that means on hover — a retry imports with exactly this. -->
                    {@const sharing = describeImportSharing(job)}
                    <span class="import-meta">
                      {settledLine(job)}{job.format === "json" ? " · transcript only" : ""} ·
                      <span class="import-sharing" title={sharing.title}>{sharing.line}</span>
                    </span>
                  {/if}
                </div>
                <span class="import-actions">
                  {#if running && !fromCLI && job.state !== "uploading"}
                    <button type="button" class="btn btn-ghost btn-square btn-xs" aria-label={`Cancel ${job.sourceName}`} on:click={() => void cancelJob(job)}>
                      <X size={14} aria-hidden="true" />
                    </button>
                  {/if}
                  {#if canRetry(job)}
                    <button type="button" class="import-link" on:click={() => void retryJob(job)}>Retry</button>
                  {/if}
                  {#if canRemove(job)}
                    <button type="button" class="import-link import-link-quiet" title="Delete the temporary upload and remove this attempt." on:click={() => void removeJob(job)}>Remove</button>
                  {/if}
                  {#if canOpen}
                    <button type="button" class="import-link" on:click={() => dispatch("openMeeting", job.contentId ?? "")}>Open</button>
                  {/if}
                  {#if dismissable(job)}
                    <button type="button" class="import-link import-link-quiet" title="Hide this row. It stays in the server's import history." on:click={() => dismiss(job)}>Dismiss</button>
                  {/if}
                </span>
              </div>
              {#if detailsOpen.has(job.id) && job.error}
                <p class="import-details">{job.error}</p>
              {/if}
            </li>
          {/each}
        </ul>
      </section>
    {/if}
  </div>

  {#if readyCount > 0}
    <footer class="import-foot">
      <span>{readyCount} ready{skippedCount > 0 ? ` · ${skippedCount} skipped` : ""}</span>
      <button type="button" class="btn btn-primary" disabled={submitting} on:click={() => void submit()}>
        {submitting ? "Importing…" : `Import ${readyCount} meeting${readyCount === 1 ? "" : "s"}`}
      </button>
    </footer>
  {/if}
</div>

<style>
  .import-panel {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }
  .import-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 14px 12px 4px 20px;
  }
  .import-intro {
    padding: 0 20px 16px;
    font-size: 0.875rem;
    line-height: 1.5;
    color: color-mix(in oklch, var(--color-base-content) 68%, transparent);
  }
  .import-note {
    padding: 14px 20px 0;
    font-size: 0.75rem;
    line-height: 1.5;
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }
  .import-drop {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 12px;
    margin: 0 20px;
    padding: 32px 16px;
    border: 2px dashed color-mix(in oklch, var(--color-primary) 55%, transparent);
    border-radius: var(--radius-box, 12px);
    color: var(--color-primary);
    background: color-mix(in oklch, var(--color-primary) 6%, transparent);
    text-align: center;
  }
  .import-drop-compact {
    flex-direction: row;
    gap: 10px;
    margin-top: 8px;
    padding: 8px 8px 8px 14px;
    text-align: left;
  }
  .import-drop-label {
    font-weight: 600;
    color: var(--color-base-content);
  }
  .import-drop-compact .import-drop-label {
    flex: 1;
    font-weight: 500;
    font-size: 0.8125rem;
  }
  .import-drop.drag-over {
    border-color: var(--color-primary);
    background: color-mix(in oklch, var(--color-primary) 14%, transparent);
  }
  .import-body {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    overscroll-behavior: contain;
    padding: 16px 20px 24px;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .import-bulk {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 0.8125rem;
    color: color-mix(in oklch, var(--color-base-content) 68%, transparent);
  }
  .import-bulk :global(.sharee-picker) {
    flex: 1;
  }
  .import-list {
    border: 1px solid var(--color-base-300);
    border-radius: calc(var(--radius-box, 12px) - 2px);
    background: var(--color-base-200);
  }
  .import-item + .import-item {
    border-top: 1px solid var(--color-base-300);
  }
  .import-row {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 9px 8px 9px 12px;
  }
  .import-text {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .import-title {
    font-weight: 600;
    font-size: 0.84375rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .import-meta {
    font-size: 0.75rem;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .import-meta-error {
    white-space: normal;
  }
  .import-meta-error {
    color: var(--color-error);
  }
  .import-bar {
    height: 3px;
    margin-top: 5px;
  }
  .import-pill {
    flex: none;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 26px;
    padding: 0 6px 0 8px;
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-field, 0.5rem);
    background: var(--color-base-100);
    font-size: 0.75rem;
    cursor: pointer;
  }
  .import-pill-on {
    border-color: color-mix(in oklch, var(--color-primary) 50%, transparent);
    background: color-mix(in oklch, var(--color-primary) 10%, var(--color-base-100));
  }
  .import-share {
    margin: 0 8px 10px 38px;
    padding: 8px;
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-field, 0.5rem);
    background: var(--color-base-100);
  }
  .import-share-heading {
    padding: 8px 4px 2px;
    font-size: 0.6875rem;
    font-weight: 600;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }
  .import-share-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    min-height: 26px;
    padding: 0 0 0 4px;
    font-size: 0.8125rem;
  }
  .import-recent {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .import-recent-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 2px;
  }
  .import-recent-head h3 {
    font-size: 0.75rem;
    font-weight: 600;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .import-actions {
    flex: none;
    display: flex;
    align-items: center;
    gap: 12px;
    padding-right: 4px;
  }
  .import-link {
    padding: 0;
    border: 0;
    background: transparent;
    font-size: 0.75rem;
    font-weight: 600;
    color: var(--color-primary);
    cursor: pointer;
  }
  .import-link-quiet {
    font-weight: 500;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .import-details {
    margin: 0 12px 10px 37px;
    padding: 6px 8px;
    border-radius: 6px;
    background: var(--color-base-100);
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.6875rem;
    line-height: 1.5;
    overflow-wrap: anywhere;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }
  .import-sharing {
    text-decoration: underline dotted;
    text-underline-offset: 3px;
    cursor: help;
  }
  .import-foot {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 14px 20px;
    border-top: 1px solid var(--color-base-300);
    font-size: 0.75rem;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .import-foot span {
    flex: 1;
  }
</style>
