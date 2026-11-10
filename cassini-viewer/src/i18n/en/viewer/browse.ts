// The meeting list, rooms, search, selection, import, tags and the agent-connect panel.
//
// Keys are grouped by component or module, in snake_case. See docs/translations.md.
//
// Counts: a key ending in `_one` is shown for exactly one, `_other` for any
// other number ({count}). The code picks between them.
const browse = {
  // The viewer's frame (App.svelte): the page title, sheets and dialogs around the list.
  app: {
    // The browser tab's title while a meeting is open. {title} is the meeting's title.
    document_title_meeting: "{title} | Cassini Viewer",
    // The browser tab's title on the meeting list.
    document_title_list: "Cassini Meetings",
    // The browser tab's title before anything has loaded.
    document_title_default: "Cassini Viewer",
    meta_description: "Static audio and transcript viewer for transcript.words.v1 meeting artifacts.",
    // A link or the address named a meeting the list does not have. {id} is the meeting's id.
    meeting_not_found: "Meeting not found in catalog: {id}",
    no_context_bundle: "This build cannot assemble a context bundle.",
    search_failed: "Could not search the meetings.",
    // {title} is the meeting's title.
    delete_failed_one: "“{title}” could not be deleted.",
    delete_failed_other: "{count} meetings could not be deleted.",
    insight_rename_busy: "Another rename is still saving. Try again in a moment.",
    // Labels of the dimmed area beside a sheet; clicking it closes the sheet.
    close_room_list: "Close the room list",
    close_insight: "Close the insight",
    close_meeting: "Close the meeting",
    close_prepare: "Close Prepare",
    close_connect_agent: "Close Connect an agent",
    import_sheet_label: "Import meetings",
    // {title} is the meeting's title.
    delete_confirm_title_one: "Delete “{title}”?",
    delete_confirm_title_other: "Delete {count} meetings?",
    delete_confirm_body:
      "Deleting removes the recording, transcript and tags for everyone who can see it. This cannot be undone.",
    delete_confirm_button_one: "Delete meeting",
    delete_confirm_button_other: "Delete {count} meetings",
  },

  // The list of meetings and insights, with the search field above it.
  meeting_list: {
    list_label: "Meeting list",
    // Tooltip on a row's Imported badge. {name} is who imported it, {date} the day, written in the reader's locale ("14 Oct 2026").
    imported_by_on: "Imported by {name} on {date}",
    imported_by: "Imported by {name}",
    imported_on: "Imported on {date}",
    imported_here: "Imported into this archive",
    imported_badge: "Imported",
    // The search field's placeholder, by what the list is showing and what search can find.
    search_label: {
      meetings: {
        said_and_tags: "Search meetings, their tags and what was said in them",
        said: "Search meetings and what was said in them",
        name_date_tag: "Search meetings by name, date or tag",
        name_date: "Search meetings by name or date",
      },
      insights: {
        said_and_tags: "Search insights, their tags and what was said in them",
        said: "Search insights and what was said in them",
        name_date_tag: "Search insights by name, date or tag",
        name_date: "Search insights by name or date",
      },
      both: {
        said_and_tags: "Search meetings or insights, their tags and what was said in them",
        said: "Search meetings or insights and what was said in them",
        name_date_tag: "Search meetings or insights by name, date or tag",
        name_date: "Search meetings or insights by name or date",
      },
    },
    search_unavailable: "Search is unavailable right now.",
    choose_room: "Choose a room",
    // On the narrow-screen rooms button when no room is chosen.
    all_rooms_short: "All",
    // On the narrow-screen rooms button, after the room's name. {count} is how many tags filter the list.
    room_button_tags_one: "{room} · 1 tag",
    room_button_tags_other: "{room} · {count} tags",
    searching: "Searching…",
    clear_search: "Clear search",
    toggle_theme: "Toggle light or dark theme",
    finding_meetings: "Finding matching meetings…",
    // The result line while something narrows the list. {shown} of {total}.
    meetings_shown: "{shown} of {total} meetings",
    meetings_count_one: "1 meeting",
    meetings_count_other: "{count} meetings",
    insights_shown: "{shown} of {total} insights",
    insights_count_one: "1 insight",
    insights_count_other: "{count} insights",
    insights_failed: "Insights could not be listed.",
    show_every_room: "Show every room",
    tag_filter_count_one: "1 tag",
    tag_filter_count_other: "{count} tags",
    clear_tag_filters: "Clear tag filters",
    clear_filters: "Clear filters",
    retry_tag_update: "Retry tag update",
    retry_delete: "Retry delete",
    empty_title: "No meetings yet",
    empty_body: "Recordings you publish from Talk show up here.",
    // Followed by the "Import meetings" link.
    empty_import_lead: "Moving from another Cassini?",
    empty_import_link: "Import meetings",
    no_insights_title: "No insights yet",
    no_insights_body: "Pick some meetings, ask one question of them, and the answer is kept here beside them.",
    loading_insights: "Loading insights…",
    searching_meetings: "Searching meetings…",
    // {query} is what the person typed.
    searching_said: "Looking for “{query}” in what was said.",
    search_not_finished: "Search could not finish",
    nothing_matches: "Nothing matches",
    insights_have_no_tags: "Insights carry no tags, so a tag filter hides them.",
    // The search of what was said failed; only names and dates were checked.
    // `here`: a room or tag filter is also narrowing the list.
    names_dates_no_match: {
      meetings: "Names and dates match no meeting, and what was said could not be searched.",
      insights: "Names and dates match no insight, and what was said could not be searched.",
      both: "Names and dates match no meeting or insight, and what was said could not be searched.",
    },
    names_dates_no_match_here: {
      meetings: "Names and dates match no meeting here, and what was said could not be searched.",
      insights: "Names and dates match no insight here, and what was said could not be searched.",
      both: "Names and dates match no meeting or insight here, and what was said could not be searched.",
    },
    no_meeting_has_tag: "No meeting here has that tag.",
    no_meeting_has_tags: "No meeting here has those tags.",
    no_meeting_has_tag_and_search: "No meeting here has that tag and matches that search.",
    no_meeting_has_tags_and_search: "No meeting here has those tags and matches that search.",
    // {room} is the room's name.
    no_match_in_room: {
      meetings: "No meeting in {room} matches that search.",
      insights: "No insight in {room} matches that search.",
      both: "No meeting or insight in {room} matches that search.",
    },
    room_has_none: {
      meetings: "{room} has no meetings.",
      insights: "{room} has no insights.",
      both: "{room} has no meetings or insights.",
    },
    // {searched} of {total} meetings could be searched for their words.
    no_match_partial: {
      meetings: "No meeting matches that search — but only {searched} of {total} could be searched for what was said in them.",
      insights: "No insight matches that search — but only {searched} of {total} could be searched for what was said in them.",
      both: "No meeting or insight matches that search — but only {searched} of {total} could be searched for what was said in them.",
    },
    no_match: {
      meetings: "No meeting matches that search.",
      insights: "No insight matches that search.",
      both: "No meeting or insight matches that search.",
    },
    nothing_to_show: {
      meetings: "There are no meetings to show.",
      insights: "There are no insights to show.",
      both: "There are no meetings or insights to show.",
    },
    clear_tag_filter: "Clear tag filter",
    // {title} is the meeting's title.
    select_meeting: "Select {title}",
    // A badge on a row: the meeting has a transcript but no recording.
    transcription_only: "Transcription only",
    audio_included: "Audio included",
    speakers_one: "1 speaker",
    speakers_other: "{count} speakers",
    tag_meeting: "Tag {title}",
    tag_this_meeting: "Tag this meeting",
    delete_meeting: "Delete {title}",
    delete_this_meeting: "Delete this meeting",
    // A search hit with no text to quote.
    matched_here: "matched here",
    matched_alias: "Matched a known mistranscription",
    show_more: "Show more ({count} remaining)",
    load_note: "Load note",
    // Under "Load note" when the list could not be loaded or refreshed. The
    // technical reason follows underneath, in English.
    load_failed: "Couldn't load the meetings.",
  },

  // The rooms, tags and kinds down the side of the list.
  rooms_rail: {
    nav_label: "Rooms",
    rooms_heading: "Rooms",
    audience_participants: "Shared with participants",
    all_meetings: "All meetings",
    // {name} is the room's name.
    rename_room_named: "Rename room {name}",
    rename_room: "Rename room",
    tags_heading: "Tags",
    manage_tags: "Manage tags",
    filter_by_tag: "Filter by tag",
    no_tags: "No tags yet.",
    // Before the Any / All switch, shown once two or more tags are ticked.
    match: "Match",
    match_group: "Show meetings with",
    match_any: "any",
    match_all: "all",
    match_any_label: "any of the ticked tags",
    match_all_label: "all of the ticked tags",
    tags_unavailable: "Tags are unavailable right now.",
    show_heading: "Show",
    show_meetings: "Meetings",
    show_insights: "Insights",
    connect_agent: "Connect an agent",
  },

  // The bar that floats over the list once meetings are picked.
  selection_bar: {
    region_label: "Selected meetings",
    hide_selected: "Hide the selected meetings",
    show_selected: "Show the selected meetings",
    selected_one: "1 meeting selected",
    selected_other: "{count} meetings selected",
    clear_selection: "Clear selection",
    // {count} of the picked meetings are outside the current room or search.
    not_shown_count: "{count} not shown here.",
    what_next: "Tag, download or get insights from them.",
    // {max} is the most meetings one bundle can hold.
    over_cap_one: "You can work with up to {max} meetings at once. Unpick one.",
    over_cap_other: "You can work with up to {max} meetings at once. Unpick {count}.",
    retry_tag_update: "Retry tag update",
    tag: "Tag",
    prepare: "Prepare",
    item_not_shown: "Not shown here",
    unpick: "Unpick {title}",
    tag_meetings_one: "Tag 1 meeting",
    tag_meetings_other: "Tag {count} meetings",
    dropped_one: "1 selected meeting is no longer in the archive and was removed.",
    dropped_other: "{count} selected meetings are no longer in the archive and were removed.",
    dismiss_dropped: "Dismiss the removal notice",
  },

  // What the Prepare panel says about the picked meetings (selectionModel.ts).
  selection: {
    length_not_recorded: "Length not recorded",
    // {count} is already formatted ("12,405").
    words: "{count} words",
    at_least_words: "At least {count} words",
    over_cap_one: "You can work with up to {max} meetings at once. Unpick one.",
    over_cap_other: "You can work with up to {max} meetings at once. Unpick {count}.",
    no_portable_audio_one: "One of these predates the single-file format (marked in the list). Unpick it to prepare.",
    no_portable_audio_other: "{count} of these predate the single-file format (marked in the list). Unpick them to prepare.",
  },

  // Renaming a room.
  room_name: {
    title: "Rename room",
    field: "Room name",
    // {name} is the name the room has when nobody has renamed it.
    default_name: "Default: {name}",
    current_saved: "Current saved name: {name}",
    current_saved_review: "Current saved name: {name}. Review it, then save again.",
    // Added after the server's own message about a conflicting change.
    current_saved_draft_kept: "Current saved name: {name}. Your draft is unchanged.",
    checking: "Checking room details…",
    scope: "This name is shared with everyone who can access this room in Cassini.",
    use_default: "Use default",
    invalid_characters: "Use a name without control characters.",
    // {max} is the longest a name can be.
    invalid_length: "Use a name between 1 and {max} characters.",
  },

  // "Connect an agent": how to give an AI agent read access over MCP.
  connect_agent: {
    title: "Connect an agent",
    intro:
      "Claude Code, Codex or any MCP client can read the meetings you see here. It reads as you, and it can only read: it cannot mark, rename or delete anything.",
    close: "Close Connect an agent",
    address_heading: "Server address",
    copy_address: "Copy address",
    // Step 1. {title} is step_password_title in bold, {link} is step_password_link as a link.
    step_password: "{title} in {link}. Revoking it disconnects the agent.",
    step_password_title: "Create an app password",
    // Nextcloud's own menu path; use the words Nextcloud uses in this language.
    step_password_link: "Personal settings › Security",
    // Step 2. {title} is step_add_title in bold, {placeholder} is the text to replace in the command.
    step_add: "{title} For Claude Code, with your app password in place of {placeholder}:",
    step_add_title: "Add Cassini to your agent.",
    // Shown inside the command; the reader replaces it with their password.
    password_placeholder: "<app password>",
    // Step 3. {title} is step_ask_title in bold, {example} is step_ask_example in italics.
    step_ask: "{title} like {example}",
    step_ask_title: "Ask it something,",
    step_ask_example: "“What was decided in yesterday's standup?”",
    more: "Other agents and the command line",
  },

  // Importing meeting files.
  import: {
    // The Import button beside search, and what it says while imports run.
    button: "Import",
    button_running: "Importing {count}",
    button_done: "{count} imported",
    button_failed_one: "1 import failed",
    button_failed_other: "{count} imports failed",
    title: "Import meetings",
    close: "Close import",
    intro:
      "Add meetings downloaded from Cassini, here or on another Nextcloud: the recording (.opus), or just its transcript (.json).",
    drop_label: "Drop files to import",
    drop_here: "Drop files here",
    drop_more: "Drop more files",
    choose_files: "Choose files",
    note: "Speaker names, tags and the summary come with the file. You choose who can see each meeting before it is added.",
    share_all_with: "Share all with",
    share_all_placeholder: "A person, group or team",
    files_label: "Files to import",
    not_a_meeting: "Not a meeting file from Cassini, so it was skipped.",
    reading: "Reading…",
    // {percent} is a number from 0 to 100.
    uploading_percent: "Uploading {percent}%",
    shared: "Shared",
    only_me: "Only me",
    // {name} is a file's or a person's name.
    remove_named: "Remove {name}",
    add_sharee_placeholder: "Add a person, group or team",
    group_people: "People",
    group_groups: "Groups",
    // Nextcloud Teams (formerly Circles).
    group_teams: "Teams",
    you: "You",
    importer: "importer",
    recent_heading: "Recent imports",
    clear_finished: "Clear finished",
    details: "Details",
    // After the row's status, for a meeting with no recording.
    transcript_only_suffix: "transcript only",
    cancel_named: "Cancel {name}",
    remove_title: "Delete the temporary upload and remove this attempt.",
    open: "Open",
    dismiss_title: "Hide this row. It stays in the server's import history.",
    ready: "{count} ready",
    ready_skipped: "{count} ready · {skipped} skipped",
    importing: "Importing…",
    import_one: "Import 1 meeting",
    import_other: "Import {count} meetings",
    // What a running import says.
    running_uploading: "Uploading…",
    running_adding: "Adding…",
    // What a finished import says.
    settled_upload_failed: "Upload didn't finish",
    settled_server_failed: "Import on the server didn't finish",
    settled_failed: "Couldn't import this file",
    settled_staged: "Uploaded, not started",
    settled_canceled: "Canceled",
    settled_imported: "Imported",
    // Parts of a dropped file's summary line, joined with " · ".
    preview_transcript_only: "Transcript only",
    preview_speakers_one: "1 speaker",
    preview_speakers_other: "{count} speakers",
    preview_tags_one: "1 tag",
    preview_tags_other: "{count} tags",
    preview_summary: "summary",
    duration_under_minute: "under a minute",
    // Who can open an imported meeting.
    sharing_only_you: "Only you",
    sharing_only_you_title: "Only you can open this meeting.",
    // {count} people or groups, the importer included.
    sharing_shared_with: "Shared with {count}",
    // The first name in the list of who can open it.
    sharing_you: "you",
    // {name} is a Nextcloud group's name.
    sharee_group: "{name} (group)",
    sharee_team: "{name} (team)",
    // The badge beside a search result.
    kind_group: "group",
    kind_team: "team",
    sharee_placeholder: "Add a person or group…",
    sharee_search_failed: "Could not search people right now.",
    sharee_searching: "Searching…",
    sharee_none: "Nobody matches that.",
    file_empty: "The file is empty.",
    not_portable: "Not a portable meeting: neither an Ogg/Opus recording nor a transcription document.",
    untitled: "Untitled meeting",
  },

  // Tags: picking, the chip, and the Manage tags sheet.
  tags: {
    picker_label: "Tag",
    find_or_create: "Find or create a tag",
    find: "Find a tag",
    list_label: "Tags",
    meetings_one: "1 meeting",
    meetings_other: "{count} meetings",
    // {name} is the new tag's name, shown in bold.
    create: "Create {name}",
    // {name} is a colour from `colors`.
    colour_named: "Colour: {name}",
    colour_for: "Colour for “{name}”",
    none_yet_create: "No tags yet. Type a name to create one.",
    remove_named: "Remove {name}",
    colour_picker: "Tag colour",
    icon_picker: "Tag icon",
    no_icon: "No icon",
    name_field: "Tag name",
    colour: "Colour",
    icon: "Icon",
    // {name} is an icon from `icons`.
    icon_named: "Icon: {name}",
    add_icon: "Add an icon",
    // {name} is the other tag's name.
    already_exists: "“{name}” already exists.",
    merge_instead: "Merge into “{name}” instead",
    // The Manage tags sheet.
    manager_title: "Manage tags",
    manager_count_one: "1 tag",
    manager_count_other: "{count} tags",
    manager_close_scrim: "Close Manage tags",
    none_yet: "No tags yet.",
    menu_rename: "Rename",
    menu_colour: "Change colour",
    menu_merge: "Merge into…",
    menu_delete: "Delete…",
    delete_title: "Delete “{name}”?",
    merge_title: "Merge “{name}” into “{into}”?",
    merge: "Merge",
    merge_into: "Merge “{name}” into",
    edit_named: "Edit {name}",
    last_change: "Last change to {name}",
    actions_for: "Actions for {name}",
    run_again: "Run again",
    // How much of the archive a tag is on.
    counts_unused: "Not used yet",
    counts_in_one: "Used in 1 meeting",
    counts_in_other: "Used in {count} meetings",
    // {times} is how many places the tag is on, across {count} meetings.
    counts_times_one: "Used {times} times in 1 meeting",
    counts_times_other: "Used {times} times in {count} meetings",
    confirm_none: "None of your meetings use it.",
    confirm_one: "Updates one of your meetings. Meetings that are in rooms you can't access will keep the old tag.",
    confirm_other: "Updates {count} of your meetings. Meetings that are in rooms you can't access will keep the old tag.",
    // {name} is a person, {when} how long ago ("2 days ago").
    changed_by: "Changed by {name}",
    changed_by_when: "Changed by {name} · {when}",
    job_updating: "Updating {done} of {total}…",
    job_stopped: "Stopped before it finished.",
    job_failed: "{count} couldn't be updated",
    busy: "You already have a change running.",
    change_failed: "That change didn't go through. Try again.",
    // Errors from the server about tags and marks.
    error_unresolved: "Remove the marks that can't be placed first.",
    error_busy: "Tags are being changed elsewhere. Try again in a moment.",
    colors: {
      slate: "Slate",
      red: "Red",
      orange: "Orange",
      amber: "Amber",
      green: "Green",
      teal: "Teal",
      cyan: "Cyan",
      blue: "Blue",
      indigo: "Indigo",
      violet: "Violet",
      purple: "Purple",
      pink: "Pink",
    },
    icons: {
      star: "Star",
      flag: "Flag",
      bolt: "Bolt",
      bookmark: "Bookmark",
      check: "Check",
      alert: "Alert",
      bug: "Bug",
      heart: "Heart",
      question: "Question",
      lightbulb: "Lightbulb",
      target: "Target",
      users: "Users",
      calendar: "Calendar",
      money: "Money",
      lock: "Lock",
      chat: "Chat",
    },
  },

  // Tagging from the list and the selection bar (listTags.ts).
  list_tags: {
    saving_one: "Saving 1 tag update…",
    saving_other: "Saving {count} tag updates…",
    // {title} is a meeting's title; {reason} is why it failed.
    confirm_failed_meeting: "Could not confirm the tag update for “{title}”: {reason}",
    confirm_failed_meetings_one: "Could not confirm the tag update for 1 meeting: {reason}",
    confirm_failed_meetings_other: "Could not confirm the tag update for {count} meetings: {reason}",
    tag_failed_meeting: "Could not tag “{title}”: {reason}",
    tag_failed_meetings_one: "Could not tag 1 meeting: {reason}",
    tag_failed_meetings_other: "Could not tag {count} meetings: {reason}",
    untag_failed_meeting: "Could not untag “{title}”: {reason}",
    untag_failed_meetings_one: "Could not untag 1 meeting: {reason}",
    untag_failed_meetings_other: "Could not untag {count} meetings: {reason}",
    tagged_one: "Tagged 1 meeting",
    tagged_other: "Tagged {count} meetings",
    untagged_one: "Untagged 1 meeting",
    untagged_other: "Untagged {count} meetings",
    tagged_partial: "Tagged {done} of {total} — {failed} failed",
    untagged_partial: "Untagged {done} of {total} — {failed} failed",
    too_many: "Tag at most {max} meetings at once.",
  },

  // Loading and searching the list.
  catalog: {
    // {status} is an HTTP status code.
    load_failed: "Could not load the meeting list (HTTP {status}).",
  },
  search: {
    unreachable: "Could not reach the search index.",
    rate_limited: "Too many searches just now — try again in a moment.",
    not_ready: "The search index is not ready yet.",
    failed: "Could not search the meetings (HTTP {status}).",
    unreadable: "The search index answered something this viewer cannot read.",
  },
  // Grouping the list.
  rooms: {
    // The group for meetings that were not recorded in a Talk room.
    no_room: "No room",
    // The month heading for meetings with no date.
    undated: "Undated",
  },
  // Downloading meeting files.
  export_transfer: {
    // {title} is the meeting's title.
    no_file: "{title} has no meeting file to download.",
    download_failed: "Couldn't download the meeting file for {title} ({status}).",
    no_files: "No meeting files to download.",
    too_large: "The selected meeting files are too large for one download.",
  },

  // Small shared controls.
  ui: {
    copy_command: "Copy command",
    copy_blocked: "Copying is blocked here. It is selected: press ⌘C or Ctrl+C.",
    confirm_delete: "Delete",
    confirm_cancel: "Cancel",
  },
};

export default browse;
