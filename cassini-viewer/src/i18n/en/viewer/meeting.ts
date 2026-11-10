// One meeting: its header and facts, the player, transcript, speakers, marking, and loading a meeting file.
//
// Keys are grouped by component or module, in snake_case. See docs/translations.md.
//
// Counts come as `_one` / `_other` pairs, picked in code. Every {count},
// {seconds}, {minutes} and {max} arrives already written as a number in the
// reader's locale.
const meeting = {
  // The meeting page itself (MeetingView.svelte).
  view: {
    region_label: "Meeting view",
    back: "Back to meeting list",
    // The heading when no meeting is open yet.
    no_meeting_heading: "Meeting transcript viewer",
    rename: "Rename meeting",
    title_field: "Meeting title",
    rename_save: "Save",
    rename_reset: "Reset",
    rename_reset_title: "Restore the default title",
    rename_cancel: "Cancel",
    // A small red badge beside the transcript switcher; keep it short.
    switch_failed: "Switch failed",
    // {error} is the message of what went wrong.
    switch_failed_dismiss: "Dismiss switch error: {error}",
    switch_failed_detail: "Couldn't load that transcript: {error}",
    choose_transcript: "Choose transcript",
    // Added to the tooltip of the transcript the recording opens with.
    transcript_default: "(producer default)",
    // Export menu items: the meeting's .opus file (audio with its transcripts
    // inside) when it has one, else the bare audio.
    download_meeting_file: "Download meeting file",
    download_audio: "Download audio",
    delete_label: "Delete {title}",
    delete_title: "Delete this meeting",
    close_label: "Close the meeting",
    close_title: "Close (Esc)",
    no_audio: "This meeting contains a transcript without audio. Playback is unavailable.",
    not_found_heading: "Meeting not found",
    // Under the heading above when the meeting's file could not be read. The
    // technical reason follows underneath, in English.
    load_failed: "Couldn't load this meeting.",
    loading: "Loading meeting…",
    select_prompt: "Select a meeting to view",
    // Heading over the list of insights made from this meeting, on its Summary tab.
    insights_heading: "Insights",
    // After an insight's date: how many meetings it was made from.
    insight_context_one: "Context from {count} meeting",
    insight_context_other: "Context from {count} meetings",
    tabs_label: "Meeting content",
    tab_summary: "Summary",
    tab_transcript: "Transcript",
    search_open: "Search the transcript",
    search_close: "Close search",
    // Status lines beside the Export button.
    export_clipboard_unavailable: "Clipboard unavailable here — use Download transcript.",
    export_copied: "Transcript copied.",
    export_clipboard_blocked: "Clipboard blocked here — use Download transcript.",
    export_downloaded: "Transcript downloaded.",
    export_preparing_file: "Preparing meeting file…",
    export_file_downloaded: "Meeting file downloaded.",
    reload_failed: "Couldn't reload the recording: {error}",
    playback_failed: "Playback could not start. Try again or download the audio file.",
    playback_unsupported: "This browser could not play the audio file. You can download it to listen in an Opus player.",
  },
  // The transcript as read on the meeting page.
  transcript: {
    label: "Transcript",
    failed: "Recording available. Transcription failed.",
    turned_off: "Transcription was turned off for this recording.",
    needs_attention: "Recording available. Transcription needs attention in Settings.",
    no_speech: "No speech was transcribed in this recording.",
    not_loaded: "No transcript loaded yet.",
    // {query} is what the reader typed in the search.
    no_matches: "Nothing in this transcript matches “{query}”.",
    // Who spoke when the transcript does not say.
    unknown_speaker: "Unknown speaker",
    // A small warning badge on a turn; lower case, like a remark.
    crosstalk_badge: "probably crosstalk",
    // {name} is the speaker the turn is credited to.
    crosstalk_title:
      "Another participant's microphone was much louder here. This is probably their voice bleeding into {name}'s track, not {name} speaking.",
    // Read aloud before a remark that is probably crosstalk.
    crosstalk_sr: "Probably crosstalk.",
    // Read aloud before a short remark set inside someone else's turn; the
    // remark's words follow.
    interjection_sr: "Interjection by {name}:",
    // Beside a turn's time: who else was speaking at once. {names} is a list
    // of names ("Ben and Cara"). Shown in brackets.
    over: "over {names}",
    // The same, read aloud.
    simultaneous_sr: "Simultaneous speech over {names}",
    simultaneous_title_one: "Simultaneous speech: {names} was speaking at the same time.",
    simultaneous_title_other: "Simultaneous speech: {names} were speaking at the same time.",
  },
  // The player under the transcript.
  player: {
    play: "Play",
    pause: "Pause",
    // Keep "follow" to one short word: it is all a phone has room for.
    follow: "Follow",
    follow_label: "Follow the audio in the transcript",
    follow_title: "Keep the transcript on the words being played",
    seek_label: "Seek within meeting",
    // {time} is a clock time such as 12:04.
    elapsed: "{time} elapsed",
    total: "{time} total",
    remaining: "-{time} remaining",
    captions: "Captions",
    chapters: "Chapters",
  },
  // The meeting's details popover (MeetingFacts.svelte).
  facts: {
    chip_label: "Meeting details",
    panel_label: "Meeting details",
    date: "Date",
    duration: "Duration",
    room: "Room",
    about: "About this recording",
    transcribed: "Transcribed",
    model: "Model",
    // Not `length`: typesafe-i18n cannot take a key a function already has.
    size: "Length",
    timing: "Timing",
    // Short for the meeting's identifier.
    id: "ID",
    words_one: "{count} word",
    words_other: "{count} words",
    passages_one: "{count} passage",
    passages_other: "{count} passages",
    copy_id: "Copy meeting ID",
    id_copied: "Meeting ID copied",
    copied: "Copied",
    // The audio's channels, in the recording's details after the codec and
    // sample rate ("OPUS · 48 kHz · mono"). Mid-sentence; use your language's
    // capitalisation.
    audio_mono: "mono",
    audio_stereo: "stereo",
    // {count} is 3 or more.
    audio_channels: "{count} channels",
  },
  // How precisely the words are timed, under "Timing" in the details.
  timing: {
    passage: "Passage-timed",
    word: "Word-timed",
    mixed: "Mixed timing",
    passage_detail_display: "Seeking is precise to passages; visible words are not individually timed in this artifact.",
    word_detail_display: "Playback highlighting can follow individual words throughout the displayed transcript.",
    mixed_detail_display: "Some displayed words have individual timing, while rewritten or unmatched words remain passage-timed.",
    passage_detail: "This artifact exposes passage timing but not individual word timing.",
    word_detail: "Playback highlighting can follow individual words throughout the transcript.",
    mixed_detail: "Some transcript passages have word timing, while others only have passage timing.",
  },
  // Reading a meeting file.
  load: {
    // {url} is the address of the file.
    could_not_load: "Could not load {url}.",
    // A meeting whose file carries no name.
    untitled: "Untitled meeting",
    // The two transcripts a meeting with separated voices offers.
    transcript_separated: "Separated voices",
    transcript_original: "Original",
  },
  // A transcript copied or downloaded as Markdown.
  export: {
    title_fallback: "Meeting transcript",
    empty: "No transcript available.",
    // After a speaker's name: who else was speaking at once. {names} is a list.
    over: "(over {names})",
  },
  // The People section of the details popover (PeoplePanel.svelte).
  people: {
    count_one: "{count} participant",
    count_other: "{count} participants",
    // When a shared device was separated into voices.
    split_one: "1 voice on 1 device",
    split_one_device: "{voices} voices on 1 device",
    split_other: "{voices} voices on {devices} devices",
    // A device whose voices were separated but which has no name of its own.
    shared_device: "Shared device",
    // A voice inside its device's group. {number} is 1, 2, 3…
    voice: "Speaker {number}",
    voice_placeholder: "Speaker {number} · add a name",
    name_for: "Name for {name}",
    add_name: "Add a name",
    actions_for: "Actions for {name}",
    play_sample: "Play a sample of {name}",
    stop_sample: "Stop the sample of {name}",
    stop: "Stop",
    listen_title: "Listen to a few seconds of this voice",
    // {name} is shown in bold.
    merged_into: "Same person as {name}",
    not_same_as: "{name} is not the same person as {other}",
    not_same: "Not the same person",
    // A small button that opens the list of other voices; a ▾ follows it.
    same_as_button: "Same person as",
    same_as_label: "{name} is the same person as…",
    same_as_menu: "{name} is the same person as",
    little_speech: "Little speech — listen to check",
    // A voice merged into this one.
    includes: "Includes {name}",
    privacy: "Names are saved in the recording and visible to everyone who can open it.",
    summary_stale: "The summary was written before these speaker changes.",
    summary_stale_who: "An administrator or a moderator of the room can rewrite it.",
    inconclusive: "Couldn't separate voices on {device}: only one voice found",
    dismiss: "Dismiss",
    saving: "Saving…",
    update_failed: "Couldn't update voices",
    retry: "Retry",
    loading_updated: "Loading the updated recording…",
    // Followed by a Reload button.
    voices_updated: "Voices updated",
    reload: "Reload",
    rewrite_summary: "Also rewrite the summary for these names",
    cancel: "Cancel",
    save: "Save",
    wait_title: "Wait until the recording is updated",
    wait_note: "Wait until the recording is updated.",
    treat_as_one: "Treat as one person again",
    separate: "Separate voices",
    separate_why: "Several people used this device",
  },
  // Changing a meeting's speakers (speakerEdits.ts).
  speaker_edits: {
    error_revision_conflict: "Someone else changed the voices in this meeting. Their changes are shown now; try again.",
    error_busy: "This recording is being updated. Try again when it is done.",
    error_invalid: "Cassini could not accept these changes.",
    // {detail} is the server's reason, in English.
    error_invalid_detail: "Cassini could not accept these changes. {detail}",
    error_rate_limited: "Too many changes in a short time. Try again later.",
    error_rate_limited_in: "Too many changes in a short time. Try again in {minutes} min.",
    error_not_found: "This meeting is not available to you any more.",
    error_unavailable: "The voices in this recording cannot be changed any more.",
    // Why speakers cannot be changed on this meeting.
    reason_no_job:
      "This Cassini doesn't hold each participant's own audio for this recording — it was imported, or made before Cassini kept it.",
    reason_no_source_audio: "Each participant's own audio was not kept for this recording.",
    reason_no_transcript: "This recording has no transcript to separate.",
    reason_diarization_unavailable:
      "Voice separation is not installed on this server. An administrator can download it in Cassini's Settings.",
    reason_unpublished_rebuild:
      "This recording was processed again and the new version was not published. An administrator can run it again from Cassini Admin.",
    label_too_long: "Names can be at most {max} characters.",
    label_control_characters: "Names cannot contain control characters.",
    // Time left on an update, beside a progress bar.
    almost_done: "almost done",
    seconds_left: "about {seconds} s left",
    minutes_left: "about {minutes} min left",
    phase_starting: "Starting…",
    phase_queued: "Waiting for other recordings…",
    phase_separating: "Separating voices…",
    phase_updating: "Updating the recording…",
  },
  // The meeting's own tags, under its title (MeetingTags.svelte).
  tags: {
    group_label: "Tags on the whole meeting",
    add_tag: "Add tag",
    tag_options: "{tag} tag options",
    remove_tag: "Remove {tag}",
    remove: "Remove",
    picker_label: "Tag the whole meeting",
    // Marks made on another version of the recording.
    lost_one: "{count} mark can't be placed on this recording",
    lost_other: "{count} marks can't be placed on this recording",
    remove_lost_one: "Remove it",
    remove_lost_other: "Remove them",
    waiting_to_retry: "Annotation updates waiting to retry",
    saving: "Saving annotations…",
    sync_pending: "Tags saved · updating recording…",
    sync_delayed: "Tags saved · recording update delayed",
    sync_blocked: "Tags saved · recording needs attention",
    retry_sync: "Retry recording update",
    retry_saving: "Retry saving annotations",
    dismiss: "Dismiss",
    preparing: "Tags are being prepared…",
    // {error} is the reason, from the server.
    unavailable: "Tags aren't available right now: {error}",
  },
  // Tagged sections of the transcript.
  marking: {
    // How a tagged section is read aloud. {start} and {end} are clock times,
    // {actor} who made it.
    describe: "{tag}, {start} to {end}, marked by {actor}",
    // {mark} is the description above.
    open_mark: "{mark}. Open it.",
    go_to_mark: "{mark}. Go to it.",
    // Who made a mark that is still being saved.
    you: "You",
    section_changed: "This section changed or was removed. Select it again before moving it.",
    remove_lost_first: "Remove the marks that can't be placed first.",
    sections_label: "Tagged sections",
    edge_from: "Where the section begins",
    edge_to: "Where the section ends",
    // The rail beside the transcript.
    map_label: "Tagged sections across the whole meeting",
    rail_label_grab:
      "The whole meeting. Drag to scroll the transcript, click to go to a moment, or press Enter for the turn under the playhead.",
    rail_label: "The whole meeting. Drag to scroll the transcript, or click to go to a moment.",
  },
  // How many sections are tagged, with arrows between them (SectionsNav.svelte).
  sections_nav: {
    count_one: "{count} tagged section",
    count_other: "{count} tagged sections",
    // Shown in a small pill, followed by sections_label: "2 of 5" "tagged sections".
    position: "{current} of {count}",
    label_one: "tagged section",
    label_other: "tagged sections",
    previous: "Previous tagged section",
    next: "Next tagged section",
  },
  // The card for a selected stretch of transcript (StretchToolbar.svelte).
  // Keycaps printed next to a button, for its keyboard shortcut. Use the key's
  // name as printed on keyboards in your language (German: "Strg").
  keys: {
    // The Control key (Mac shows ⌘ instead).
    ctrl: "Ctrl",
    // The Escape key.
    escape: "Esc",
  },
  stretch: {
    toolbar_label: "This section",
    unsaved: "Unsaved changes",
    edit: "Edit",
    done: "Done",
    save_changes: "Save changes",
    remove: "Remove",
    cancel: "Cancel",
    tag_selection: "Tag selection",
    picker_label: "Tag selection",
    copy_text: "Copy text",
    copied: "Copied",
    copy_link: "Copy link",
    link_copied: "Link copied",
    copy_this_link: "Copy this link:",
    link_field: "Selection link",
  },
  // Find in the transcript (TranscriptToolbar.svelte).
  find: {
    toolbar_label: "Transcript",
    field: "Find in transcript",
    only_matching: "Show only matching turns",
    only_matching_on_title: "Showing only the turns that mention your search. Click to show the whole transcript again.",
    only_matching_off_title: "Hide every turn that doesn't mention your search, so you can read the matches together.",
    // Which match is shown: "3 of 12".
    position: "{current} of {count}",
    no_matches: "No matches",
    previous: "Previous match",
    previous_title: "Previous match (Shift+Enter)",
    next: "Next match",
    next_title: "Next match (Enter)",
    clear: "Clear the search",
  },
  // A word in the transcript (TranscriptWords.svelte).
  word: {
    // {word} is the word, {time} where it was said, {speaker} who said it.
    label: "{word} — seek to {time}, {speaker}",
    estimated_title: "Estimated word timing",
  },
};

export default meeting;
