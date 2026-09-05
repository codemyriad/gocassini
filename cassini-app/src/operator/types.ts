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
  // setup is the same recipe as something to EXECUTE (D-671). Empty for a mode
  // that is already available.
  setup: StorageSetupStep[];
}

// StorageSetupStep is one missing prerequisite and how to make it exist.
// `browser` is the load-bearing field: false means Nextcloud requires the
// administrator's password on the request itself (a `strict` password
// confirmation), which no session can satisfy and Cassini will not do — those
// steps are attempted by the operator instead, and handed off if it is refused.
export interface StorageSetupStep {
  id: string;
  action: string;
  title: string;
  args: Record<string, string>;
  browser: boolean;
  occ: string;
  app_url: string;
}

// AppInstallOutcome is what the operator's own attempt at a `strict` app
// install produced. The reason is what the UI branches on: `enabled` is done,
// `password_confirmation_required` needs Nextcloud's Apps page, and
// `app_store_unavailable` must not be retried for five minutes.
export interface AppInstallOutcome {
  app: string;
  ok: boolean;
  reason: string;
  detail: string;
}

// StorageTransition is what a switch actually did, present only on the PUT that
// performed one.
export interface StorageTransition {
  mode: string;
  meetings_moved: number;
  catalog_moved: boolean;
  source_root: string;
  destination_root: string;
  // meetings_replaced is how many copies overwrote a leftover from an attempt
  // that did not finish. Every source recording is copied either way — nothing
  // is skipped — so this is what says "this was a retry", not a count of work
  // avoided.
  meetings_replaced: number;
  // source_cleared is false when the archive arrived but the tidy-up did not
  // finish. The switch worked; there is a leftover copy and a button for it.
  source_cleared: boolean;
  leftover_source: string;
}

export interface StorageStatus {
  mode: StorageMode;
  mode_source: string;
  // migration_clean is false when a mode switch stopped before it finished
  // tidying up. The archive is complete at the mode's own root — that is the
  // invariant the operator keeps — and pending_cleanup names the root holding
  // the leftovers.
  migration_clean: boolean;
  pending_cleanup: string;
  // stranded_root / stranded_recordings report an archive sitting in the mode
  // that is NOT in force. Not an error: publishing and reading both work. It is
  // the thing an administrator most needs told, because the symptom is "my
  // recordings are gone" and the cause is a mode nobody switched.
  stranded_root: string;
  stranded_recordings: number;
  ok: boolean;
  state: string;
  step: string;
  detail: string;
  checked_at: string;
  modes: StorageModeOption[];
  transition: StorageTransition | null;
  installs: AppInstallOutcome[];
  // preview is present only on the response to a preview request. Nothing has
  // happened when it is set.
  preview: StorageTransitionPreview | null;
}

// StorageTransitionPreview is what a mode switch WOULD do, before it does any
// of it — the facts the confirmation states alongside the policy.
export interface StorageTransitionPreview {
  // mode is the mode being previewed, not the one in force.
  mode: StorageMode;
  // ready is whether the switch could run at all; step/detail say why not.
  ready: boolean;
  step: string;
  detail: string;
  source_root: string;
  destination_root: string;
  // source_readable says the source tree was actually listed. Without it a
  // failed PROPFIND and an empty archive are the same zero, and the dialog says
  // "there are no published recordings to move" on the strength of a question
  // nobody managed to ask — which is exactly what QA saw.
  source_readable: boolean;
  meetings: number;
  catalog_present: boolean;
  // destination_meetings is what is already where this would write. Not fatal —
  // the transition merges — but the single most important thing to say out loud
  // before merging somebody's archive.
  destination_meetings: number;
  nothing_to_move: boolean;
  // pending_cleanup is set when an earlier switch did not finish, so the
  // administrator is told the stale root is cleared before this one starts.
  pending_cleanup: string;
  // warnings are one sentence each, most-surprising first.
  warnings: string[];
}
