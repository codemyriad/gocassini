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
  StorageServiceAccount,
  StorageSetupStep,
  StorageStatus,
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

  // Re-probe Nextcloud after browser-side account creation.
  async recheckStorage(): Promise<StorageStatus> {
    return normalizeStorage(
      await this.#request<unknown>("/storage", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ action: "recheck" }),
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

function normalizeStorage(raw: unknown): StorageStatus {
  const value = (raw ?? {}) as Record<string, unknown>;
  return {
    first_run: value.first_run === true,
    service_account: normalizeServiceAccount(value.service_account),
    ok: value.ok === true,
    state: asString(value.state),
    step: asString(value.step),
    detail: asString(value.detail),
    checked_at: asString(value.checked_at),
    setup: normalizeSetupSteps(value.setup),
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

function normalizeSetupSteps(value: unknown): StorageSetupStep[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((entry) => {
    if (entry == null || typeof entry !== "object") return [];
    const row = entry as Record<string, unknown>;
    const id = asString(row.id);
    const action = asString(row.action);
    if (!id || !action) return [];
    const args: Record<string, string> = {};
    if (row.args != null && typeof row.args === "object" && !Array.isArray(row.args)) {
      for (const [key, raw] of Object.entries(row.args as Record<string, unknown>)) {
        if (typeof raw === "string") args[key] = raw;
      }
    }
    return [{ id, action, title: asString(row.title), args,
      browser: row.browser === true, occ: asString(row.occ) }];
  });
}

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
