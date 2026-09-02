import type {
  Job,
  JobAttempt,
  JobDetailResponse,
  Settings,
  SettingsEffective,
  SettingsQuality,
  SettingsUpdate,
  StorageMode,
  StorageModeOption,
  StorageStatus,
  StorageTransition,
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

  async getStorage(): Promise<StorageStatus> {
    return normalizeStorage(await this.#request<unknown>("/storage"));
  }

  // putStorage switches the storage model, which MOVES every published
  // recording. It is one call and it blocks for the length of the move: the
  // operator holds its provisioning lock for the whole transition and re-runs
  // its preflight before answering, so there is no half-switched state to poll
  // for and nothing useful this client could do with one.
  async putStorage(accessControlEnabled: boolean): Promise<StorageStatus> {
    return normalizeStorage(
      await this.#request<unknown>("/storage", {
        method: "PUT",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({ access_control_enabled: accessControlEnabled }),
      }),
    );
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

function asStringArray(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.filter((item): item is string => typeof item === "string");
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
    ok: value.ok === true,
    state: asString(value.state),
    step: asString(value.step),
    detail: asString(value.detail),
    checked_at: asString(value.checked_at),
    modes: normalizeStorageModes(value.modes),
    transition: normalizeStorageTransition(value.transition),
  };
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
    meetings_moved:
      typeof row.meetings_moved === "number" && Number.isFinite(row.meetings_moved)
        ? row.meetings_moved
        : 0,
    catalog_moved: row.catalog_moved === true,
    source_root: asString(row.source_root),
    destination_root: asString(row.destination_root),
    leftover_source: asString(row.leftover_source),
    unmapped_groups: asStringArray(row.unmapped_groups),
  };
}
