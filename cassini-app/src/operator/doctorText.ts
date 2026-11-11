// Doctor's words in the reader's language (D-921).
//
// The operator writes each check's message, its remedy steps and the reason a
// recording is blocked in English, and sends with them a code, a step id or a
// reason code and the values the English embeds (`params`). This module words
// them again from those, through the `app.doctor_checks` dictionary.
//
// Anything it cannot word falls back to the operator's English: a code, step
// or reason this build does not know (a newer operator), a variant it does not
// know, or a param that is missing (an older operator, which sent none). Raw
// errors and the operator's internal names inside a param stay English on
// purpose, as everywhere else.
//
// Every code, step id and reason the operator source can emit is listed in
// doctorCatalog.json, generated from that source by the operator's
// doctor_catalog_test.go; doctorText.test.ts fails while one of them has no
// entry here.
import { get } from "svelte/store";
import { LL, locale } from "cassini-viewer/i18n";
import { checkLabels, type ReadinessCheck, type ReadinessStep } from "./readiness";
import type { RecordedAtBlockedFile } from "./recordedAtMigration";

export type ReadinessParams = Record<string, string | number | string[]>;

// readParams keeps the params of the kinds the operator sends; anything else
// is dropped, and a sentence needing it falls back to English.
export function readParams(value: unknown): ReadinessParams | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const out: ReadinessParams = {};
  for (const [key, item] of Object.entries(value)) {
    if (typeof item === "string" || (typeof item === "number" && Number.isFinite(item))) out[key] = item;
    else if (Array.isArray(item) && item.every(entry => typeof entry === "string")) out[key] = item;
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

const copy = () => get(LL).app.doctor_checks;

// Thrown when a param the sentence needs is absent; the caller then shows the
// operator's English.
class MissingParam extends Error {}

class Params {
  constructor(private readonly params: ReadinessParams | undefined) {}
  str(name: string): string {
    const value = this.params?.[name];
    if (typeof value === "string" && value !== "") return value;
    if (typeof value === "number") return String(value);
    throw new MissingParam(name);
  }
  num(name: string): number {
    const value = this.params?.[name];
    if (typeof value === "number" && Number.isFinite(value)) return value;
    throw new MissingParam(name);
  }
  list(name: string): string[] {
    const value = this.params?.[name];
    if (Array.isArray(value) && value.length > 0 && value.every(item => typeof item === "string")) return value;
    throw new MissingParam(name);
  }
  variant(): string {
    return this.str("variant");
  }
  has(name: string): boolean {
    return this.params?.[name] !== undefined;
  }
}

// What the panel renders as code: the label is split on backticks.
const code = (text: string) => "`" + text + "`";

// Group names as the operator writes them: “a”, “b” in a message, `a`, `b`
// in a step. Separated by commas, as the operator's English does.
const namedGroups = (groups: string[]) => groups.map(group => copy().sharing.group({ group })).join(", ");
const quotedGroups = (groups: string[]) => groups.map(code).join(", ");

// A list that ends in "and", in the reader's language. English is en-GB's: the
// operator's own sentence has no serial comma.
function conjunction(items: string[]): string {
  const language = get(locale);
  try {
    return new Intl.ListFormat(language === "en" ? "en-GB" : language, { type: "conjunction" }).format(items);
  } catch {
    return items.join(", ");
  }
}

// A row's message by its code. Undefined: a variant this build does not know.
type Wording = (p: Params, check: ReadinessCheck) => string | undefined;

const fixed = (key: keyof ReturnType<typeof copy>["message"]): Wording =>
  () => (copy().message[key] as () => string)();

// Every variant a wording below picks by, so doctorText.test.ts can hold them
// to doctorCatalog.json `variants`.
const variantKeys = new Set<string>();
const variants = (byVariant: Record<string, Wording>): Wording => {
  Object.keys(byVariant).forEach(key => variantKeys.add(key));
  return (p, check) => (Object.hasOwn(byVariant, p.variant()) ? byVariant[p.variant()](p, check) : undefined);
};

// The sentence after the archive search summary, by `reindex`; every value in
// doctorCatalog.json `reindex`.
export const reindexWordings: Record<string, (p: Params) => string> = {
  running: () => copy().search.reindex.running(),
  finished: (p) => copy().search.reindex.finished({
    added: p.num("reindex_added"), unchanged: p.num("reindex_unchanged"),
    unsearchable: p.num("reindex_unsearchable"), failed: p.num("reindex_failed"),
  }),
  archive_unreadable: () => copy().search.reindex.archive_unreadable(),
  index_unavailable: () => copy().search.reindex.index_unavailable(),
  environment: () => copy().search.reindex.environment(),
  failed: (p) => copy().search.reindex.failed({ error: p.str("error") }),
};

const searchSummary = variants({
  index_unavailable: () => copy().search.index_unavailable(),
  inventory_unreadable: () => copy().search.inventory_unreadable(),
  index_unreadable: () => copy().search.index_unreadable(),
  nothing_recorded: () => copy().search.nothing_recorded(),
  archive_empty: () => copy().search.archive_empty(),
  not_checked: () => copy().search.not_checked(),
  coverage: (p) => searchCoverage(p),
  coverage_unlisted: (p) => `${searchCoverage(p)} ${copy().search.not_listed()}`,
  coverage_complete: (p) => `${searchCoverage(p)} ${copy().search.all_accounted()}`,
});

const searchMessage: Wording = (p, check) => {
  const first = searchSummary(p, check);
  if (first === undefined) return undefined;
  if (!p.has("reindex")) return first;
  const reindex = p.str("reindex");
  return Object.hasOwn(reindexWordings, reindex) ? `${first} ${reindexWordings[reindex](p)}` : undefined;
};

// describeSearchCoverage in the operator's search_readiness.go, in the same
// order.
const searchReasons = ["silent", "disabled", "model_unavailable", "transcription_failed", "unknown_empty",
  "missing_transcript", "unreadable_transcript", "backfill_candidates", "other_unavailable", "untracked"] as const;

function searchCoverage(p: Params): string {
  const t = copy().search;
  const reasons = searchReasons
    .map(reason => ({ reason, count: p.num(reason) }))
    .filter(({ count }) => count > 0)
    .map(({ reason, count }) => t.reason[reason]({ count }));
  const indexed = p.num("indexed");
  if (indexed === 0) {
    return reasons.length === 0 ? t.none_searchable() : t.none_searchable_because({ reasons: conjunction(reasons) });
  }
  const searchable = t.searchable({ count: indexed });
  return reasons.length === 0 ? searchable : `${searchable} ${t.others({ reasons: conjunction(reasons) })}`;
}

// One sharing sentence by issue; every id in doctorCatalog.json `sharing_issues`.
export const sharingIssueWordings: Record<string, (p: Params) => string> = {
  share_api_off: () => copy().sharing.share_api_off(),
  excluded_groups: (p) => {
    const groups = p.list("excluded_groups");
    return copy().sharing.excluded_groups({ groups: namedGroups(groups), account: p.str("account"), count: groups.length });
  },
  no_allowed_group: () => copy().sharing.no_allowed_group(),
  not_in_allowed_groups: (p) => copy().sharing.not_in_allowed_groups({ groups: namedGroups(p.list("allowed_groups")), account: p.str("account") }),
  in_no_group: (p) => copy().sharing.in_no_group({ account: p.str("account") }),
  groups_ignored: (p) => copy().sharing.groups_ignored({ groups: namedGroups(p.list("groups")), account: p.str("account") }),
  only_groups: (p) => copy().sharing.only_groups({ groups: namedGroups(p.list("shareable_groups")), account: p.str("account") }),
  only_admins: (p) => copy().sharing.only_admins({ groups: namedGroups(p.list("shareable_groups")), account: p.str("account") }),
  group_sharing_off: () => copy().sharing.group_sharing_off(),
};

// The Recording sharing row (and the storage row when the Share API is off):
// one sentence per issue, in the operator's order.
const sharingMessage: Wording = (p) => {
  const issues = p.list("issues");
  if (!issues.every(issue => Object.hasOwn(sharingIssueWordings, issue))) return undefined;
  return issues.map(issue => sharingIssueWordings[issue](p)).join(" ");
};

// The host rows: what doctor found, by its variant.
const hostMessage = (row: "workdir" | "tmpdir"): Wording => {
  const t = () => copy().host;
  return variants({
    ...(row === "workdir" ? {
      found: (p: Params) => t().workdir_found({ path: p.str("path") }),
      unavailable: (p: Params) => t().workdir_unavailable({ error: p.str("error") }),
    } : {}),
    writable: (p) => t()[`${row}_writable`]({ path: p.str("path") }),
    not_accessible: (p) => t()[`${row}_not_accessible`]({ error: p.str("error") }),
    not_a_directory: (p) => t()[`${row}_not_a_directory`]({ path: p.str("path") }),
    not_writable: (p) => t()[`${row}_not_writable`]({ path: p.str("path"), error: p.str("error") }),
  });
};

// Every code in doctorCatalog.json `messages`, and nothing else.
export const messageWordings: Record<string, Wording> = {
  check_blocked: variants({
    unchecked: (p) => blockerName(p, name => copy().message.check_blocked_unchecked({ blocker: name })),
    needs_attention: (p) => blockerName(p, name => copy().message.check_blocked_needs_attention({ blocker: name })),
  }),
  connection_not_checked: fixed("connection_not_checked"),
  group_sharing_disabled: sharingMessage,
  host_checks_unavailable: fixed("host_checks_unavailable"),
  host_not_checked: fixed("host_not_checked"),
  hpb_authenticated: fixed("hpb_authenticated"),
  hpb_declared_unverified: variants({
    unverified: fixed("hpb_declared_unverified"),
    secret_from_env: fixed("hpb_declared_unverified_secret_from_env"),
    secret_saved: fixed("hpb_declared_unverified_secret_saved"),
  }),
  hpb_disabled: fixed("hpb_disabled"),
  hpb_missing: fixed("hpb_missing"),
  hpb_not_checked: fixed("hpb_not_checked"),
  hpb_unsupported: fixed("hpb_unsupported"),
  internal_clients_disabled: () => copy().message.internal_clients_disabled({ setting: "internalsecret" }),
  // The operator's row asks for the secret; the recorder's, which it replaces
  // once it has looked, only notes it is not there.
  internal_secret_missing: (_, check) => check.state === "needs_action"
    ? copy().message.internal_secret_missing()
    : copy().message.internal_secret_missing_unverified(),
  nextcloud_connection_refused: fixed("nextcloud_connection_refused"),
  nextcloud_host_not_found: fixed("nextcloud_host_not_found"),
  nextcloud_server_error: fixed("nextcloud_server_error"),
  nextcloud_timeout: fixed("nextcloud_timeout"),
  nextcloud_tls_untrusted: fixed("nextcloud_tls_untrusted"),
  nextcloud_unexpected_response: fixed("nextcloud_unexpected_response"),
  nextcloud_unreachable: fixed("nextcloud_unreachable"),
  probe_failed: fixed("probe_failed"),
  recording_auth_rejected: fixed("recording_auth_rejected"),
  recording_auth_verified: fixed("recording_auth_verified"),
  // The operator's Recording credential row, or the recorder's Talk connection row.
  recording_secret_missing: (_, check) => check.id === "talk.discovery"
    ? copy().message.recording_secret_missing_connection()
    : copy().message.recording_secret_missing(),
  search_archive_empty: searchMessage,
  search_archive_files_accounted_for: searchMessage,
  search_archive_unreadable: searchMessage,
  search_coverage_empty: searchMessage,
  search_coverage_not_checked: searchMessage,
  search_coverage_partial: searchMessage,
  search_coverage_scope_unknown: searchMessage,
  search_coverage_unknown: searchMessage,
  search_reindex_archive_unreadable: searchMessage,
  search_reindex_environment: searchMessage,
  search_reindex_index_unavailable: searchMessage,
  setup_store_unreadable: fixed("setup_store_unreadable"),
  sharing_api_disabled: sharingMessage,
  sharing_disabled_for_account: sharingMessage,
  sharing_limited_to_groups: sharingMessage,
  sharing_no_common_group: sharingMessage,
  sharing_not_checked: fixed("sharing_not_checked"),
  sharing_permitted: fixed("sharing_permitted"),
  sharing_policy_check_timed_out: fixed("sharing_policy_check_timed_out"),
  sharing_policy_unreadable: fixed("sharing_policy_unreadable"),
  signaling_auth_failed: fixed("signaling_auth_failed"),
  signaling_backend_rejected: fixed("signaling_backend_rejected"),
  signaling_handshake_failed: fixed("signaling_handshake_failed"),
  signaling_mode_unknown: fixed("signaling_mode_unknown"),
  signaling_unreachable: fixed("signaling_unreachable"),
  storage_admission_blocked: fixed("storage_admission_blocked"),
  storage_incomplete: fixed("storage_incomplete"),
  storage_not_checked: fixed("storage_not_checked"),
  storage_not_probed: fixed("storage_not_probed"),
  storage_ready: fixed("storage_ready"),
  talk_backend_url_invalid: variants({
    app_api_proxy: () => copy().message.talk_backend_url_invalid_app_api_proxy({ variable: "CASSINI_TALK_BACKEND_URL" }),
    not_a_base_url: () => copy().message.talk_backend_url_invalid_not_a_base_url({ variable: "CASSINI_TALK_BACKEND_URL" }),
  }),
  talk_or_room_unavailable: fixed("talk_or_room_unavailable"),
  test_awaiting_playback: fixed("test_awaiting_playback"),
  test_failed: variants({
    stopped: fixed("test_failed"),
    stopped_at_stage: (p) => copy().message.test_failed_at_stage({ stage: p.str("stage") }),
  }),
  test_in_progress: variants({
    waiting_for_talk: fixed("test_waiting_for_talk"),
    in_progress: fixed("test_in_progress"),
    in_progress_at_stage: (p) => copy().message.test_in_progress_at_stage({ stage: p.str("stage") }),
  }),
  test_not_run: fixed("test_not_run"),
  test_playback: fixed("test_playback"),
  test_room_invalid: fixed("test_room_invalid"),
  test_room_unavailable: fixed("test_room_unavailable"),
  "tmpdir.writable": hostMessage("tmpdir"),
  workdir: hostMessage("workdir"),
};

// The blocking row by the panel's own name for it. A row the panel has no
// name for falls back to the operator's sentence.
function blockerName(p: Params, sentence: (name: string) => string): string | undefined {
  const blocker = p.str("blocker");
  return Object.hasOwn(checkLabels, blocker) ? sentence(checkLabels[blocker]) : undefined;
}

// Every id in doctorCatalog.json `steps`, and nothing else.
type StepWording = (p: Params) => string;
const s = () => copy().step;
const NEXTCLOUD_URL = code("NEXTCLOUD_URL");
const BACKEND_URL = code("CASSINI_TALK_BACKEND_URL");

export const stepWordings: Record<string, StepWording> = {
  allow_sharing_for_everyone: () => s().allow_sharing_for_everyone(),
  check_admin_account: () => s().check_admin_account(),
  check_affected_index_entries: () => s().check_affected_index_entries(),
  check_affected_transcripts: () => s().check_affected_transcripts(),
  check_data_volume_search: () => s().check_data_volume_search(),
  check_data_volume_secret: () => s().check_data_volume_secret(),
  check_firewall: () => s().check_firewall(),
  check_media_host: () => s().check_media_host(),
  check_nextcloud_listening: () => s().check_nextcloud_listening(),
  check_nextcloud_running: () => s().check_nextcloud_running(),
  check_proxy: () => s().check_proxy({ nextcloud_url: NEXTCLOUD_URL, backend_url: BACKEND_URL }),
  check_recordings_catalog: () => s().check_recordings_catalog(),
  check_search_index: () => s().check_search_index(),
  check_search_index_available: () => s().check_search_index_available(),
  check_transcription_settings: () => s().check_transcription_settings(),
  choose_allowed_groups: (p) => s().choose_allowed_groups({ account: code(p.str("account")) }),
  create_directory: (p) => s().create_directory({ path: p.str("path") }),
  deploy_through_appapi: () => s().deploy_through_appapi({ variables: ["NEXTCLOUD_URL", "APP_SECRET", "APP_ID"].map(code).join(", ") }),
  enable_group_sharing: () => s().enable_group_sharing(),
  enable_share_api: () => s().enable_share_api(),
  enable_talk: () => s().enable_talk(),
  find_standalone_internal_secret: () => s().find_standalone_internal_secret({ key: code("internalsecret"), section: code("[clients]") }),
  fix_storage_first: () => s().fix_storage_first(),
  fix_tmpdir: () => s().fix_tmpdir({ variable: "TMPDIR" }),
  fix_workdir_permissions: () => s().fix_workdir_permissions(),
  join_allowed_group: (p) => s().join_allowed_group({ account: code(p.str("account")), groups: quotedGroups(p.list("groups")) }),
  join_common_group: (p) => s().join_common_group({ account: code(p.str("account")) }),
  join_every_group: (p) => s().join_every_group({ account: code(p.str("account")) }),
  leave_excluded_groups: (p) => s().leave_excluded_groups({ account: code(p.str("account")), groups: quotedGroups(p.list("groups")) }),
  lift_group_restriction: () => s().lift_group_restriction(),
  match_certificate_name: () => s().match_certificate_name(),
  nextcloud_address: () => s().nextcloud_address({ nextcloud_url: NEXTCLOUD_URL, backend_url: BACKEND_URL }),
  paste_secret_unchanged: () => s().paste_secret_unchanged(),
  print_aio_internal_secret: () => s().print_aio_internal_secret(),
  read_nextcloud_log: () => s().read_nextcloud_log({ file: "nextcloud.log" }),
  read_search_backfill_log: () => s().read_search_backfill_log(),
  recheck_slow_nextcloud: () => s().recheck_slow_nextcloud(),
  recheck_storage_to_list_archive: () => s().recheck_storage_to_list_archive(),
  reenable_app: () => s().reenable_app(),
  reindex_adds: (p) => s().reindex_adds({ count: p.num("count") }),
  replace_with_directory: (p) => s().replace_with_directory({ path: p.str("path") }),
  restart_app: () => s().restart_app(),
  restart_app_new_secret: () => s().restart_app_new_secret(),
  restore_setup_store: () => s().restore_setup_store({ file: "recording-setup.json" }),
  retry_reindex: () => s().retry_reindex(),
  set_recording_secret: () => s().set_recording_secret({ variable: code("CASSINI_TALK_RECORDING_SECRET") }),
  stop_ignoring_groups: (p) => s().stop_ignoring_groups({ groups: quotedGroups(p.list("groups")) }),
  test_nextcloud_address: () => s().test_nextcloud_address(),
  unexclude_groups: (p) => s().unexclude_groups({ groups: quotedGroups(p.list("groups")) }),
  use_trusted_certificate: () => s().use_trusted_certificate({ backend_url: BACKEND_URL }),
  wait_for_nextcloud: () => s().wait_for_nextcloud(),
};

// Every code in doctorCatalog.json `blocked_reasons`, and nothing else.
const FIELD = "recordedAtUtc";
export const blockedReasonWordings: Record<string, StepWording> = {
  queued_instant_invalid: () => copy().blocked_reason.queued_instant_invalid({ field: FIELD }),
  recording_missing: () => copy().blocked_reason.recording_missing(),
  cli_too_old: () => copy().blocked_reason.cli_too_old({ command: "cassini", field: FIELD }),
  file_instant_invalid: () => copy().blocked_reason.file_instant_invalid({ field: FIELD }),
  recording_too_large: () => copy().blocked_reason.recording_too_large(),
  file_id_changed: (p) => copy().blocked_reason.file_id_changed({ from: p.str("from"), to: p.str("to") }),
};

function worded(word: () => string | undefined, english: string): string {
  try {
    return word() ?? english;
  } catch (error) {
    if (error instanceof MissingParam) return english;
    throw error;
  }
}

// checkMessage is the row's message in the reader's language, or the
// operator's English where it cannot be worded.
export function checkMessage(check: ReadinessCheck): string {
  const wording = Object.hasOwn(messageWordings, check.code) ? messageWordings[check.code] : undefined;
  return wording ? worded(() => wording(new Params(check.params), check), check.message) : check.message;
}

// stepLabel is a remedy step's label in the reader's language, or the
// operator's English. Backticks still mark what the panel shows as code.
export function stepLabel(step: ReadinessStep): string {
  const wording = step.id && Object.hasOwn(stepWordings, step.id) ? stepWordings[step.id] : undefined;
  return wording ? worded(() => wording(new Params(step.params)), step.label) : step.label;
}

// blockedReason is why a recording could not be rewritten, in the reader's
// language, or the operator's English (always, for a file an older operator
// blocked).
export function blockedReason(item: RecordedAtBlockedFile): string {
  const code = item.reason_code;
  const wording = code && Object.hasOwn(blockedReasonWordings, code) ? blockedReasonWordings[code] : undefined;
  return wording ? worded(() => wording(new Params(item.reason_params)), item.reason) : item.reason;
}

// Every variant some wording above picks by, for doctorText.test.ts.
export const wordedVariants = (): string[] => [...variantKeys];

// A refusal from the operator's routes, by its error_code; every code in
// doctorCatalog.json `errors`.
export const errorWordings: Record<string, () => string> = {
  migration_not_applicable: () => copy().error.migration_not_applicable(),
  migration_still_counting: () => copy().error.migration_still_counting(),
  reindex_unavailable: () => copy().error.reindex_unavailable(),
  secret_managed_by_deployment: () => copy().error.secret_managed_by_deployment(),
  setup_save_failed: () => copy().error.setup_save_failed(),
  setup_store_needs_restore: () => copy().error.setup_store_needs_restore(),
  test_not_published: () => copy().error.test_not_published(),
  test_room_create_failed: () => copy().error.test_room_create_failed(),
  test_room_url_invalid: () => copy().error.test_room_url_invalid({ variable: "NEXTCLOUD_URL" }),
};

// operatorErrorText is what to show for a failed operator call: the refusal
// worded in the reader's language when the operator sent an error_code this
// build knows, otherwise the error's own text (raw errors stay English).
export function operatorErrorText(error: unknown): string {
  const details = (error as { details?: unknown } | null)?.details;
  const code = details && typeof details === "object" ? (details as Record<string, unknown>).error_code : undefined;
  if (typeof code === "string" && Object.hasOwn(errorWordings, code)) return errorWordings[code]();
  return error instanceof Error ? error.message : String(error);
}
