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
  transcription_terms: string[];
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
  transcription_terms: string[];
}

// StorageMode mirrors the operator's storage_settings.json vocabulary (D-616).
// "" is a third answer, not a missing one: it means no preflight has resolved a
// mode yet, which the Setup tab has to be able to tell apart from "default".
export type StorageMode = "" | "default" | "access_controlled";

// StorageModeOption is one of the two models as GET <basePath>/storage
// describes it. The copy — summary, consequence, blocker, instructions — comes
// from the operator rather than from this app, because that is the layer that
// knows the Team folder's id, the group names and which prerequisite is
// actually absent. The panel renders it and decides nothing.
export interface StorageModeOption {
  mode: Exclude<StorageMode, "">;
  label: string;
  active: boolean;
  available: boolean;
  summary: string;
  consequence: string;
  blocker: string;
  step: string;
  instructions: string[];
}

// StorageTransition is what a switch actually did, present only on the PUT that
// performed one.
export interface StorageTransition {
  mode: string;
  meetings_moved: number;
  catalog_moved: boolean;
  source_root: string;
  destination_root: string;
  leftover_source: string;
  unmapped_groups: string[];
}

export interface StorageStatus {
  mode: StorageMode;
  mode_source: string;
  ok: boolean;
  state: string;
  step: string;
  detail: string;
  checked_at: string;
  modes: StorageModeOption[];
  transition: StorageTransition | null;
}
