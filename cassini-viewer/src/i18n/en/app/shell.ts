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
};

export default shell;
