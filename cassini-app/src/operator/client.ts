import type { RecordingReadiness, RecordingSetupUpdate } from "./readiness";
import type {
  InsightWorkflow,
  Job,
  JobAttempt,
  JobDetailResponse,
  LLMEffectiveStep,
  LLMModel,
  LLMProviderView,
  LLMSettings,
  LLMSettingsUpdate,
  LLMStep,
  Settings,
  SpeechModelInventory,
  SpeechModelJob,
  SettingsEffective,
  SettingsQuality,
  SettingsUpdate,
  AppInstallOutcome,
  OpenRecording,
  OpenRecordingPrincipal,
  OpenRecordingReason,
  OpenRecordings,
  RestrictOutcome,
  RestrictResult,
  StorageArchiveFacts,
  StorageMigration,
  StorageMode,
  StorageModeOption,
  StorageServiceAccount,
  StorageSetupStep,
  StorageStatus,
  StorageTransition,
  StorageTransitionPreview,
} from "./types";

const SETTINGS_QUALITIES: readonly SettingsQuality[] = ["fast", "balanced", "best"];

export interface OperatorStateChangeEvent {
  type: string;
  job_id: string;
  attempt_number?: number;
  at: string;
  job: Job;
  attempt?: JobAttempt;
}

interface OperatorStreamHandlers {
  onOpen?: () => void;
  onError?: () => void;
  onStateChange: (event: OperatorStateChangeEvent) => void;
}

interface CreateJobResponse {
  id: string;
}

interface StopJobResponse {
  id: string;
}

interface RerunJobResponse {
  id: string;
  attempt_number: number;
}

export class OperatorHttpError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "OperatorHttpError";
    this.status = status;
  }
}

export class OperatorClient {
  #baseUrl: string;

  constructor(baseUrl: string) {
    this.#baseUrl = baseUrl.replace(/\/+$/, "");
  }

  async getReadiness(): Promise<RecordingReadiness> {
    return this.#request<RecordingReadiness>("/health");
  }

  async checkReadiness(): Promise<RecordingReadiness> {
    return this.#request<RecordingReadiness>("/health/check", { method: "POST" });
  }

  // Starts a repair the operator performs itself and returns the checklist as
  // it stands. The work outlives the request, so the row reports that it is
  // running and the next read says how it went.
  async repairReadiness(action: string): Promise<RecordingReadiness> {
    return this.#request<RecordingReadiness>("/health/repair", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ action }),
    });
  }

  async updateRecordingSetup(payload: RecordingSetupUpdate): Promise<RecordingReadiness> {
    return this.#request<RecordingReadiness>("/talk/setup", {
      method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload),
    });
  }

  async listJobs(): Promise<Job[]> {
    return this.#request<Job[]>("/jobs");
  }

  async getJobDetail(jobId: string): Promise<JobDetailResponse> {
    return this.#request<JobDetailResponse>(`/jobs/${encodeURIComponent(jobId)}`);
  }

  async startJob(url: string): Promise<CreateJobResponse> {
    return this.#request<CreateJobResponse>("/jobs?provider=nextcloud-talk", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        platform: "nextcloud-talk",
        url,
      }),
    });
  }

  async stopJob(jobId: string): Promise<StopJobResponse> {
    return this.#request<StopJobResponse>(`/jobs/${encodeURIComponent(jobId)}/stop`, {
      method: "POST",
    });
  }

  async rerunJob(jobId: string): Promise<RerunJobResponse> {
    return this.#request<RerunJobResponse>(`/jobs/${encodeURIComponent(jobId)}/rerun`, {
      method: "POST",
    });
  }

  async getSpeechModels(device: string): Promise<SpeechModelInventory> {
    return this.#request<SpeechModelInventory>(`/settings/models?device=${encodeURIComponent(device)}`);
  }
  async installSpeechModel(model: string, revision: string, device: string): Promise<SpeechModelJob> {
    return this.#request<SpeechModelJob>("/settings/models/install", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({model, revision, device}) });
  }
  async speechModelJobAction(id: string, action: "cancel" | "retry"): Promise<unknown> {
    return this.#request(`/settings/models/jobs/${encodeURIComponent(id)}/${action}`, { method: "POST" });
  }

  async getSettings(): Promise<Settings> {
    return normalizeSettings(await this.#request<unknown>("/settings"));
  }

  async putSettings(payload: SettingsUpdate): Promise<Settings> {
    return normalizeSettings(
      await this.#request<unknown>("/settings", {
        method: "PUT",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify(payload),
      }),
    );
  }

  async getLLMSettings(): Promise<LLMSettings> {
    return normalizeLLMSettings(await this.#request<unknown>("/settings/llm"));
  }

  async putLLMSettings(payload: LLMSettingsUpdate): Promise<LLMSettings> {
    return normalizeLLMSettings(
      await this.#request<unknown>("/settings/llm", {
        method: "PUT",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify(payload),
      }),
    );
  }

  async listProviderModels(providerId: string): Promise<LLMModel[]> {
    const raw = await this.#request<{ models?: unknown }>(
      `/settings/llm/providers/${encodeURIComponent(providerId)}/models`,
    );
    if (!Array.isArray(raw.models)) {
      return [];
    }
    return raw.models
      .filter((item): item is Record<string, unknown> => item != null && typeof item === "object")
      .filter((item) => typeof item.id === "string" && item.id !== "")
      .map((item) => ({
        id: item.id as string,
        name: typeof item.name === "string" ? item.name : undefined,
        context_length:
          typeof item.context_length === "number" && Number.isFinite(item.context_length)
            ? item.context_length
            : undefined,
      }));
  }

  async getStorage(): Promise<StorageStatus> {
    return normalizeStorage(await this.#request<unknown>("/storage"));
  }

  // putStorage switches the storage model, which MOVES every published
  // recording. It is one call and it blocks for the length of the move: the
  // operator holds its provisioning lock for the whole transition and re-runs
  // its preflight before answering.
  //
  // Its answer is the authoritative status. Progress, while it is out, is read
  // by a SECOND reader calling getStorage() — `migration` on that response is
  // the move as the operator sees it (D-755) — because this promise says
  // nothing until the whole move is done.
  async putStorage(accessControlEnabled: boolean, confirmOverwrite = false): Promise<StorageStatus> {
    return normalizeStorage(
      await this.#request<unknown>("/storage", {
        method: "PUT",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          access_control_enabled: accessControlEnabled,
          ...(confirmOverwrite ? { confirm_overwrite: true } : {}),
        }),
      }),
    );
  }

  // recheckStorage makes the operator look at Nextcloud again.
  //
  // The setup writes happen in the browser (D-671), so the operator cannot see
  // them until it re-probes — without this the settings section would go on
  // reporting what was missing before the administrator fixed it. It is also
  // what a plan is RECOMPUTED from: the operator cannot see a Team folder until
  // `groupfolders` is enabled, so a plan built before the apps went in is stale
  // about everything after them.
  async recheckStorage(): Promise<StorageStatus> {
    return normalizeStorage(
      await this.#request<unknown>("/storage", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ action: "recheck" }),
      }),
    );
  }

  // previewStorageSwitch asks what a mode switch would do, without doing it.
  //
  // The transition relocates an entire published archive and, going into the
  // Team folder, makes every already-published recording readable by every
  // account. The preview names the source, the destination artefacts that need
  // confirmation before replacement, and any cleanup left by a prior run.
  //
  // Read-only: the operator issues PROPFINDs and nothing else.
  async previewStorageSwitch(accessControlEnabled: boolean): Promise<StorageStatus> {
    return normalizeStorage(
      await this.#request<unknown>("/storage", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          action: "preview",
          access_control_enabled: accessControlEnabled,
        }),
      }),
    );
  }

  // finishStorageMigration completes a switch that stopped part way.
  //
  // It clears the root the recorded mode does NOT name and marks the instance
  // settled. One action covers every way a migration can stop, because the
  // operator's invariant makes them the same shape: whatever happened, the
  // recorded mode names a root holding a complete archive and the other one
  // holds something nothing reads.
  async finishStorageMigration(): Promise<StorageStatus> {
    return normalizeStorage(
      await this.#request<unknown>("/storage", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ action: "finish_migration" }),
      }),
    );
  }

  // installStorageApps asks the operator to attempt the native app installs.
  //
  // This is the one part of the setup the browser cannot do — those routes want
  // the password on the request itself — and the operator can, on releases that
  // predate Nextcloud's password-confirmation hardening or where an
  // administrator has set a bypass range. It reports per-app what happened so
  // the UI can hand off the ones it could not do.
  async installStorageApps(): Promise<StorageStatus> {
    return normalizeStorage(
      await this.#request<unknown>("/storage", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ action: "install_apps" }),
      }),
    );
  }

  // acknowledgeFirstRun records that an administrator has seen the first-run
  // dialog. It is kept in the operator's settings store, per install, so the
  // dialog is shown once for this Nextcloud rather than once per browser.
  //
  // On the existing POST /storage, like every other action here: AppAPI learns
  // an ExApp's routes when it is REGISTERED, so a new route would 404 on every
  // installation that updated in place.
  async acknowledgeFirstRun(): Promise<StorageStatus> {
    return normalizeStorage(
      await this.#request<unknown>("/storage", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ action: "acknowledge_first_run" }),
      }),
    );
  }

  // listOpenRecordings asks which recordings are readable by every account and
  // which of those could be limited to the people who were in the call (D-769).
  //
  // A POST because it PROBES — the operator reads the Team folder's permissions
  // to answer it — and GET /storage is a page an administrator may refresh, so
  // it never probes anything.
  async listOpenRecordings(): Promise<StorageStatus> {
    return normalizeStorage(
      await this.#request<unknown>("/storage", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ action: "list_unrestricted" }),
      }),
    );
  }

  // restrictRecordings limits each named recording to the audience captured
  // when it was recorded.
  //
  // The digest, not the audience: what goes back is a fingerprint of what this
  // page displayed, so the operator can refuse a row whose audience has changed
  // since — and so the browser never gets to say who may read a recording.
  async restrictRecordings(meetings: { id: string; audience_digest: string }[]): Promise<StorageStatus> {
    return normalizeStorage(
      await this.#request<unknown>("/storage", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ action: "restrict_meetings", meetings }),
      }),
    );
  }

  // ignoreRecordings dismisses recordings from that list, or restores them.
  // Nothing in Nextcloud changes: it is a note that somebody has looked.
  async ignoreRecordings(ids: string[], ignored: boolean): Promise<StorageStatus> {
    return normalizeStorage(
      await this.#request<unknown>("/storage", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ action: "ignore_recordings", ids, ignored }),
      }),
    );
  }

  // The insight templates this deployment ships (D-718). Read-only: the
  // prompts are compiled into the recorder image, so there is no PUT.
  async listInsightWorkflows(): Promise<InsightWorkflow[]> {
    return normalizeInsightWorkflows(await this.#request<unknown>("/settings/workflows"));
  }

  openEventStream(handlers: OperatorStreamHandlers): EventSource {
    const eventSource = new EventSource(`${this.#baseUrl}/events`);
    const handleMessage = (event: MessageEvent<string>) => {
      const payload = JSON.parse(event.data) as OperatorStateChangeEvent;
      handlers.onStateChange(payload);
    };
    eventSource.onopen = () => {
      handlers.onOpen?.();
    };
    eventSource.onerror = () => {
      handlers.onError?.();
    };
    eventSource.onmessage = handleMessage;
    eventSource.addEventListener("job.created", handleMessage as EventListener);
    eventSource.addEventListener("job.updated", handleMessage as EventListener);
    eventSource.addEventListener("attempt.updated", handleMessage as EventListener);
    return eventSource;
  }

  async #request<T>(path: string, init?: RequestInit): Promise<T> {
    const response = await fetch(`${this.#baseUrl}${path}`, {
      ...init,
      headers: {
        Accept: "application/json",
        ...(init?.headers ?? {}),
      },
    });
    if (!response.ok) {
      let message = `${response.status} ${response.statusText}`;
      try {
        const payload = (await response.json()) as { error?: string };
        if (typeof payload.error === "string" && payload.error.trim() !== "") {
          message = payload.error;
        }
      } catch {
        // ignore JSON parse failures and keep status text
      }
      throw new OperatorHttpError(response.status, message);
    }
    return (await response.json()) as T;
  }
}

// normalizeSettings keeps the panel resilient to the settings contract drifting:
// missing fields fall back to safe defaults and unknown quality values degrade to
// "balanced" so the UI always has a renderable, well-typed shape.
function normalizeSettings(raw: unknown): Settings {
  const value = (raw ?? {}) as Record<string, unknown>;
  const rawEffective =
    value.effective != null && typeof value.effective === "object"
      ? (value.effective as Record<string, unknown>)
      : {};
  const effective: SettingsEffective = {
    quality: normalizeQuality(rawEffective.quality),
    device: asString(rawEffective.device),
    model: asString(rawEffective.model),
    model_download_mb: asNumber(rawEffective.model_download_mb),
    min_free_memory_mb: asNumber(rawEffective.min_free_memory_mb),
    note: asString(rawEffective.note),
  };
  return {
    transcription_enabled: value.transcription_enabled === true,
    active_model: asString(value.active_model),
    active_revision: asString(value.active_revision),
    quality: normalizeQuality(value.quality),
    device_override: asString(value.device_override),
    transcription_terms: asStringArray(value.transcription_terms),
    search_aliases: asStringArrayArray(value.search_aliases),
    source: asString(value.source) || "auto",
    detected_gpu: value.detected_gpu === true,
    cores: typeof value.cores === "number" && Number.isFinite(value.cores) ? value.cores : 0,
    hardware_fingerprint: asString(value.hardware_fingerprint),
    effective,
  };
}

function normalizeQuality(value: unknown): SettingsQuality {
  return SETTINGS_QUALITIES.includes(value as SettingsQuality)
    ? (value as SettingsQuality)
    : "balanced";
}

function asNumber(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function asString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function asStringArrayArray(value: unknown): string[][] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.map((group) => asStringArray(group)).filter((group) => group.length > 0);
}

function asStringArray(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.filter((item): item is string => typeof item === "string");
}

// normalizeLLMSettings mirrors normalizeSettings: tolerate contract drift so
// the panel always has a renderable shape, and never carry a raw key even if a
// buggy server were to send one.
function normalizeLLMSettings(raw: unknown): LLMSettings {
  const value = (raw ?? {}) as Record<string, unknown>;
  const effective =
    value.effective != null && typeof value.effective === "object"
      ? (value.effective as Record<string, unknown>)
      : {};
  const providers = Array.isArray(value.providers)
    ? value.providers
        .filter((item): item is Record<string, unknown> => item != null && typeof item === "object")
        // Typed on the callback rather than left to inference: a field added
        // to LLMProviderView but not copied here is otherwise invisible to
        // the compiler, and that is exactly how `model` was served by the
        // operator, dropped on the way in, and then sent back empty (D-749).
        .map((item): LLMProviderView => ({
          id: asString(item.id),
          name: asString(item.name),
          base_url: asString(item.base_url),
          api_key_configured: item.api_key_configured === true,
          timeout_sec: asNonNegativeNumber(item.timeout_sec),
          max_tokens: asNonNegativeNumber(item.max_tokens),
          model: asString(item.model),
        }))
        .filter((item) => item.id !== "")
    : [];
  return {
    providers,
    summary: normalizeLLMStep(value.summary),
    insight: normalizeLLMStep(value.insight),
    effective: {
      summary: normalizeLLMEffectiveStep(effective.summary),
      insight: normalizeLLMEffectiveStep(effective.insight),
    },
  };
}

function normalizeLLMStep(raw: unknown): LLMStep {
  const value = (raw ?? {}) as Record<string, unknown>;
  return {
    enabled: value.enabled === true,
    provider: asString(value.provider),
    model: asString(value.model),
    template: asString(value.template),
  };
}

function normalizeLLMEffectiveStep(raw: unknown): LLMEffectiveStep | null {
  if (raw == null || typeof raw !== "object") {
    return null;
  }
  const value = raw as Record<string, unknown>;
  return {
    provider: asString(value.provider),
    base_url: asString(value.base_url),
    model: asString(value.model),
    api_key_configured: value.api_key_configured === true,
    inherited: value.inherited === true,
  };
}

function asNonNegativeNumber(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : 0;
}

const STORAGE_MODES: readonly StorageMode[] = ["", "default", "access_controlled"];

// normalizeStorage does for /storage what normalizeSettings does for /settings:
// give the panel a renderable, well-typed shape whatever the server sent.
//
// The one field it will not guess is `available`. Everything else degrades to
// an empty string or an empty list, but a mode the server did not explicitly
// call available must not become available here — that boolean is what decides
// whether the UI offers to move an entire archive.
function normalizeStorage(raw: unknown): StorageStatus {
  const value = (raw ?? {}) as Record<string, unknown>;
  return {
    mode: normalizeStorageMode(value.mode),
    mode_source: asString(value.mode_source),
    // Absent reads as UNCONFIRMED and NOT awaiting a choice, which is the pair
    // an operator predating these fields produces: it had already recorded a
    // mode, and treating it as unconfirmed is the safe direction.
    mode_confirmed: value.mode_confirmed === true,
    awaiting_choice: value.awaiting_choice === true,
    service_account: normalizeServiceAccount(value.service_account),
    ok: value.ok === true,
    state: asString(value.state),
    step: asString(value.step),
    detail: asString(value.detail),
    checked_at: asString(value.checked_at),
    // Absent reads as SETTLED, matching the operator's own absent-means-clean
    // rule. An older operator that does not send the field must not make the
    // settings section offer a cleanup that DELETES from a root.
    migration_clean: value.migration_clean !== false,
    pending_cleanup: asString(value.pending_cleanup),
    stranded_root: asString(value.stranded_root),
    stranded_recordings: asCount(value.stranded_recordings),
    modes: normalizeStorageModes(value.modes),
    transition: normalizeStorageTransition(value.transition),
    installs: normalizeInstalls(value.installs),
    preview: normalizeStoragePreview(value.preview),
    // D-757: see normalizeRecordingAccess at the end of this file.
    ...normalizeRecordingAccess(value),
    // D-769. Null rather than an empty list when the key is absent: "nobody
    // asked" and "there are none" are different answers, and only one of them
    // means the section has nothing to show.
    open_recordings: normalizeOpenRecordings(value.open_recordings),
    restricted: normalizeRestrictResults(value.restricted),
  };
}

// --- D-769 ---------------------------------------------------------------

function normalizeOpenRecordings(value: unknown): OpenRecordings | null {
  if (value === null || typeof value !== "object") {
    return null;
  }
  const row = value as Record<string, unknown>;
  return {
    recordings: normalizeOpenRecordingList(row.recordings),
    ignored: normalizeOpenRecordingList(row.ignored),
    narrowable: asCount(row.narrowable),
  };
}

function normalizeOpenRecordingList(value: unknown): OpenRecording[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.map((entry) => {
    const row = (entry ?? {}) as Record<string, unknown>;
    return {
      id: asString(row.id),
      room_name: asString(row.room_name),
      created_at: asString(row.created_at),
      // Not narrowable unless the operator said so. This boolean decides
      // whether a row offers to change who can read a recording, so an
      // operator that did not say must not be read as saying yes.
      narrowable: row.narrowable === true,
      reason: asOpenRecordingReason(row.reason),
      audience: normalizeAudience(row.audience),
      audience_digest: asString(row.audience_digest),
    };
  });
}

function asOpenRecordingReason(value: unknown): OpenRecordingReason {
  const reason = asString(value);
  if (reason === "no_job" || reason === "no_roster" || reason === "nobody_grantable") {
    return reason;
  }
  return "";
}

function normalizeAudience(value: unknown): OpenRecordingPrincipal[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map((entry) => {
      const row = (entry ?? {}) as Record<string, unknown>;
      return { type: asString(row.type), id: asString(row.id) };
    })
    .filter((principal) => principal.id !== "");
}

function normalizeRestrictResults(value: unknown): RestrictResult[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.map((entry) => {
    const row = (entry ?? {}) as Record<string, unknown>;
    return {
      id: asString(row.id),
      outcome: asRestrictOutcome(row.outcome),
      grants: asCount(row.grants),
      detail: asString(row.detail),
    };
  });
}

function asRestrictOutcome(value: unknown): RestrictOutcome {
  const outcome = asString(value);
  switch (outcome) {
    case "restricted":
    case "refused_stale":
    case "refused_empty":
    case "refused_not_open":
      return outcome;
    default:
      // An outcome this build does not recognise is not a success. Treating an
      // unknown answer as "failed" is the direction that cannot mislead.
      return "failed";
  }
}

function normalizeServiceAccount(value: unknown): StorageServiceAccount {
  const row = (value ?? {}) as Record<string, unknown>;
  return {
    user: asString(row.user),
    known: row.known === true,
    exists: row.exists === true,
    reset_occ: asString(row.reset_occ),
  };
}

function normalizeArchiveFacts(value: unknown): StorageArchiveFacts {
  const row = (value ?? {}) as Record<string, unknown>;
  return {
    probed: row.probed === true,
    present: row.present === true,
    meetings: asCount(row.meetings),
    catalog: row.catalog === true,
  };
}

function asStringList(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((entry): entry is string => typeof entry === "string" && entry !== "")
    : [];
}

// normalizeStoragePreview keeps `null` meaning "no preview was asked for",
// which is not the same as "a preview that found nothing" — the confirmation
// renders those differently, and conflating them would let a dialog claim there
// is nothing to move when nobody has looked.
function normalizeStoragePreview(value: unknown): StorageTransitionPreview | null {
  if (value == null || typeof value !== "object") {
    return null;
  }
  const row = value as Record<string, unknown>;
  return {
    mode: normalizeStorageMode(row.mode),
    ready: row.ready === true,
    step: asString(row.step),
    detail: asString(row.detail),
    source_root: asString(row.source_root),
    destination_root: asString(row.destination_root),
    source_readable: row.source_readable === true,
    meetings: asCount(row.meetings),
    catalog_present: row.catalog_present === true,
    destination_meetings: asCount(row.destination_meetings),
    destination_readable: row.destination_readable === true,
    overwrite_names: asStringList(row.overwrite_names),
    overwrite_required: row.overwrite_required === true,
    adopting_destination: row.adopting_destination === true,
    nothing_to_move: row.nothing_to_move === true,
    pending_cleanup: asString(row.pending_cleanup),
    warnings: Array.isArray(row.warnings)
      ? row.warnings.filter((w): w is string => typeof w === "string" && w !== "")
      : [],
  };
}

// asCount will not turn a missing or nonsense count into something the UI would
// state as fact. A dialog saying "0 recordings will move" when the server never
// said so is worse than saying nothing.
function asCount(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? Math.floor(value) : 0;
}

function normalizeInstalls(value: unknown): AppInstallOutcome[] {
  if (!Array.isArray(value)) {
    return [];
  }
  const out: AppInstallOutcome[] = [];
  for (const entry of value) {
    if (entry == null || typeof entry !== "object") continue;
    const row = entry as Record<string, unknown>;
    const app = asString(row.app);
    if (app === "") continue;
    out.push({ app, ok: row.ok === true, reason: asString(row.reason), detail: asString(row.detail) });
  }
  return out;
}

// normalizeSetupSteps will not invent `browser`. That flag decides whether the
// UI attempts a write against Nextcloud, and a step the server did not
// explicitly call browser-doable must never be attempted — the ones that are
// not are refused by Nextcloud every time.
function normalizeSetupSteps(value: unknown): StorageSetupStep[] {
  if (!Array.isArray(value)) {
    return [];
  }
  const out: StorageSetupStep[] = [];
  for (const entry of value) {
    if (entry == null || typeof entry !== "object") continue;
    const row = entry as Record<string, unknown>;
    const id = asString(row.id);
    const action = asString(row.action);
    if (id === "" || action === "") continue;
    const args: Record<string, string> = {};
    if (row.args != null && typeof row.args === "object" && !Array.isArray(row.args)) {
      for (const [key, raw] of Object.entries(row.args as Record<string, unknown>)) {
        if (typeof raw === "string") args[key] = raw;
      }
    }
    out.push({
      id,
      action,
      title: asString(row.title),
      args,
      browser: row.browser === true,
      occ: asString(row.occ),
      app_url: asString(row.app_url),
    });
  }
  return out;
}

function normalizeStorageMode(value: unknown): StorageMode {
  return STORAGE_MODES.includes(value as StorageMode) ? (value as StorageMode) : "";
}

function normalizeStorageModes(value: unknown): StorageModeOption[] {
  if (!Array.isArray(value)) {
    return [];
  }
  const out: StorageModeOption[] = [];
  for (const entry of value) {
    if (entry == null || typeof entry !== "object") {
      continue;
    }
    const row = entry as Record<string, unknown>;
    const mode = normalizeStorageMode(row.mode);
    if (mode === "") {
      // A row naming no mode is not a mode this build can offer to switch to.
      continue;
    }
    out.push({
      mode,
      label: asString(row.label) || mode,
      active: row.active === true,
      available: row.available === true,
      summary: asString(row.summary),
      consequence: asString(row.consequence),
      blocker: asString(row.blocker),
      step: asString(row.step),
      instructions: asStringArray(row.instructions),
      setup: normalizeSetupSteps(row.setup),
      root: asString(row.root),
      archive: normalizeArchiveFacts(row.archive),
    });
  }
  return out;
}

function normalizeStorageTransition(value: unknown): StorageTransition | null {
  if (value == null || typeof value !== "object") {
    return null;
  }
  const row = value as Record<string, unknown>;
  return {
    mode: asString(row.mode),
    confirmed: row.confirmed === true,
    meetings_moved:
      typeof row.meetings_moved === "number" && Number.isFinite(row.meetings_moved)
        ? row.meetings_moved
        : 0,
    meetings_deleted_at_destination: asCount(row.meetings_deleted_at_destination),
    catalog_moved: row.catalog_moved === true,
    source_root: asString(row.source_root),
    destination_root: asString(row.destination_root),
    source_cleared: row.source_cleared === true,
    leftover_source: asString(row.leftover_source),
  };
}

// normalizeInsightWorkflows keeps the panel from rendering a row it cannot
// describe. A workflow with no id or no content hash is one an insight
// document could never be traced back to, which is the whole point of the
// listing, so it is dropped rather than shown as a template you could pick.
// The operator refuses such an entry too; this is the second gate, because the
// panel is what a person believes.
//
// A body that is not a list at all is a different failure and gets a different
// answer: the endpoint never serves one (it replaces a nil registry with an
// empty array precisely so success cannot look like absence), so this is a
// build talking to something that is not the operator it expects. Returning []
// would put the panel's "This build ships no templates." on the screen — a
// positive claim about the image, made from a body nobody understood.
function normalizeInsightWorkflows(raw: unknown): InsightWorkflow[] {
  if (!Array.isArray(raw)) {
    throw new Error("the workflow registry came back in a shape this app does not understand");
  }
  return raw
    .filter((item): item is Record<string, unknown> => item != null && typeof item === "object")
    .map((item) => ({
      id: asString(item.id),
      version: asString(item.version),
      sha256: asString(item.sha256),
      name: asString(item.name),
      question: asString(item.question),
      description: asString(item.description),
      origin: asString(item.origin),
      instruction: asString(item.instruction),
    }))
    .filter((item) => item.id !== "" && item.sha256 !== "");
}

// --- D-757: the fields GET /storage gained for "Who can see recordings" -------
//
// A block of its own at the end of the file, spliced into normalizeStorage by
// one line, so a branch adding other fields to the same response does not
// collide with this one.

// normalizeRecordingAccess reads the two fields the settings section needs.
//
// Absent reads as "not the first run" and "no switch is running", which is what
// an operator predating these fields produces: it has been serving recordings
// for a while, and it has no switch in flight it could tell us about. The safe
// direction for both — a first-run dialog shown to an install that has been
// running for months, or a progress panel for a move nobody started, would each
// be a claim made from a missing field.
function normalizeRecordingAccess(value: Record<string, unknown>): {
  first_run: boolean;
  migration: StorageMigration | null;
} {
  return {
    first_run: value.first_run === true,
    migration: normalizeMigration(value.migration),
  };
}

// normalizeMigration keeps `null` meaning "no switch is running". A row that is
// present but not active means the same thing and is normalised to null here,
// so the UI has one test rather than two.
function normalizeMigration(value: unknown): StorageMigration | null {
  if (value == null || typeof value !== "object") {
    return null;
  }
  const row = value as Record<string, unknown>;
  if (row.active !== true) {
    return null;
  }
  return {
    active: true,
    phase: normalizeMigrationPhase(row.phase),
    done: asCount(row.done),
    total: asCount(row.total),
  };
}

const MIGRATION_PHASES: readonly StorageMigration["phase"][] = [
  "copying",
  "verifying",
  "switching",
  "clearing",
];

// A phase this build has never heard of reads as the FIRST one. The steps are
// ordered and a switch only ever moves forward through them, so the earliest is
// the one guess that cannot claim work is finished when it is not.
function normalizeMigrationPhase(value: unknown): StorageMigration["phase"] {
  return MIGRATION_PHASES.find((phase) => phase === value) ?? "copying";
}
