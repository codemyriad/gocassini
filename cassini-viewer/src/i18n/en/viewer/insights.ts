// Insights in the viewer: the insight list, cards and documents, the prepare panel and summary requests.
//
// Keys are grouped by component or module, in snake_case. See docs/translations.md.
const insights = {
  // An insight run's state, as a short badge on its card and a line in its
  // document. The operator's four states. Each describes the insight (the run
  // and its document): agree with that noun if your language needs to.
  status: {
    queued: "Queued",
    running: "Running",
    succeeded: "Succeeded",
    failed: "Failed",
  },
  // What names an insight that has no name, no question and no template id.
  headline_fallback: "Insight",
  // Why an insight run failed: a title saying what happened, and a line
  // saying what to do. Shown on the failed insight's document.
  failure: {
    bad_request: { title: "This request could not be run", line: "Check the app log." },
    // {place} is the path to a settings page in the app, "Operator › AI providers".
    no_provider: { title: "No AI endpoint", line: "Add one under {place}." },
    provider_refused: {
      title: "The endpoint refused the request",
      // {place}: as above.
      line: "Check its key, quota or model name under {place}.",
    },
    model_failed: { title: "The model did not answer", line: "Retry, or pick fewer meetings." },
    template_changed: {
      title: "The template changed before this insight ran",
      line: "Generate it again to use the template as it is now.",
    },
    write_failed: { title: "The insight could not be written to disk", line: "Check the app's storage." },
    deliver_failed: {
      title: "The insight could not be saved to your Nextcloud files",
      line: "Check your space, then retry.",
    },
    timeout: { title: "Stopped after an hour", line: "Retry with fewer meetings or a faster endpoint." },
    staging_failed: { title: "The meetings could not be staged", line: "Check the app's storage." },
    catalog_failed: { title: "Your meeting list could not be read", line: "Retry in a moment." },
    meeting_unavailable: {
      title: "One of these meetings is no longer available to you",
      line: "Unpick it and ask again.",
    },
    download_failed: { title: "A meeting could not be downloaded from Nextcloud", line: "Retry in a moment." },
    assemble_failed: {
      title: "The meetings could not be assembled into one document",
      line: "Check the app log.",
    },
    interrupted: { title: "Cassini restarted before this insight finished", line: "Retry it." },
    unknown: { title: "The insight failed", line: "Retry, or check the app log." },
  },
  // The name suggested for a new insight, e.g. "Summary · Design review, 3 Oct
  // 2026". It is saved with the insight, so it stays in the language it was
  // made in.
  default_name: {
    // Stands in for the template's name when the reader cannot see templates.
    template_fallback: "Insight",
    // Stands in for the meetings' titles: one meeting with no title, or several.
    // Plural; see the translator guide. {count}: meetings.
    meetings: "{count:number|num} {{meeting|meetings}}",
  },
  // An insight's card in the meeting list.
  card: {
    // A small uppercase label above the card's title.
    kind: "Insight",
    // How many of the insight's meetings the reader can see.
    // Plural; see the translator guide. {count}: meetings.
    meetings: "{count:number|num} {{meeting|meetings}}",
  },
  // An insight open in the side sheet.
  document: {
    // Accessible name of the region holding one insight's document.
    region_label: "Insight",
    name_input_label: "Insight name",
    rename_button_label: "Rename the insight",
    // Tooltip on the pencil button.
    rename_tooltip: "Rename",
    close_label: "Close the insight",
    // The insight's meetings came from several rooms.
    // Plural; see the translator guide. {count}: rooms, 2 or more.
    rooms: "{count:number|num} {{room|rooms}}",
    export_label: "Export this insight",
    copy_markdown: "Copy as markdown",
    download_markdown: "Download markdown",
    open_in_files: "Open in Files",
    clipboard_unavailable: "Clipboard unavailable here — download the markdown instead.",
    copied: "Copied as markdown.",
    clipboard_blocked: "Clipboard blocked here — try Copy again, or download the markdown.",
    // {file} is the downloaded file's name.
    downloaded: "Downloaded {file}.",
    // Heading over the list of meetings the insight read.
    // Plural; see the translator guide. {count}: meetings.
    sources_heading: "Context from {count:number|num} {{meeting|meetings}}",
    // The same heading when none of the meetings can be shown.
    sources_heading_none: "Context sources",
    // Plural; see the translator guide. {count}: meetings.
    missing_sources:
      "{count:number|num} source {{meeting is|meetings are}} no longer available (deleted, or no longer shared with you).",
    // {status} is "Queued" or "Running".
    pending: "{status}. The answer appears here when it finishes.",
    cannot_load: "This build cannot fetch the document — the run and what it read are all it can show.",
    loading: "Loading the answer…",
    // {error} is a technical reason, in English.
    load_failed: "The answer could not be loaded: {error}",
    // {path} is a file path, shown in code style.
    document_missing: "This run succeeded, but its document is no longer at {path} in your Nextcloud files.",
    // The details of the run, at the bottom of the insight.
    run_heading: "This run",
    asked_by: "Asked by",
    // Who asked, when the record does not say.
    asked_by_unknown: "unknown",
    status_label: "Status",
    template_label: "Template",
    provider_label: "Provider",
    // How many times the insight was tried.
    attempt_label: "Attempt",
  },
  // The markdown file an insight is copied or downloaded as. People read
  // these lines; no program parses them.
  export: {
    // {date} is a date and time with its time zone.
    written_by: "Written by Cassini on {date}",
    // {template} is a template id and version, e.g. "summarise v3".
    template: "Template: {template}",
    model: "Model: {model}",
    // Above the list of the meetings the insight read.
    sources: "Drawn from these meetings:",
  },
  // A meeting's summary, and asking for one.
  summary: {
    missing_failed: "No summary: it could not be generated when the meeting was published.",
    missing_disabled: "No summary: summaries were turned off when this meeting was published.",
    missing_not_configured: "No summary: no language model was set up for summaries when this meeting was published.",
    error_busy: "A summary is already being written for this meeting.",
    error_has_summary: "This meeting has a summary now. Reload it to see it.",
    error_forbidden: "Only an administrator or a moderator of the room can replace this summary.",
    error_rate_limited: "Too many summaries asked for in a short time. Try again later.",
    error_unavailable: "A summary can't be written for this meeting now.",
    // {error} is a technical reason, in English.
    error_other: "Couldn't ask for the summary: {error}",
    // The summary request block on a meeting.
    request: {
      writing: "Writing the summary… It appears here when it is ready.",
      last_failed: "The last attempt to write a summary failed.",
      note_write: "Everyone who can open the meeting will see it.",
      note_replace: "Everyone who can open the meeting sees the new summary instead of this one.",
    },
  },
  // The Prepare panel: picked meetings, ready to copy or download as context.
  prepare: {
    title: "Prepare context",
    region_label: "Prepare context",
    export_label: "Export selected meetings",
    close_label: "Close Prepare",
    // Heading over the picked meetings.
    in_bundle: "In this bundle",
    // On a meeting that stops the bundle being made. "Prepare" is this panel.
    blocks_prepare: "Blocks Prepare",
    length_unknown: "Length not recorded",
    // {title} is a meeting's title.
    unpick: "Unpick {title}",
    total: "Total",
    selection_changed_bundle: "The selection changed while the bundle was being prepared — press again.",
    selection_changed: "The selection changed — press again.",
    preparing_transcripts: "Preparing transcripts…",
    clipboard_unavailable: "Clipboard unavailable here — use Export to download.",
    transcripts_copied: "Transcripts copied.",
    // {error} is a technical reason, in English.
    transcripts_copy_failed: "Couldn't copy transcripts: {error}. Press again or download them.",
    transcripts_downloaded: "Transcripts downloaded.",
    preparing_files: "Preparing meeting files…",
    file_downloaded: "Meeting file downloaded.",
    files_downloaded: "Meeting files downloaded.",
    earlier_files_downloaded: "Meeting files from the earlier selection downloaded.",
    preparing: "Preparing…",
    // Plural; see the translator guide. {count}: characters copied.
    copied_characters: "Copied {count:number|num} {{character|characters}}.",
    clipboard_blocked: "Clipboard blocked here — use Export to copy again or download.",
    // {file} is the downloaded file's name.
    downloaded: "Downloaded {file}.",
  },
};

export default insights;
