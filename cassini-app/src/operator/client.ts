import type {
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
        }))
        .filter((item) => item.id !== "")
    : [];
  return {
    providers,
    summary: normalizeLLMStep(value.summary),
    timeout_sec: asNonNegativeNumber(value.timeout_sec),
    max_tokens: asNonNegativeNumber(value.max_tokens),
    effective: {
      summary: normalizeLLMEffectiveStep(effective.summary),
    },
  };
}

function normalizeLLMStep(raw: unknown): LLMStep {
  const value = (raw ?? {}) as Record<string, unknown>;
  return {
    enabled: value.enabled === true,
    provider: asString(value.provider),
    model: asString(value.model),
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
  };
}

function asNonNegativeNumber(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : 0;
}
