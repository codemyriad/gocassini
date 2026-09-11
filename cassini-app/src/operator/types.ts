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
  /** The Talk conversation's display name, as it was when this job ran. Null
   * for a non-Talk job, for a job whose room-name lookup never completed, and
   * for any job recorded before the operator promoted the room to a column. */
  room_name: string | null;
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
  /**
   * Spellings the transcriber produces for names it gets wrong, one group per
   * name: the first entry is what someone would type, the rest are what the
   * transcript actually says. Searching for any of them finds all of them.
   */
  search_aliases: string[][];
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
  search_aliases: string[][];
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
  // root is where this model keeps recordings, and archive is what is in it
  // right now. Both are reported for BOTH models, always — the question the
  // setup wizard is built around is answered by what is already in each of them
  // (D-708).
  root: string;
  archive: StorageArchiveFacts;
}

// StorageArchiveFacts is one recordings root as the operator's last probe saw
// it. `probed` is load-bearing: false means nobody could look, and it must never
// render as "empty".
export interface StorageArchiveFacts {
  probed: boolean;
  present: boolean;
  meetings: number;
  catalog: boolean;
}

// StorageServiceAccount is the account every recording is written and read as.
// There is no password field and never will be: it is generated in the browser,
// shown once, and never reaches the operator.
export interface StorageServiceAccount {
  user: string;
  known: boolean;
  exists: boolean;
  reset_occ: string;
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
  // confirmed marks the one outcome that moves nothing and still changes
  // something: an administrator agreeing to the mode already in force, which is
  // what turns an unconfirmed install into a settled one.
  confirmed: boolean;
  // meetings_moved is how many recordings were copied from the source archive.
  meetings_moved: number;
  // meetings_deleted_at_destination is what was removed from the confirmed
  // destination before the source archive was copied.
  meetings_deleted_at_destination: number;
  catalog_moved: boolean;
  source_root: string;
  destination_root: string;
  // source_cleared is false when the archive arrived but the tidy-up did not
  // finish. The switch worked; there is a leftover copy and a button for it.
  source_cleared: boolean;
  leftover_source: string;
}

export interface StorageStatus {
  mode: StorageMode;
  mode_source: string;
  // mode_confirmed says a person (or a dev/CI deploy option) chose this mode,
  // as opposed to a build recording one on its own. False is what puts the
  // Setup tab into its wizard rather than its settled panel (D-708).
  mode_confirmed: boolean;
  // awaiting_choice says nothing is recorded at all. Not the same as
  // `mode === ""`, which also happens before any preflight has run.
  awaiting_choice: boolean;
  service_account: StorageServiceAccount;
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
// of it — including the destination artefacts that require confirmation.
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
  // destination_meetings is what is already where this would write.
  destination_meetings: number;
  // destination_readable is the same distinction source_readable draws, for the
  // other tree.
  destination_readable: boolean;
  overwrite_names: string[];
  overwrite_required: boolean;
  // adopting_destination means an unconfigured installation already has an
  // archive in the selected root. Selecting it records the choice only; no
  // archive is overwritten, copied, or removed.
  adopting_destination: boolean;
  nothing_to_move: boolean;
  // pending_cleanup is set when an earlier switch did not finish, so the
  // administrator is told the stale root is cleared before this one starts.
  pending_cleanup: string;
  // warnings are one sentence each, most-surprising first.
  warnings: string[];
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

// --- D-757: what GET /storage gained for "Who can see recordings" -------------
//
// Declared as a separate block, merged into the interface above rather than
// edited into it, so that two branches adding fields to the same response do
// not collide on the same lines. Same file, same interface: TypeScript merges
// these declarations, and nothing downstream can tell the difference.

// StorageMigration is a mode switch that is RUNNING. Null at every other
// moment. The phases are the operator's own order — copy, verify, flip the
// mode, clear the old root — which is what lets an interrupted switch be
// described honestly: whichever phase it stopped at, a complete archive exists
// somewhere. Counts are recordings.
export interface StorageMigration {
  active: boolean;
  phase: "copying" | "verifying" | "switching" | "clearing";
  done: number;
  total: number;
}

export interface StorageStatus {
  // first_run is true until an administrator acknowledges the first-run dialog.
  // It is per INSTALL, in the operator's settings store, not per browser: a
  // second administrator opening Cassini must not be asked again.
  first_run: boolean;
  migration: StorageMigration | null;
}
