// The Nextcloud shell around everything: navigation, first-run, access notices, and errors from the app's own clients.
//
// Keys are grouped by component or module, in snake_case. See docs/translations.md.
const shell = {
  // The shell's own chrome: the tabs, and the notices above them.
  app: {
    // Label of the navigation holding the two tabs below.
    nav_label: "Cassini surfaces",
    // Tab: the meeting list and meetings.
    tab_browse: "Browse",
    recording_needs_action: "Recording setup needs an administrator’s attention.",
    open_recording_setup: "Open recording setup",
    retention_load_failed: "Could not load retention settings.",
    // Shown above the retention reminder. {detail} is the server's error, in English.
    retention_dismiss_failed: "Could not dismiss the reminder: {detail}",
  },
  // A settings section that could not be read from Cassini.
  load_error: {
    title: "Cassini couldn't read these settings",
    summary_default: "Try again in a moment. If it still won't load, share the technical details below with Cassini support.",
    summary_unauthorized: "Sign in to Nextcloud again, then try again.",
    summary_forbidden: "Nextcloud denied access. Make sure you're signed in with an administrator account, then try again.",
    summary_not_found: "Cassini's storage settings couldn't be found. If you just updated Cassini, check that the update has finished, then try again.",
    summary_server_error: "Cassini couldn't respond right now. Wait a moment and try again.",
    summary_unreachable: "Cassini couldn't be reached. Check your connection and try again.",
  },
  // Renaming a meeting room.
  room_names: {
    unreachable: "Could not reach Cassini. Your draft is still here.",
    room_gone: "This room is no longer available to you.",
    update_failed: "Could not update the room name.",
    invalid_response: "Cassini returned an invalid room name. Refresh before trying again.",
  },
  // A meeting opened from a link that the reader cannot open yet.
  meeting_access: {
    // When Nextcloud refused to accept the share and gave no reason.
    share_refused: "Nextcloud did not accept this share.",
  },
  // Importing meeting files: uploading into the reader's Files, and the imports requests.
  imports_client: {
    no_session: "Import needs a Nextcloud session, and this page has none.",
    // {status} is an HTTP status code, e.g. 500.
    staging_failed: "Could not prepare the staging folder (HTTP {status}).",
    register_failed: "Could not register the upload.",
    upload_failed: "The upload failed — check the connection and try again.",
    upload_interrupted: "The upload was interrupted.",
    not_enough_space: "Not enough space in your Nextcloud Files for this upload.",
    // {status} is an HTTP status code.
    upload_refused: "The upload was refused (HTTP {status}).",
    upload_unregistered: "This upload is no longer registered with Cassini; add the file again.",
    upload_unanswered: "Cassini stopped answering during the upload, so it was interrupted; add the file again.",
    imports_not_served: "This deployment does not serve meeting imports.",
    identity_unverified: "Cassini could not verify who you are; reload and try again.",
    // {status} is an HTTP status code.
    request_failed: "The import request failed (HTTP {status}).",
    // Searching for people to share an import with. {status} is an HTTP status code.
    people_search_failed: "People search failed (HTTP {status}).",
  },
  // Preparing a bundle of meetings as context for an AI assistant.
  data_provider: {
    bundle_needs_meetings: "Pick at least one meeting first.",
    bundle_bad_request: "Cassini could not read that request for a bundle.",
    bundle_not_found: "One of these meetings is not available to you, or this deployment cannot assemble bundles.",
    bundle_nextcloud_unreadable: "Cassini could not read these meetings from Nextcloud.",
    // {status} is an HTTP status code.
    bundle_failed: "Could not prepare the bundle (HTTP {status}).",
  },
  // SetupNotice.svelte and buildSetupNotice (operator/setupHealth.ts): the
  // strip above Browse's meeting list when recordings cannot be saved.
  setup_notice: {
    checking: "Checking…",
    show_details: "Show details",
    details_heading: "Details for administrators",
    // A link. {place} is the path to a settings page, "Operator › Publish pipeline".
    open_publish_pipeline: "Open {place}",
    title_failing: "Cassini can't save recordings right now",
    title_unchecked: "Cassini hasn't checked that it can save recordings",
    summary_failing: "You can still browse recordings. Calls will still run, but their recordings will fail until this is fixed.",
    summary_unchecked: "You can still browse recordings, but new ones will fail until this check runs.",
    // Shown to someone who is not an administrator, above the app's address.
    share_label: "Send this link to an administrator for setup instructions.",
    step_rerun_setup: "Re-run Cassini's setup by disabling and enabling the app",
    // {place}: as open_publish_pipeline.
    step_open_settings: "Open {place} to check the recordings account",
    // {user}: the account name, "cassini".
    step_create_account: "Create the {user} account in Nextcloud",
    // {variable}: an environment variable name, CASSINI_NC_ADMIN_USER.
    step_set_admin_user: "Set {variable} to a Nextcloud administrator",
    cause_unknown: "Cassini could not confirm its recordings setup.",
    // {state} and {step}: the operator's internal names for the failing check,
    // e.g. "unavailable" at "owner_account". Shown to administrators only.
    detail_state: "The operator reported {state}.",
    detail_state_at_step: "The operator reported {state} at {step}.",
    // {command}: "occ". {example}: "sudo -u www-data php occ".
    note_occ: "{command} is however your deployment invokes it, for example {example}.",
    // {endpoint}: "GET /operator/status".
    reference: "See {endpoint} for the full recordings access report.",
  },
  // SharingNotice.svelte and operator/sharingNotice.ts: what Browse says about
  // the reader's own recording shares, waiting for them to accept or refused
  // by a Nextcloud sharing setting. Also used by a meeting opened from a link
  // that the reader cannot open yet.
  sharing_notice: {
    pending_title_one: "1 recording is waiting for you",
    pending_title_other: "{count} recordings are waiting for you",
    pending_label: "Recordings waiting for you",
    pending_line: "Nextcloud holds files shared with you until you accept them.",
    // A link that opens or closes the list under it.
    show_them: "Show them",
    hide_them: "Hide them",
    accept: "Accept",
    accept_all: "Accept all {count}",
    // {title}: the recording's title, or its date.
    accept_row_aria: "Accept {title}",
    why_accept:
      "Why? Your account accepts shared files by hand (Personal settings › Sharing), or this server asks everyone to. Cassini never accepts for you.",
    accept_refused: "Nextcloud did not accept this share.",
    // {status}: an HTTP status code.
    accept_failed: "Could not accept the share (HTTP {status}).",
    dismiss_failed: "Could not dismiss the notice (HTTP {status}).",
    refused_title_one: "1 recording could not be shared with you.",
    refused_title_other: "{count} recordings could not be shared with you.",
    refused_line: "A Nextcloud sharing setting stops it.",
    refused_group_label: "Recordings that could not be shared with you",
    starter_refused_title_one: "1 recording you started could not be published.",
    starter_refused_title_other: "{count} recordings you started could not be published.",
    starter_refused_line_one: "Nextcloud refused to share it with you.",
    starter_refused_line_other: "Nextcloud refused to share them with you.",
    unpublished_group_label: "Recordings you started that could not be published",
    // One refused recording, in bold. {name}: the Talk room (or the meeting's
    // title). {date}: e.g. "Fri 30 Oct, 10:00".
    refused_notice: {
      room_date: "The recording in “{name}” on {date} could not be shared with you.",
      room: "The recording in “{name}” could not be shared with you.",
      date: "The recording on {date} could not be shared with you.",
      unknown: "A recording could not be shared with you.",
    },
    // One recording the reader started that was not published.
    unpublished_notice: {
      room_date: "Your recording in “{name}” on {date} could not be published.",
      room: "Your recording in “{name}” could not be published.",
      date: "Your recording on {date} could not be published.",
      unknown: "A recording you started could not be published.",
    },
    // A meeting opened from a link, when only its id is known.
    refused_this: "This recording could not be shared with you.",
    unpublished_this: "This recording you started could not be published.",
    waiting_title: "This recording is waiting for you",
    // Stands in for a recording with no title and no date.
    a_recording: "A recording",
    dismiss_all: "Dismiss all",
    // {label}: the room or title, and the date.
    dismiss_row_aria: "Dismiss {label}",
    // {title}: the notice's sentence.
    dismiss_notice_aria: "Dismiss: {title}",
    // {link} becomes a link whose text is send_link; it opens the app.
    admin_fix: "An administrator can fix that: {link}.",
    send_link: "send them this link",
    send_link_title: "Opening it as an administrator shows exactly what is stopping it",
    // When there is no address to hand on.
    admin_fix_plain: "An administrator can fix that.",
  },
};

export default shell;
