export interface Job {
  id: string;
  provider: string;
  request_json: string;
  stage: string;
  state: string;
  current_attempt_number: number;
  rerun_count: number;
  artifact_run_path: string | null;
  artifact_meeting_path: string | null;
  artifact_opus_path: string | null;
  artifact_opus_sha256: string | null;
  artifact_site_path: string | null;
  error: string | null;
  stop_reason: string | null;
  stop_requested_at: string | null;
  stop_signal_sent_at: string | null;
  record_exit_code: number | null;
  record_stop_detail: string | null;
  created_at: string;
  updated_at: string;
  record_queued_at: string | null;
  record_started_at: string | null;
  record_finished_at: string | null;
  build_queued_at: string | null;
  build_retry_not_before: string | null;
  build_deferral_count: number;
  build_started_at: string | null;
  build_finished_at: string | null;
  seal_queued_at: string | null;
  seal_started_at: string | null;
  seal_finished_at: string | null;
  publish_queued_at: string | null;
  publish_started_at: string | null;
  publish_finished_at: string | null;
  interrupted_at: string | null;
  completed_at: string | null;
}

export interface JobAttempt {
  job_id: string;
  attempt_number: number;
  trigger_kind: string;
  request_json: string;
  stage: string;
  state: string;
  artifact_run_path: string | null;
  artifact_meeting_path: string | null;
  artifact_opus_path: string | null;
  artifact_opus_sha256: string | null;
  artifact_site_path: string | null;
  error: string | null;
  stop_reason: string | null;
  stop_requested_at: string | null;
  stop_signal_sent_at: string | null;
  record_exit_code: number | null;
  record_stop_detail: string | null;
  record_log_path: string | null;
  build_log_path: string | null;
  seal_log_path: string | null;
  publish_log_path: string | null;
  created_at: string;
  updated_at: string;
  record_queued_at: string | null;
  record_started_at: string | null;
  record_finished_at: string | null;
  build_queued_at: string | null;
  build_retry_not_before: string | null;
  build_deferral_count: number;
  build_started_at: string | null;
  build_finished_at: string | null;
  seal_queued_at: string | null;
  seal_started_at: string | null;
  seal_finished_at: string | null;
  publish_queued_at: string | null;
  publish_started_at: string | null;
  publish_finished_at: string | null;
  interrupted_at: string | null;
  completed_at: string | null;
}

export interface JobDetailResponse {
  job: Job;
  attempts: JobAttempt[];
}

export type SettingsQuality = "fast" | "balanced" | "best";

// Settings mirror GET/PUT <basePath>/settings (D-435). The panel stays tolerant
// of extra or missing fields so a server on a slightly older/newer contract
// still renders rather than erroring.
export interface Settings {
  quality: SettingsQuality;
  device_override: string;
  model_override: string;
  source: string;
  detected_gpu: boolean;
  cores: number;
  hardware_fingerprint: string;
  effective: Record<string, unknown>;
}

export interface SettingsUpdate {
  quality: SettingsQuality;
  device_override: string;
  model_override: string;
}

// --- LLM settings (D-696): mirror GET/PUT <basePath>/settings/llm. Keys are
// write-only: the server reports api_key_configured and never the value.

export interface LLMProviderView {
  id: string;
  name: string;
  base_url: string;
  api_key_configured: boolean;
}

export interface LLMStep {
  enabled: boolean;
  provider: string;
  model: string;
}

export interface LLMEffectiveStep {
  provider: string;
  base_url: string;
  model: string;
  api_key_configured: boolean;
}

export interface LLMSettings {
  providers: LLMProviderView[];
  summary: LLMStep;
  timeout_sec: number;
  max_tokens: number;
  effective: {
    summary: LLMEffectiveStep | null;
  };
}

// api_key semantics on PUT: omitted/null keeps the stored key for that id,
// "" clears it, any other string replaces it.
export interface LLMProviderUpdate {
  id: string;
  name: string;
  base_url: string;
  api_key?: string | null;
}

export interface LLMSettingsUpdate {
  providers?: LLMProviderUpdate[];
  summary?: LLMStep;
  timeout_sec?: number;
  max_tokens?: number;
}

export interface LLMModel {
  id: string;
  name?: string;
  context_length?: number;
}
