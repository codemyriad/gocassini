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
