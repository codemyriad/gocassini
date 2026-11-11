// What Doctor (Operator › Doctor) says on each check row, and the steps under
// it: the operator's own English, worded again here by the code it sends
// (D-921). cassini-app/src/operator/doctorText.ts picks the key.
//
// Row names in quotes or capitals, like “Recording storage” or the High
// Performance Backend, are the names of other rows (operator.readiness.check);
// keep them the same. Nextcloud's settings, in “quotes”, are named as
// Nextcloud's own settings pages name them: use Nextcloud's translation. A
// {placeholder} shown as code (a variable, a file, an account) stays as it is.
const doctor_checks = {
  // A check row's message, by the check's code. Where one code has several
  // sentences, the key ends in which one.
  message: {
    // A row that waits on another. {blocker}: that row's name, e.g. “Recording storage”.
    check_blocked_unchecked:
      "Not checked: this depends on {blocker}, which has not been checked yet. Run all checks first.",
    // {blocker}: the name of the row this one waits on.
    check_blocked_needs_attention: "Not checked: this depends on {blocker}, which needs attention first.",
    connection_not_checked: "The Talk connection has not been checked yet.",
    host_checks_unavailable: "Cassini could not check disk space and ffmpeg on the recording volume.",
    host_not_checked: "Not checked yet.",
    hpb_authenticated:
      "The signaling server accepted Cassini and advertises media support. A test recording verifies the actual call path.",
    hpb_declared_unverified:
      "Talk names a High Performance Backend. Whether Cassini can authenticate to it is what the Talk connection check below establishes.",
    hpb_declared_unverified_secret_from_env:
      "Talk names a High Performance Backend, and the internal secret comes from Cassini's deployment configuration. Whether that secret is the right one is what this check establishes.",
    hpb_declared_unverified_secret_saved:
      "Talk names a High Performance Backend and an internal secret is saved. Whether Cassini can authenticate with it is what this check establishes.",
    hpb_disabled:
      "Recording cannot work until Talk has a High Performance Backend. Cassini records by joining the call as a hidden participant, and Talk only allows that through standalone signaling — here it is signalling by itself. Nothing else about Talk is affected.",
    hpb_missing: "Talk has no standalone signaling server configured. Enable its high-performance backend.",
    hpb_not_checked:
      "Whether Talk has a High Performance Backend has not been established yet, and Cassini can only record through one.",
    hpb_unsupported: "The signaling server does not advertise HPB media support.",
    // {setting}: the signaling server's setting name, shown as is.
    internal_clients_disabled: "HPB does not accept internal clients. Ask its administrator to configure {setting}.",
    // Talk has a backend and Cassini has no secret for it.
    internal_secret_missing:
      "Talk has a High Performance Backend, and Cassini needs that server's internal secret to join calls invisibly. This is not a Nextcloud setting: it belongs to the signaling server, which is why Cassini cannot read it for you.",
    // The same, before the operator has looked at the saved secret.
    internal_secret_missing_unverified:
      "Talk has standalone signaling configured. Supply its internal client secret to verify HPB authentication.",
    nextcloud_connection_refused:
      "Nothing answered at the Nextcloud address Cassini was given: the connection was refused.",
    nextcloud_host_not_found:
      "Cassini could not find Nextcloud at the address it was given: the name does not resolve from Cassini's container.",
    nextcloud_server_error: "Nextcloud returned a server error while Cassini was reading Talk's settings.",
    nextcloud_timeout: "Nextcloud did not answer in time at the address Cassini was given.",
    nextcloud_tls_untrusted:
      "Cassini reached Nextcloud but does not trust its certificate, so it did not read Talk's settings.",
    nextcloud_unexpected_response: "Something answered at the Nextcloud address, but not with Talk's settings.",
    nextcloud_unreachable: "Could not read Talk settings. Check Nextcloud connectivity and TLS, then try again.",
    probe_failed:
      "The connection check did not finish. Check the recorder installation and connectivity, then retry.",
    recording_auth_rejected:
      "Talk refused Cassini. Cassini is not set up as Talk's recording backend yet, or the recording secret Talk has does not match Cassini's.",
    recording_auth_verified: "Talk accepted Cassini's recording credential.",
    // On the Recording credential row.
    recording_secret_missing:
      "Cassini has no recording secret. It creates one when it starts and keeps it on its data volume, and that did not succeed.",
    // On the Talk connection row.
    recording_secret_missing_connection: "Configure Cassini's recording credential and connect Talk first.",
    setup_store_unreadable:
      "Cassini could not read its saved recording setup, so its Talk credentials cannot be confirmed.",
    sharing_not_checked: "Nextcloud's sharing settings have not been checked yet.",
    sharing_permitted:
      "Nextcloud's sharing settings let Cassini share recordings with everyone in a meeting. Whether each person has accepted the share is theirs to decide; anyone with recordings waiting sees them in Browse.",
    sharing_policy_check_timed_out:
      "Nextcloud did not answer in time when Cassini read its sharing settings, so whether they let it share recordings is not known.",
    sharing_policy_unreadable:
      "Cassini could not read Nextcloud's sharing settings, so whether they let it share recordings is not known.",
    signaling_auth_failed:
      "HPB rejected internal-client authentication. Check the internal secret and server authentication configuration.",
    signaling_backend_rejected:
      "The signaling server does not recognise this Nextcloud as one of its backends. Check its backend configuration and the URL Cassini reaches Nextcloud on.",
    signaling_handshake_failed: "The signaling handshake did not finish. Check the server connection and try again.",
    signaling_mode_unknown:
      "Cassini could not read Talk's signaling configuration, so it cannot tell whether a High Performance Backend is available.",
    signaling_unreachable: "The configured signaling server could not be reached. Check its network route and TLS.",
    storage_admission_blocked:
      "Cassini currently blocks recording on its stored storage status. Review the storage details below and check again after repairing them.",
    storage_incomplete:
      "Cassini cannot store or share recordings in Nextcloud yet. The storage details name the step that failed.",
    storage_not_checked: "Nextcloud storage has not been checked yet.",
    storage_not_probed:
      "The Nextcloud storage check does not apply to this publish destination. Verify storage with a test recording.",
    storage_ready:
      "Cassini can store recordings in Nextcloud: its own account exists, its recordings folder is writable, and Nextcloud's sharing API answers. Whether its shares are accepted is the Recording sharing check.",
    // {variable}: an environment variable name.
    talk_backend_url_invalid_app_api_proxy:
      "Cassini's configured Talk backend URL addresses Cassini's own AppAPI proxy, so requests for Talk's settings never reach Talk. Clear {variable} unless a deployment genuinely needs an override; Talk sends the correct backend URL with each recording request.",
    // {variable}: an environment variable name.
    talk_backend_url_invalid_not_a_base_url:
      "Cassini's configured Talk backend URL is not a usable base URL. It must be a plain http(s) URL with no query or fragment. Clear {variable} unless a deployment genuinely needs an override.",
    talk_or_room_unavailable:
      "Talk's recording settings are unavailable: either the Talk app is not enabled, or the conversation Cassini checks with is gone. Check the Talk app, then run this check again — Cassini will make a new conversation if it needs one.",
    test_awaiting_playback: "The test recording was published. Playing it is what confirms the audio arrived.",
    test_failed: "The test recording did not finish.",
    // {stage}: the operator's internal name for the step, e.g. "build", in English.
    test_failed_at_stage: "The test recording did not finish; it stopped at {stage}.",
    test_waiting_for_talk: "Cassini is waiting for a recording started in the test room.",
    test_in_progress: "A test recording is in progress.",
    // {stage}: the operator's internal name for the step, e.g. "build", in English.
    test_in_progress_at_stage: "A test recording is in progress: {stage}.",
    test_not_run:
      "Nothing has been recorded through Talk yet. A short test recording is what proves the whole path, from a call to audio you can play.",
    test_playback: "A recording started in Talk was published, and its audio was confirmed by playing it.",
    test_room_invalid:
      "Cassini's check conversation could not be resolved. Run this check again and Cassini will make a new one.",
    test_room_unavailable:
      "Cassini could not create the conversation it checks the connection with. Check that Talk is installed and that Cassini's account may create conversations.",
  },
  // The Recording volume and Temporary space rows: what the host check found,
  // written as short notes. {path}: a directory on the server. {error}: the
  // system's own error, in English.
  host: {
    workdir_found: "working directory {path}",
    workdir_unavailable: "working directory unavailable: {error}",
    workdir_writable: "working directory writable: {path}",
    workdir_not_accessible: "working directory not accessible: {error}",
    workdir_not_a_directory: "working directory is not a directory: {path}",
    workdir_not_writable: "working directory is not writable: {path} ({error})",
    tmpdir_writable: "temporary directory writable: {path}",
    tmpdir_not_accessible: "temporary directory not accessible: {error}",
    tmpdir_not_a_directory: "temporary directory is not a directory: {path}",
    tmpdir_not_writable: "temporary directory is not writable: {path} ({error})",
  },
  // The Recording sharing row: one sentence for each sharing setting in the
  // way, in this order, joined by a space. {account}: Cassini's own Nextcloud
  // account name ("cassini"). {groups}: Nextcloud group names, each in the
  // quotes of `group`, separated by commas.
  sharing: {
    // One group name. {group}: the name; change the quotes to your language's.
    group: "“{group}”",
    share_api_off:
      "Nobody can receive recordings. Nextcloud's “Allow apps to use the Share API” is off, so every share Cassini makes is refused and no new meetings can be published.",
    // Plural; see the translator guide. {count}: the number of groups listed, not shown.
    excluded_groups:
      "Nobody can receive recordings. Nextcloud's “Exclude some groups from sharing” lists {groups}, and Cassini shares from its {account} account, which is in {{count:that group|those groups}}. Every share is refused, so no new meetings can be published.",
    no_allowed_group:
      "Nobody can receive recordings. Nextcloud's “Limit sharing to some groups” allows no group at all, so no account may share. Every share is refused, so no new meetings can be published.",
    not_in_allowed_groups:
      "Nobody can receive recordings. Nextcloud's “Limit sharing to some groups” allows only {groups}, and Cassini shares from its {account} account, which is in none of them. Every share is refused, so no new meetings can be published.",
    in_no_group:
      "Nobody can receive recordings. Nextcloud's “Restrict users to only share with users in their groups” is on, and Cassini shares from its {account} account, which is in no group. Every share is refused, so no new meetings can be published.",
    groups_ignored:
      "Nobody can receive recordings. Nextcloud's “Restrict users to only share with users in their groups” is on, and every group the {account} account is in ({groups}) is ignored when Nextcloud checks membership. Every share is refused, so no new meetings can be published.",
    only_groups:
      "Only members of {groups} can receive recordings. Nextcloud's “Restrict users to only share with users in their groups” is on, and Cassini shares from its {account} account, which is only in {groups}. Anyone outside these groups cannot open the recordings of their meetings, and a recording they start is not published at all.",
    // {groups}: the administrators' group's name.
    only_admins:
      "Only administrators can receive recordings. Nextcloud's “Restrict users to only share with users in their groups” is on, and Cassini shares from its {account} account, which is only in {groups}, the administrators' group. Anyone who is not an administrator cannot open the recordings of their meetings, and a recording they start is not published at all.",
    group_sharing_off:
      "Groups in a room do not receive recordings. Nextcloud's “Allow sharing with groups” is off, so a meeting whose room includes a group is published without access for that group's members, unless they are in the room themselves.",
  },
  // The Archive search row: a first sentence, then sometimes one about the
  // last re-index, joined by a space.
  search: {
    index_unavailable: "The search index is unavailable, so archive search coverage is unknown.",
    inventory_unreadable: "Cassini could not read the archive inventory, so search coverage is unknown.",
    index_unreadable: "Cassini could not read the search index, so archive search coverage is unknown.",
    nothing_recorded: "No meetings have been recorded yet, so search has nothing to index.",
    archive_empty: "The recordings archive is empty, so search has nothing to index.",
    not_checked: "Archive search coverage has not been checked yet.",
    // After the count of searchable meetings.
    not_listed: "Cassini has not listed the archive yet, so older recordings may not be counted.",
    // After the count of searchable meetings.
    all_accounted: "Every meeting file in the archive is accounted for.",
    none_searchable: "No meetings are searchable yet.",
    // {reasons}: a list of the phrases in `reason`, e.g. "2 were silent and 1 failed to transcribe".
    none_searchable_because: "No meetings are searchable yet: {reasons}.",
    // Plural; see the translator guide. {count}: meetings.
    searchable: "{count:number|num} {{meeting is|meetings are}} searchable.",
    // After `searchable`. {reasons}: as in none_searchable_because.
    others: "Of the others, {reasons}.",
    // Why a meeting is not searchable, as items of a list. Each starts with
    // how many meetings. Plural; see the translator guide. {count}: meetings.
    reason: {
      // Plural; see the translator guide. {count}: meetings.
      silent: "{count:number|num} {{was silent|were silent}}",
      // Plural; see the translator guide. {count}: meetings.
      disabled: "{count:number|num} {{was recorded with transcription off|were recorded with transcription off}}",
      // Plural; see the translator guide. {count}: meetings.
      model_unavailable:
        "{count:number|num} {{was not transcribed because a model was unavailable|were not transcribed because a model was unavailable}}",
      // Plural; see the translator guide. {count}: meetings.
      transcription_failed: "{count:number|num} {{failed to transcribe|failed to transcribe}}",
      // Plural; see the translator guide. {count}: meetings.
      unknown_empty:
        "{count:number|num} {{has an empty transcript for an unknown reason|have empty transcripts for an unknown reason}}",
      // Plural; see the translator guide. {count}: meetings.
      missing_transcript: "{count:number|num} {{is missing its transcript|are missing their transcripts}}",
      // Plural; see the translator guide. {count}: meetings.
      unreadable_transcript: "{count:number|num} {{has an unreadable transcript|have unreadable transcripts}}",
      // Plural; see the translator guide. {count}: meetings.
      backfill_candidates:
        "{count:number|num} {{is waiting to be verified for search|are waiting to be verified for search}}",
      // Plural; see the translator guide. {count}: meetings.
      other_unavailable:
        "{count:number|num} {{could not be indexed for another reason|could not be indexed for other reasons}}",
      // Plural; see the translator guide. {count}: meetings.
      untracked: "{count:number|num} {{is in the archive but not indexed yet|are in the archive but not indexed yet}}",
    },
    // The sentence about the last re-index.
    reindex: {
      running: "Re-indexing is running now.",
      // Each {…}: a number of meetings.
      finished:
        "The last re-index added {added:number|num}, left {unchanged:number|num} unchanged, found {unsearchable:number|num} not searchable and failed on {failed:number|num}.",
      archive_unreadable: "The last re-index could not list the recordings archive in Nextcloud.",
      index_unavailable:
        "The last re-index could not use the search index, so trying again would fail the same way.",
      environment:
        "The last re-index could not read the settings AppAPI gives Cassini, so trying again would fail the same way.",
      // {error}: the error, in English.
      failed: "The last re-index did not finish: {error}",
    },
  },
  // The steps under a row ("What to do about it"), by the step's id. Most
  // have no full stop, as the operator writes them. Some are followed by a
  // command. {account}, {groups}, {variable}, {file}, {path}: shown as code.
  step: {
    allow_sharing_for_everyone: "Or allow sharing for everyone under Administration settings › Sharing.",
    check_admin_account:
      "Cassini reads them as the Nextcloud administrator it acts as. Check that Nextcloud is reachable and that account is still an administrator, then check again",
    check_affected_index_entries: "Check the affected recordings and their entries in the search index",
    check_affected_transcripts:
      "Check the affected recordings' transcripts. A missing or unreadable transcript has to be fixed before indexing can help",
    check_data_volume_search: "Check that Cassini's data volume is mounted and writable: the search index is kept on it",
    check_data_volume_secret:
      "Check that Cassini's data volume is mounted and writable: the secret is saved next to its database",
    check_firewall: "Check that no firewall between Cassini's container and Nextcloud drops the connection",
    check_media_host: "Check the recorder's media tools and its recording volume, then run the checks again",
    check_nextcloud_listening: "Check that Nextcloud is running and listening on that address and port",
    check_nextcloud_running: "Check that Nextcloud is running, and is not overloaded or part-way through a restart",
    // {nextcloud_url} and {backend_url}: environment variable names.
    check_proxy:
      "A proxy may be answering in Nextcloud's place. Make sure {nextcloud_url}, or {backend_url} when set, points at Nextcloud itself",
    check_recordings_catalog: "Check the recordings catalog and try the archive check again",
    check_search_index: "Check the search index database and try again",
    check_search_index_available: "Check that Cassini's search index database is available, then check again",
    check_transcription_settings:
      "Check transcription settings and that the model is ready. Indexing again cannot add words to recordings that were never transcribed",
    choose_allowed_groups:
      "Choose the groups allowed to share under Administration settings › Sharing, including one {account} is in.",
    // A note from the host check; starts in lower case. {path}: a directory.
    create_directory: "create {path} or choose a path you can write to",
    // {variables}: environment variable names.
    deploy_through_appapi:
      "Cassini is running without its AppAPI settings ({variables}). Deploy it through AppAPI rather than starting its container by hand",
    enable_group_sharing: "Turn on “Allow sharing with groups” under Administration settings › Sharing.",
    enable_share_api: "Turn on “Allow apps to use the Share API” under Administration settings › Sharing.",
    enable_talk: "Confirm the Talk app is enabled, then run this check again",
    // {key} and {section}: the setting's name and its section in the signaling server's file.
    find_standalone_internal_secret:
      "On a standalone signaling server, it is {key} under {section} in its configuration file",
    fix_storage_first:
      "Recording storage uses the same access to the archive. If it needs attention, fix that first",
    // A note from the host check; starts in lower case. {variable}: an environment variable name.
    fix_tmpdir: "free space or permissions in the system temp directory, or run with {variable} set to a writable location",
    // A note from the host check; starts in lower case.
    fix_workdir_permissions:
      "change into a writable working directory or fix directory permissions before running Cassini",
    join_allowed_group: "Add {account} to one of {groups} on Nextcloud's Users page.",
    join_common_group:
      "Or add {account} to a group everyone who records is in. It does not need to be an administrator. Keep it there: removing it later makes Nextcloud remove people's access to recordings already shared through that group.",
    join_every_group:
      "Or add {account} to every group whose members should receive recordings. A group everyone is in covers everybody; {account} does not need to be an administrator. Keep it there: removing it later makes Nextcloud remove people's access to recordings already shared through that group.",
    leave_excluded_groups: "Or take {account} out of {groups} on Nextcloud's Users page.",
    lift_group_restriction:
      "Turn off “Restrict users to only share with users in their groups” under Administration settings › Sharing.",
    match_certificate_name: "The address Cassini uses must also match the name the certificate was issued for",
    // {nextcloud_url} and {backend_url}: environment variable names.
    nextcloud_address:
      "Cassini reaches Nextcloud at {nextcloud_url}, or at {backend_url} when that is set. The address has to work from inside Cassini's container, not only from your browser",
    paste_secret_unchanged:
      "Paste it unchanged — one differing character fails exactly as a wrong credential would, and nothing can tell the difference until this check runs",
    // Followed by a command.
    print_aio_internal_secret: "On Nextcloud All-in-One, print it with:",
    // {file}: Nextcloud's log file name.
    read_nextcloud_log:
      "Nextcloud's log says what failed: Administration settings → Logging, or {file} on the server",
    // Followed by a command.
    read_search_backfill_log: "The full error is in Cassini's log. On the Docker host:",
    recheck_slow_nextcloud:
      "Check again. If it keeps running out of time, Nextcloud is answering slowly: the Recording storage check above uses the same connection",
    recheck_storage_to_list_archive:
      "Check Recording storage again so Cassini can list the archive, then check search again",
    reenable_app: "Disable and re-enable Cassini in Nextcloud, which re-runs its setup",
    // Plural; see the translator guide. {count}: recordings. “Re-index now” is the button beside it (operator.readiness.repair.backfill_search).
    reindex_adds: "Re-index now adds the {count:number|num} {{recording that is|recordings that are}} not in search yet",
    // A note from the host check; starts in lower case. {path}: a directory.
    replace_with_directory: "replace {path} with a writable directory",
    restart_app: "Then restart Cassini by disabling and re-enabling it in Nextcloud's apps",
    restart_app_new_secret:
      "Then restart Cassini by disabling and re-enabling it in Nextcloud's apps, which creates the secret again",
    // {file}: a file name.
    restore_setup_store:
      "Check that Cassini's persistent volume is mounted and writable, then restore {file} from a backup if it is missing",
    retry_reindex: "If it passes, Nextcloud was probably busy or restarting, so try re-indexing again",
    // {variable}: an environment variable name.
    set_recording_secret: "Or set one yourself in {variable}, in Cassini's deploy options. A secret set there always wins",
    stop_ignoring_groups:
      "Or stop ignoring {groups} under “Ignore the following groups when checking group membership”.",
    // Followed by a command.
    test_nextcloud_address:
      "To test the address, run this on the Nextcloud host. It should print an HTTP status such as 200, not an error:",
    unexclude_groups: "Remove {groups} from the excluded groups under Administration settings › Sharing.",
    // {backend_url}: an environment variable name.
    use_trusted_certificate:
      "A self-signed or private certificate is not trusted from inside Cassini's container. Use a publicly trusted certificate on Nextcloud, or point {backend_url} at an address whose certificate Cassini trusts",
    wait_for_nextcloud: "Nextcloud may be in maintenance mode or still starting. Wait for it, then check again",
  },
  // Why a past recording could not have its recording time written into it
  // (Operator › Doctor, the recording-time migration), after the meeting's
  // name and a colon; starts in lower case. {field}: the name of the field
  // in the file, shown as is.
  blocked_reason: {
    queued_instant_invalid: "queued {field} is not a valid instant",
    recording_missing: "recording is missing; restore it and retry",
    // {command}: the command-line tool's name.
    cli_too_old: "the {command} CLI cannot set {field}; upgrade it and retry",
    file_instant_invalid: "the delivered file states an invalid {field}; repair it and retry",
    recording_too_large: "recording exceeds the staging limit",
    // {from} and {to}: Nextcloud's file ids, numbers.
    file_id_changed: "Nextcloud changed the file id from {from} to {to}; existing shares must be repaired",
  },
  // When Cassini refuses something on Doctor, shown under the heading: a
  // setup change, a test recording, a re-index, or the recording-time
  // migration's Write button.
  error: {
    // The recording-time migration's start; starts in lower case.
    migration_not_applicable: "this deployment has no delivered files Cassini can rewrite",
    migration_still_counting:
      "Cassini is still counting the past recordings that need this; try again once it has finished",
    reindex_unavailable: "Search re-indexing is available only for Nextcloud recording archives.",
    secret_managed_by_deployment: "The internal secret is managed by deployment configuration.",
    setup_save_failed: "Could not save recording setup on the persistent volume.",
    setup_store_needs_restore: "Restore the saved recording setup before making changes.",
    test_not_published: "The selected Talk test recording has not finished publishing.",
    test_room_create_failed:
      "Cassini could not create a test room in Talk. Check that Talk is installed and reachable, then try again.",
    // {variable}: an environment variable name.
    test_room_url_invalid:
      "Use a Talk room URL without query parameters. Configure {variable} or the Talk backend URL on the server first.",
  },
};

export default doctor_checks;
