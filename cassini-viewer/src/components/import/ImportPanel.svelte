<script lang="ts">
  // The Import tab (D-828): a persistent workbench, not a dialog.
  //
  // Drop zone at the top; dropped files parse LOCALLY (viewer/imports.ts) and
  // stack below with per-meeting sharing; "Import" stages each file into the
  // caller's own Files and hands the operator only the staged paths. The jobs
  // list below shows the durable server-side imports — it survives navigation
  // and reload, and "Clear completed" is a display action only (the rows are
  // retention provenance and never leave the operator's database).
  import { createEventDispatcher, onDestroy, onMount } from "svelte";
  import { CloudUpload, FileAudio, FileJson, RotateCcw, X } from "@lucide/svelte";

  import type { DataProvider } from "../../viewer/dataProvider";
  import {
    formatImportDuration,
    importJobTerminal,
    parseImportPreview,
    type ImportFileRequest,
    type ImportJobRecord,
    type ImportPreview,
    type ImportSharee,
  } from "../../viewer/imports";
  import ShareePicker from "./ShareePicker.svelte";

  export let dataProvider: DataProvider;
  // Whether this tab is the visible one. The panel stays mounted either way so
  // the jobs poll keeps the tab dot honest before the tab is first opened.
  export let active = false;

  const dispatch = createEventDispatcher<{ activity: boolean; imported: void }>();

  type PendingStatus = "parsing" | "ready" | "invalid" | "staging" | "failed";
  interface PendingItem {
    key: string;
    file: File;
    status: PendingStatus;
    reason?: string;
    preview?: ImportPreview;
    share: boolean;
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
  let bulkShare: "none" | "people" = "none";
  let fileInput: HTMLInputElement;
  let keySequence = 0;

  // "Clear completed" hides done/canceled rows in THIS browser only. A plain
  // per-viewer convenience: absent or blocked storage simply shows them again.
  const CLEARED_KEY = "cassini.imports.cleared.v1";
  let cleared: Set<string> = readCleared();

  function readCleared(): Set<string> {
    try {
      const raw = localStorage.getItem(CLEARED_KEY);
      return new Set(raw ? (JSON.parse(raw) as string[]) : []);
    } catch {
      return new Set();
    }
  }

  function persistCleared() {
    try {
      localStorage.setItem(CLEARED_KEY, JSON.stringify([...cleared]));
    } catch {
      // A private window or blocked storage: clearing lasts for this page only.
    }
  }

  // --- the jobs poll -------------------------------------------------------

  let pollTimer: ReturnType<typeof setInterval> | null = null;
  let previousStates = new Map<string, string>();

  async function refreshJobs() {
    if (!dataProvider.listImports) return;
    try {
      const listed = await dataProvider.listImports();
      for (const job of listed) {
        const before = previousStates.get(job.id);
        if (before && before !== "done" && job.state === "done") {
          // A meeting just landed: the Meetings tab should not wait a minute
          // for its poll to notice.
          dispatch("imported");
        }
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

  $: activeJobs = jobs.filter((job) => !importJobTerminal(job.state));
  $: dispatch("activity", activeJobs.length > 0 || submitting || pending.some((item) => item.status === "staging"));

  function syncPolling() {
    const wanted = activeJobs.length > 0 || submitting;
    if (wanted && pollTimer === null) {
      pollTimer = setInterval(() => void refreshJobs(), 2500);
    } else if (!wanted && pollTimer !== null) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  $: if (active) {
    // Re-entering the tab refreshes once; the interval only runs while work is live.
    void refreshJobs();
  }

  onMount(() => void refreshJobs());
  onDestroy(() => {
    if (pollTimer !== null) clearInterval(pollTimer);
  });

  // --- drop & parse --------------------------------------------------------

  function acceptFiles(files: FileList | File[] | null) {
    if (!files) return;
    for (const file of Array.from(files)) {
      const item: PendingItem = {
        key: `pf_${++keySequence}`,
        file,
        status: "parsing",
        share: bulkShare === "people",
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
  }

  function handleDrop(event: DragEvent) {
    event.preventDefault();
    dragOver = false;
    acceptFiles(event.dataTransfer?.files ?? null);
  }

  // --- sharing selection ----------------------------------------------------

  function setShare(key: string, share: boolean) {
    patch(key, { share });
  }

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

  // The bulk control seeds every row; rows stay individually editable after.
  function applyBulkShare(mode: "none" | "people") {
    bulkShare = mode;
    pending = pending.map((item) => ({ ...item, share: mode === "people" }));
  }

  function addBulkSharee(sharee: ImportSharee) {
    pending = pending.map((item) =>
      item.share && !item.audience.some((have) => have.type === sharee.type && have.id === sharee.id)
        ? { ...item, audience: [...item.audience, sharee] }
        : item,
    );
  }

  // --- submit ---------------------------------------------------------------

  $: readyCount = pending.filter((item) => item.status === "ready").length;

  async function submit() {
    if (submitting || readyCount === 0) return;
    if (!dataProvider.stageImportUpload || !dataProvider.startImports) return;
    submitting = true;
    syncPolling();
    submitError = "";
    try {
      for (const item of pending.filter((candidate) => candidate.status === "ready")) {
        patch(item.key, { status: "staging", sentBytes: 0 });
        try {
          const sharing = { share: item.share, audience: item.audience.map(({ type, id }) => ({ type, id })) };
          const stagedPath = await dataProvider.stageImportUpload(item.file, (sent) => {
            patch(item.key, { sentBytes: sent });
          }, sharing);
          // This upload is already durable. Queue it independently so a large
          // selection cannot upload fully and then hit the 50-file batch cap.
          removePending(item.key);
          await dataProvider.startImports([{
            stagedPath,
            sourceName: item.file.name,
            share: item.share,
            audience: item.audience.map(({ type, id }) => ({ type, id })),
          }]);
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

  // --- the jobs list --------------------------------------------------------

  $: shownJobs = jobs.filter((job) => !((job.state === "done" || job.state === "canceled") && cleared.has(job.id)));
  $: clearableCount = jobs.filter((job) => (job.state === "done" || job.state === "canceled") && !cleared.has(job.id)).length;

  function clearCompleted() {
    for (const job of jobs) {
      if (job.state === "done" || job.state === "canceled") cleared.add(job.id);
    }
    cleared = cleared;
    persistCleared();
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
      persistCleared();
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

  async function clearStaging() {
    if (!dataProvider.clearImportStaging) return;
    if (!window.confirm("Delete inactive temporary uploads? Failed attempts will disappear from this list. Imported meetings and active work will be kept.")) return;
    try { await dataProvider.clearImportStaging(); submitError = ""; await refreshJobs(); }
    catch (error) { submitError = error instanceof Error ? error.message : String(error); }
  }

  const JOB_LABELS: Record<string, string> = {
    uploading: "uploading",
    upload_failed: "upload incomplete",
    staged: "uploaded — ready to import",
    queued: "queued",
    fetching: "fetching upload",
    validating: "validating",
    delivering: "delivering",
    sharing: "sharing",
    indexing: "indexing",
    done: "imported",
    failed: "failed",
    canceled: "canceled",
  };

  function stagingPercent(item: PendingItem): number {
    if (item.file.size <= 0) return 0;
    return Math.min(100, Math.round((item.sentBytes / item.file.size) * 100));
  }

  function previewLine(preview: ImportPreview): string {
    const parts = [preview.dateLabel, formatImportDuration(preview.durationMs)];
    if (preview.speakerCount > 0) parts.push(`${preview.speakerCount} speaker${preview.speakerCount === 1 ? "" : "s"}`);
    if (preview.tagCount > 0) parts.push(`${preview.tagCount} tag${preview.tagCount === 1 ? "" : "s"}`);
    if (preview.hasSummary) parts.push("summary ✓");
    return parts.filter(Boolean).join(" · ");
  }

  const canSearchSharees = typeof dataProvider.searchImportSharees === "function";
  const searchSharees = (query: string) =>
    dataProvider.searchImportSharees ? dataProvider.searchImportSharees(query) : Promise.resolve([]);
</script>

<div class="import-pane" class:import-pane-hidden={!active}>
  <div class="import-column">
    <header class="pt-4 pb-1">
      <h2 class="text-[17px] font-semibold">Import meetings</h2>
      <p class="text-sm text-base-content/65">
        Drop portable meeting files to add them to this archive. A file carries its own transcript,
        speakers and tags — including speaker names and who marked what on the instance it came from.
      </p>
    </header>

    <div
      class="import-drop"
      class:drag-over={dragOver}
      role="button"
      tabindex="0"
      aria-label="Drop portable meeting files here, or press Enter to choose files"
      on:click={() => fileInput.click()}
      on:keydown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          fileInput.click();
        }
      }}
      on:dragover|preventDefault={() => (dragOver = true)}
      on:dragleave={() => (dragOver = false)}
      on:drop={handleDrop}
    >
      <CloudUpload size={28} aria-hidden="true" />
      <span class="font-semibold">Drop portable meeting files here</span>
      <span class="text-sm opacity-75">.opus or transcription-only .json — or click to choose</span>
      <input
        bind:this={fileInput}
        id="import-file-input"
        type="file"
        multiple
        accept=".opus,.json,audio/ogg,application/json"
        class="hidden"
        on:change={(event) => {
          acceptFiles(event.currentTarget.files);
          event.currentTarget.value = "";
        }}
      />
    </div>

    {#if pending.length > 1 && readyCount > 0}
      <div class="import-bulk" role="group" aria-label="Apply sharing to every meeting">
        <span class="text-sm font-medium">Apply to all:</span>
        <div class="join">
          <button
            type="button"
            class="btn btn-sm join-item"
            class:btn-active={bulkShare === "none"}
            on:click={() => applyBulkShare("none")}>Don't share</button
          >
          <button
            type="button"
            class="btn btn-sm join-item"
            class:btn-active={bulkShare === "people"}
            on:click={() => applyBulkShare("people")}>Share with people</button
          >
        </div>
        {#if bulkShare === "people" && canSearchSharees}
          <ShareePicker search={searchSharees} on:pick={(event) => addBulkSharee(event.detail)} placeholder="Add someone to every meeting…" />
        {/if}
      </div>
    {/if}

    {#each pending as item (item.key)}
      <div class="import-item" class:import-item-error={item.status === "invalid" || item.status === "failed"}>
        <div class="flex items-baseline justify-between gap-2 flex-wrap">
          <span class="flex items-center gap-1.5 font-semibold min-w-0">
            {#if item.preview?.format === "json"}
              <FileJson size={15} aria-hidden="true" />
            {:else}
              <FileAudio size={15} aria-hidden="true" />
            {/if}
            <span class="truncate">{item.preview?.title ?? item.file.name}</span>
          </span>
          <span class="flex items-center gap-1.5">
            {#if item.status === "parsing"}
              <span class="badge badge-sm badge-ghost">reading…</span>
            {:else if item.status === "ready"}
              {#if item.preview && !item.preview.hasAudio}
                <span class="badge badge-sm badge-info badge-soft">transcription only</span>
              {/if}
              <span class="badge badge-sm badge-success badge-soft">valid</span>
            {:else if item.status === "staging"}
              <span class="badge badge-sm badge-info badge-soft">uploading {stagingPercent(item)}%</span>
            {:else if item.status === "invalid"}
              <span class="badge badge-sm badge-error badge-soft">skipped</span>
            {:else if item.status === "failed"}
              <span class="badge badge-sm badge-error badge-soft">upload failed</span>
            {/if}
            {#if item.status !== "staging"}
              <button type="button" class="btn btn-ghost btn-square btn-xs" aria-label={`Remove ${item.file.name}`} on:click={() => removePending(item.key)}>
                <X size={14} aria-hidden="true" />
              </button>
            {/if}
          </span>
        </div>

        {#if item.status === "invalid" || item.status === "failed"}
          <p class="text-sm text-error mt-0.5">{item.reason}</p>
        {:else if item.preview}
          <p class="text-sm text-base-content/65 mt-0.5">{previewLine(item.preview)}</p>
        {/if}

        {#if item.status === "staging"}
          <progress class="progress progress-primary w-full mt-1" value={stagingPercent(item)} max="100"></progress>
        {/if}

        {#if item.status === "ready"}
          <div class="mt-2 flex flex-wrap items-center gap-2">
            <div class="join">
              <button
                type="button"
                class="btn btn-sm join-item"
                class:btn-active={!item.share}
                on:click={() => setShare(item.key, false)}>Don't share</button
              >
              <button
                type="button"
                class="btn btn-sm join-item"
                class:btn-active={item.share}
                on:click={() => setShare(item.key, true)}>Share with people</button
              >
            </div>
            {#if !item.share}
              <span class="text-xs text-base-content/60">Visible only to you.</span>
            {/if}
          </div>
          {#if item.share}
            <div class="mt-1.5 flex flex-wrap items-center gap-1.5">
              <span class="badge badge-sm badge-outline" title="You import this meeting, so you always have it">you</span>
              {#each item.audience as sharee (sharee.type + sharee.id)}
                <span class="badge badge-sm badge-outline gap-1">
                  {sharee.label}
                  <button type="button" class="cursor-pointer" aria-label={`Remove ${sharee.label}`} on:click={() => removeSharee(item.key, sharee)}>
                    <X size={11} aria-hidden="true" />
                  </button>
                </span>
              {/each}
              {#if canSearchSharees}
                <ShareePicker search={searchSharees} on:pick={(event) => addSharee(item.key, event.detail)} />
              {/if}
            </div>
            <p class="text-xs text-base-content/60 mt-1">
              People you share with can open the meeting and pass it along.
            </p>
          {/if}
        {/if}
      </div>
    {/each}

    {#if readyCount > 0}
      <div class="flex justify-end py-2">
        <button type="button" class="btn btn-primary btn-sm" disabled={submitting} on:click={() => void submit()}>
          {submitting ? "Importing…" : `Import ${readyCount} meeting${readyCount === 1 ? "" : "s"}`}
        </button>
      </div>
    {/if}

    <section class="pt-4" aria-label="Import jobs">
      <div class="flex items-center justify-between gap-2">
        <h3 class="text-[15px] font-semibold">Imports</h3>
        {#if dataProvider.clearImportStaging}
          <button type="button" class="btn btn-ghost btn-xs" disabled={submitting} on:click={clearStaging}>Clear staged files</button>
        {/if}
        {#if clearableCount > 0}
          <button type="button" class="btn btn-ghost btn-xs" on:click={clearCompleted}>Clear completed</button>
        {/if}
      </div>
      {#if submitError}
        <p role="alert" class="alert alert-warning alert-soft my-1.5 py-2 text-sm">{submitError}</p>
      {/if}
      <p class="text-xs text-base-content/60">Inactive temporary files are cleared daily at 03:03 UTC. Cleared unsuccessful attempts disappear from this list.</p>
      {#if jobsError}
        <p role="alert" class="alert alert-warning alert-soft my-1.5 py-2 text-sm">{jobsError}</p>
      {/if}
      {#if jobsLoaded && shownJobs.length === 0}
        <p class="text-sm text-base-content/60 py-2">Nothing imported from this account yet.</p>
      {/if}
      {#each shownJobs as job (job.id)}
        <div class="import-item" class:import-item-error={job.state === "failed"}>
          <div class="flex items-baseline justify-between gap-2 flex-wrap">
            <span class="font-semibold truncate">{job.sourceName}</span>
            <span class="flex items-center gap-1.5">
              <span
                class="badge badge-sm badge-soft"
                class:badge-success={job.state === "done"}
                class:badge-error={job.state === "failed"}
                class:badge-info={!importJobTerminal(job.state)}
              >{JOB_LABELS[job.state] ?? job.state}</span>
              {#if !importJobTerminal(job.state) && job.state !== "uploading"}
                <button type="button" class="btn btn-ghost btn-xs" on:click={() => void cancelJob(job)}>Cancel</button>
              {:else if (job.state === "failed" || job.state === "canceled" || job.state === "staged") && job.stagingState !== "removed"}
                <button type="button" class="btn btn-ghost btn-xs gap-1" on:click={() => void retryJob(job)}>
                  <RotateCcw size={13} aria-hidden="true" /> Retry
                </button>
              {/if}
              {#if dataProvider.removeImport && (job.state === "upload_failed" || job.state === "failed" || job.state === "canceled" || job.state === "staged") && job.stagedPath.startsWith(".CassiniImports/")}
                <button type="button" class="btn btn-ghost btn-xs" title="Delete the temporary upload and remove this attempt" on:click={() => void removeJob(job)}>Remove</button>
              {/if}
            </span>
          </div>
          {#if (job.state === "failed" || job.state === "upload_failed") && job.error}
            <p class="text-sm text-error mt-0.5">{job.error}</p>
          {:else if job.state === "done"}
            <p class="text-sm text-base-content/65 mt-0.5">
              {job.sharing.share ? `Shared with ${(job.sharing.audience ?? []).length + 1}` : "Visible only to you"}
              {job.format === "json" ? " · transcription only" : ""}
            </p>
          {:else if !importJobTerminal(job.state)}
            <progress class="progress progress-primary w-full mt-1"></progress>
          {/if}
        </div>
      {/each}
    </section>
  </div>
</div>

<style>
  .import-pane {
    grid-column: 1 / -1;
    grid-row: 2;
    min-height: 0;
    overflow-y: auto;
    overscroll-behavior: contain;
  }
  .import-pane-hidden {
    display: none;
  }
  .import-column {
    max-width: 680px;
    margin: 0 auto;
    padding: 0 16px 48px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .import-drop {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 4px;
    padding: 28px 16px;
    border: 2px dashed var(--color-primary);
    border-radius: var(--radius-box, 12px);
    color: var(--color-primary);
    background: color-mix(in oklab, var(--color-primary) 6%, transparent);
    cursor: pointer;
    text-align: center;
  }
  .import-drop.drag-over,
  .import-drop:focus-visible {
    background: color-mix(in oklab, var(--color-primary) 14%, transparent);
    outline: 2px solid var(--color-primary);
    outline-offset: 2px;
  }
  .import-bulk {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-box, 12px);
    background: var(--color-base-200);
  }
  .import-item {
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-box, 12px);
    background: var(--color-base-100);
    padding: 10px 12px;
  }
  .import-item-error {
    border-color: var(--color-error);
  }
</style>
