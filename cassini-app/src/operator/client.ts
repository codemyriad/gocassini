import type { RecordingReadiness, RecordingSetupUpdate } from "./readiness";
import type {
  InsightWorkflow,
  Job,
  JobAttempt,
  JobDetailResponse,
  LLMEffectiveStep,
  LLMModel,
  LLMSettings,
  LLMSettingsUpdate,
  LLMStep,
  Settings,
  SettingsEffective,
  SettingsQuality,
  SettingsUpdate,
  AppInstallOutcome,
  StorageArchiveFacts,
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
    return this.#request<RecordingReadiness>("/readiness");
  }

  async checkReadiness(): Promise<RecordingReadiness> {
    return this.#request<RecordingReadiness>("/readiness/check", { method: "POST" });
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
  // its preflight before answering, so there is no half-switched state to poll
  // for and nothing useful this client could do with one.
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
  // them until it re-probes — without this the Setup tab would go on reporting
  // what was missing before the administrator fixed it. It is also what a plan
  // is RECOMPUTED from: the operator cannot see a Team folder until
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
        .map((item) => ({
          id: asString(item.id),
          name: asString(item.name),
          base_url: asString(item.base_url),
          api_key_configured: item.api_key_configured === true,
          timeout_sec: asNonNegativeNumber(item.timeout_sec),
          max_tokens: asNonNegativeNumber(item.max_tokens),
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
    // mode, and the wizard asking about it once is the safe direction.
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
    // Setup tab offer a cleanup that DELETES from a root.
    migration_clean: value.migration_clean !== false,
    pending_cleanup: asString(value.pending_cleanup),
    stranded_root: asString(value.stranded_root),
    stranded_recordings: asCount(value.stranded_recordings),
    modes: normalizeStorageModes(value.modes),
    transition: normalizeStorageTransition(value.transition),
    installs: normalizeInstalls(value.installs),
    preview: normalizeStoragePreview(value.preview),
  };
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
// would put the panel's "This build ships no templates. The registry answered,
// and it is empty." on the screen — a positive claim about the image, made from
// a body nobody understood.
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
