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
// SettingsEffective is the operator's answer to "what will the next build
// actually do": the device its resource governor will admit, the model the
// recorder will load on it, and one sentence of context. On a host with no
// usable GPU that device is the CPU — a supported, slower outcome the panel
// must show rather than leave the admin to infer from a failed build.
export interface SettingsEffective {
  quality: SettingsQuality;
  device: string;
  model: string;
  // Approximate download size in MB when the running image does not bake this
  // model, and 0 when the build starts without a download.
  model_download_mb: number;
  // Memory that must be free before a build of this tier starts, in MB.
  min_free_memory_mb: number;
  note: string;
}

export interface Settings {
  quality: SettingsQuality;
  device_override: string;
  transcription_terms: string[];
  source: string;
  detected_gpu: boolean;
  cores: number;
  hardware_fingerprint: string;
  effective: SettingsEffective;
}

export interface SettingsUpdate {
  quality: SettingsQuality;
  device_override: string;
  transcription_terms: string[];
}

// --- LLM settings (D-696): mirror GET/PUT <basePath>/settings/llm. Keys are
// write-only: the server reports api_key_configured and never the value.

export interface LLMProviderView {
  id: string;
  name: string;
  base_url: string;
  api_key_configured: boolean;
  // 0 means "use the recorder default" (900s / 4096 tokens).
  timeout_sec: number;
  max_tokens: number;
}

export interface LLMStep {
  enabled: boolean;
  provider: string;
  model: string;
  // The workflow this step runs, by id. Empty means the shipped default,
  // which is what a settings file written before D-719 reads as.
  template: string;
}

export interface LLMEffectiveStep {
  provider: string;
  base_url: string;
  model: string;
  api_key_configured: boolean;
  // True when this step has no endpoint of its own and is running on another
  // step's — so it moves when that one moves (D-719).
  inherited: boolean;
}

export interface LLMSettings {
  providers: LLMProviderView[];
  summary: LLMStep;
  // The endpoint an insight runs on. Off means it inherits the summary's,
  // never that insights are unavailable (D-719).
  insight: LLMStep;
  effective: {
    summary: LLMEffectiveStep | null;
    insight: LLMEffectiveStep | null;
  };
}

// api_key semantics on PUT: omitted/null keeps the stored key for that id,
// "" clears it, any other string replaces it.
export interface LLMProviderUpdate {
  id: string;
  name: string;
  base_url: string;
  api_key?: string | null;
  timeout_sec?: number;
  max_tokens?: number;
}

export interface LLMSettingsUpdate {
  providers?: LLMProviderUpdate[];
  summary?: LLMStep;
  insight?: LLMStep;
}

export interface LLMModel {
  id: string;
  name?: string;
  context_length?: number;
}

// --- Insight templates (D-718): mirror GET <basePath>/settings/workflows.
// Read-only. The workflows are prompts compiled into the recorder image, so
// there is nothing to write back and no PUT to write it with.

export interface InsightWorkflow {
  id: string;
  // The immutable version of the prompt. A change is a new version, never an
  // edit in place, so (id, version) names one set of bytes forever.
  version: string;
  // SHA-256 of those bytes. An insight document records it, which is how a
  // document a month old can be traced to the prompt that made it.
  sha256: string;
  name: string;
  // What this workflow asks of a set of meetings, in a person's words. The
  // name says nothing about what the model is asked to do, so this is what a
  // row discloses under it.
  question: string;
  description: string;
  // Where the bytes came from — "Built in" for everything shipped in the
  // image. Derived by the recorder, so a second resolver would say something
  // else here without any row being edited to admit it.
  origin: string;
  // The system prompt with its template already spliced in: the exact bytes
  // sent to the model, not a description of them.
  instruction: string;
}
