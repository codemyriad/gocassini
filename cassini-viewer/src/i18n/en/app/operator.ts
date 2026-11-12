// Operator: the job list, Doctor and readiness, recording setup, sharing notices, storage, support and migrations.
//
// Keys are grouped by component or module, in snake_case. See docs/translations.md.
// `occ` commands, paths and environment variable names stay in the code and are
// never part of a string here.
const operator = {
  // operator/readiness.ts: the Doctor panel (Operator › Doctor), built from
  // the operator's readiness report.
  readiness: {
    // Doctor's heading, from the operator's verdict.
    title: {
      // Plural; see the translator guide. {count}: checks that need action, a
      // handful at most; ?? is that number.
      needs_action: "{{count:One recording check needs attention|?? recording checks need attention}}",
      warn: "Recording checks need attention",
      not_verified: "Recording setup needs verification",
      not_verified_archive_failed: "Recording setup needs verification; archive search re-index failed",
      not_verified_archive_attention: "Recording setup needs verification; archive search needs attention",
      not_verified_archive_reindexing: "Recording setup needs verification; archive search is re-indexing",
      passed: "Recording checks passed",
      ready_archive_failed: "Recording ready; archive search re-index failed",
      ready_archive_attention: "Recording ready; archive search needs attention",
      ready_archive_reindexing: "Recording ready; archive search is re-indexing",
    },
    // The name of each check row. Talk's own terms ("High Performance
    // Backend") follow Nextcloud Talk's translation where it has one.
    check: {
      configuration: "Saved configuration",
      storage: "Recording storage",
      storage_sharing: "Recording sharing",
      archive_search: "Archive search",
      talk_discovery: "Talk connection",
      talk_hpb: "High Performance Backend",
      talk_handoff: "Recording credential",
      test: "Test recording",
      host: "Recording host",
      host_workdir: "Recording volume",
      host_tmpdir_writable: "Temporary space",
    },
    // The state word beside a check's name.
    state: {
      passed: "Passed",
      warn: "Needs attention",
      needs_action: "Needs action",
      not_verified: "Not verified",
      repair_failed: "Repair failed",
      reindex_failed: "Re-index failed",
      // A test recording someone played back earlier.
      previously_confirmed: "Previously confirmed",
    },
    // Buttons on a check row.
    action: {
      configure_talk: "Set credential",
      connect_talk: "Connect Talk",
      test_recording: "Record a test",
      recheck: "Check",
      setup_storage: "Set up storage",
      repair_configuration: "How to repair this",
      change_secret: "Change secret",
      show_steps: "Show steps",
    },
    // Buttons for a repair Cassini runs itself.
    repair: {
      backfill_search: "Re-index now",
      backfill_search_retry: "Try re-indexing again",
    },
    // Links to documentation for a fault Cassini cannot repair.
    guide: {
      talk_hpb: "How to set up a High Performance Backend",
      nextcloud: "Read Nextcloud's guide",
    },
    // The second half of Doctor's heading (doctor.verdict_and_follow_up),
    // drawn as a link to the test recording row; mid-sentence; use your language's capitalisation.
    test_follow_up: {
      failed: "your test recording did not finish",
      play: "play your test recording to finish it",
      waiting_for_talk: "your test recording is waiting for you in Talk",
      in_progress: "your test recording is still in progress",
    },
    // When Doctor could not re-read the report.
    refresh_failure: {
      signed_out:
        "Your Nextcloud session may have expired, or this account is no longer an administrator. Reload the page and sign in again.",
      not_responding:
        "Cassini is not responding. It may be restarting, so wait a moment and try again. If it keeps happening, check that Cassini is enabled under Nextcloud's apps.",
      unreachable: "Your browser could not reach Nextcloud. Check the connection, then try again.",
      // {message}: the operator's own error, in English.
      answered_with_error: "Cassini answered with an error: {message}",
      unknown: "Something went wrong. Try again.",
    },
    // Recording sharing row: published meetings Nextcloud refused to share
    // with someone in the room. {since}: a short date ("12 Oct"). When the
    // operator says who, `missing` follows as a second sentence.
    unshared: {
      // Plural; see the translator guide. {count}: recordings.
      summary: "{count:number|num} {{recording was|recordings were}} not shared with everyone.",
      // Plural; see the translator guide. {count}: recordings.
      summary_since: "{count:number|num} {{count:recording|recordings}} since {since} {{count:was|were}} not shared with everyone.",
      // {who}: one of the people/groups phrases below.
      missing: "Missing: {who}.",
      // Plural; see the translator guide. {count}: people.
      people: "{count:number|num} {{person|people}}",
      // Plural; see the translator guide. {count}: groups.
      groups: "{count:number|num} {{group|groups}}",
      // {people} and {groups}: the phrases above, e.g. "3 people and 1 group".
      who_both: "{people} and {groups}",
    },
    // Recording sharing row: recipients Nextcloud's share limit held back.
    // {since}: a short date ("6 Nov").
    share_limited: {
      // Plural; see the translator guide. {count}: recordings.
      summary:
        "Nextcloud's share limit held back recipients of {count:number|num} {{recording|recordings}}. Re-share each from its run, or raise the limit.",
      // Plural; see the translator guide. {count}: recordings.
      summary_since:
        "Nextcloud's share limit held back recipients of {count:number|num} {{count:recording|recordings}} since {since}. Re-share each from its run, or raise the limit.",
    },
  },
  // buildFeatureNotice (operator/setupHealth.ts): what Browse says about a
  // capability with no AI provider behind it (NeedsSetupCard).
  feature_notice: {
    insights_title: "No AI endpoint is available",
    summaries_title: "Meetings are not being summarised",
    insights_summary: "Insights need an AI endpoint, and this deployment has none.",
    insights_summary_ask_admin: "Insights need an AI endpoint, and this deployment has none. Ask a Nextcloud administrator.",
    summaries_summary: "Summaries need an AI endpoint and the summarise step switched on.",
    summaries_summary_ask_admin:
      "Summaries need an AI endpoint and the summarise step switched on. Ask a Nextcloud administrator.",
    // A button. {place} is the section's name, "AI providers".
    action: "Open {place}",
    // A narrow button; the same words as the button on the AI providers panel.
    action_short: "Add a provider",
    insights_action_title: "Add a provider to create insights",
    summaries_action_title: "Add a provider to write summaries",
  },
  // operator/stepOutcomes.ts: notices on a run in Operator › Jobs, saying
  // what its transcription, summary and sharing steps did.
  step_outcomes: {
    // The operator stores a code for these, not an English sentence (#472 review).
    audience_notice: {
      no_capture: "No attendee list was captured; only the starter received this recording.",
      no_local: "No local attendees were captured; only the starter received this recording.",
      unknown: "This recording's audience mode was not recognised; only the starter received this recording.",
    },
    transcription: {
      disabled: "Published without a transcript: transcription is turned off",
      model_unavailable: "Published without a transcript: no speech model could run",
      model_unavailable_hint: "Check Speech models in Settings, then rerun to transcribe it.",
      failed: "Published without a transcript: transcription failed",
      insufficient_resources: "Published without a transcript: not enough memory or GPU to transcribe",
      // {reason}: the operator's internal reason code, e.g. "timeout".
      other: "Published without a transcript ({reason})",
      rerun_hint: "Rerun transcribes it again.",
    },
    summary: {
      template_fallback: "Summarised with the built-in template: the configured summary template could not run",
      template_fallback_hint:
        "The build log says why. Fix the template in {templates}, or choose another one for summaries in {pipeline}.",
      failed: "No summary: the summary could not be generated",
      failed_hint: "The build log has the language model's error.",
      disabled: "No summary: summaries are turned off",
      not_configured: "No summary: no language model is set up for summaries",
      not_configured_hint: "Add one in Settings.",
      // {reason}: the operator's internal reason code.
      other: "No summary ({reason})",
    },
    // The small label beside a refused recipient's id. "Team" is Nextcloud's
    // name for a circle; use Nextcloud's own translation of it.
    principal: {
      person: "person",
      group: "group",
      team: "Team",
    },
    // Plural; see the translator guide. {count}: people or groups.
    recipients: "{count:number|num} {{recipient|recipients}}",
    refused: {
      // {id}: a user, group or Team id. {kind}: one of the labels above.
      entry_with_kind: "{id} ({kind})",
      // {who}: an id, or the line above. {reason}: Nextcloud's reason, in English.
      entry_with_reason: "{who}: {reason}",
      // {names}: the first few ids, e.g. "bob, carol, dave".
      // Plural; see the translator guide. {count}: how many more.
      list_more: "{names} and {count:number|num} {{count:other|others}}",
    },
    // The line beside "Re-share with the room" after it ran.
    reshare: {
      // {recipients}: "1 recipient" or "3 recipients".
      held_back: "Still held back by Nextcloud's share limit: {recipients}. Re-share in 10 minutes.",
      // {held_back}: the sentence above.
      shared_again_held_back: "Shared again. {held_back}",
      shared_with_everyone: "Shared with everyone.",
      // {names}: who, e.g. "bob, carol and dave".
      still_refused:
        "Shared again. Nextcloud still refused {recipients} ({names}). Check Doctor › Recording sharing.",
      // Two of the sentences above, one after the other.
      two_sentences: "{first} {second}",
    },
    share: {
      starter_refused: "Not published: Nextcloud refused to share it with the person who started the recording.",
      // {starter}: the user id of the person who started the recording.
      starter_refused_named: "Not published: Nextcloud refused to share it with {starter}, who started the recording.",
      starter_refused_hint_rerun:
        "Fix the setting Doctor › Recording sharing names, then Rerun. Rerun publishes it and shares it with the whole room.",
      starter_refused_hint_rerun_attendees:
        "Fix the setting Doctor › Recording sharing names, then Rerun. Rerun publishes it and shares it with the call's attendees.",
      starter_refused_hint_no_rerun:
        "Fix the setting Doctor › Recording sharing names so later recordings publish. This run cannot be rerun.",
      // {recipients}: "1 recipient" or "3 recipients".
      held_back:
        "Published. Held back by Nextcloud's share limit: {recipients}. Re-share in 10 minutes, or raise the limit (Doctor › Recording sharing).",
      // Plural; see the translator guide. {count} of {audience}: refused recipients out of everyone in the room.
      partial_of: "Published, but not shared with {count:number|num} of {audience:number|num} {{audience:recipient|recipients}}",
      // {recipients}: "1 recipient" or "3 recipients".
      partial: "Published. Not shared: {recipients}",
      // A disclosure; {count}: how many are listed in it.
      who_was_refused: "Who was refused ({count})",
      partial_hint: "Nextcloud refused them because of a sharing setting. Doctor › Recording sharing names it and the fix.",
      // Plural; see the translator guide. {count}: recipients.
      also_held_back: "Nextcloud's share limit also held back {count:number|num} {{recipient|recipients}}.",
      reshare_button: "Re-share with the room",
      reshare_button_attendees: "Re-share with call attendees",
      reshare_note:
        "Adds whoever is missing, including anyone removed by hand. People who accept shares by hand will find it waiting in Browse.",
      reshare_note_attendees:
        "Adds whoever was in the call and is missing, including anyone removed by hand. People who accept shares by hand will find it waiting in Browse.",
    },
  },
  // operator/runStages.ts: the five steps of a run, on its stage bar in
  // Operator › Jobs. Narrow labels: keep them to one short word.
  run_stages: {
    stage: {
      record: "Record",
      build: "Build",
      summary: "Summary",
      // Packs the built meeting into the one portable .opus file that is published.
      seal: "Seal",
      publish: "Publish",
    },
    // The Record step after its recording was deleted.
    record_deleted: "Record (deleted)",
    // Said in place of a time under the Summary step; each describes the
    // summary. "In Build": it was written as part of the Build step.
    summary_note: {
      completed: "In Build",
      skipped: "Skipped",
      failed: "Failed",
    },
    // Why the Summary step reads as it does, on hover.
    summary_reason: {
      disabled: "Summaries are turned off",
      not_configured: "No summary model is configured",
      no_transcript: "There was no transcript to summarize",
      llm_failed: "The summary model failed; the build log says why",
    },
    summary_rewritten_after_rename: "Rewritten after speakers were renamed",
    summary_written_in_build: "Written during the build",
  },
  // operator/attemptModels.ts: an attempt's fields in a run's detail.
  attempt_models: {
    // {model}: a speech model id. {device}: "GPU" or "CPU".
    model_on_device: "{model} on {device}",
    // Why an attempt ran.
    reason: {
      recording_ended: "Recording ended",
      recording: "Recording",
      rerun: "Rerun requested",
      summarize: "Summary requested",
      refine_rewrite: "Speakers renamed, summary rewritten",
      refine_keep: "Speakers renamed, summary kept",
    },
    // The speech model field of an attempt that did not transcribe.
    speech: {
      // {attempt}: the attempt number.
      reused_from: "Reused from attempt {attempt}",
      reused: "Reused",
    },
    // The summary model field when there is no model to name.
    summary: {
      kept: "Kept as it was",
      skipped: "Skipped",
      skipped_disabled: "Skipped: summaries are turned off",
      skipped_not_configured: "Skipped: no summary model is configured",
      skipped_no_transcript: "Skipped: there was no transcript",
      failed: "Failed",
      failed_llm: "Failed: the model returned an error",
      written_unrecorded: "Written; the model was not recorded",
    },
  },
  // operator/processingSpeed.ts: how long a run took to process, in a run's
  // detail. "12m · 4.8× real time".
  processing_speed: {
    // {factor}: a number, e.g. "4.8".
    multiplier: "{factor}×",
    // {multiplier}: the line above, e.g. "4.8×". Processing speed against the
    // meeting's length.
    real_time: "{multiplier} real time",
    // On hover. {span}: a duration, e.g. "2m".
    not_counting_summary: "Not counting {span} waiting on the summary model",
  },
  // operator/pipelineStatus.ts: the status strip at the top of Settings ›
  // Publish pipeline, one short line per step. Parts are joined with " · ".
  pipeline_status: {
    on: "On",
    // {detail}: what is on, e.g. "Balanced · GPU" or a provider's name.
    on_with: "On · {detail}",
    off: "Off",
    off_downloading: "Off · downloading",
    off_downloading_percent: "Off · downloading {percent}%",
    unknown: "Unknown",
    // Why a step is amber or busy, after the state; mid-sentence; use your language's capitalisation.
    reason: {
      previous_model_meanwhile: "previous model transcribes meanwhile",
      downloading_model: "downloading the model",
      downloading_model_percent: "downloading the model · {percent}%",
      model_check_failed: "model check failed",
      model_not_installed: "model not installed",
      needs_transcript: "needs a transcript",
      needs_transcribe_on: "needs Transcribe on",
    },
    // A small badge beside a step; mid-sentence; use your language's capitalisation.
    badge: {
      download_model: "download a model",
      needs_transcript: "needs a transcript",
      add_provider: "add a provider",
      until_model_ready: "until the model is ready",
      no_reprocessing: "no reprocessing later",
    },
    publish: {
      transcript_only: "Transcript only",
      audio_transcript: "Audio + transcript",
      audio_no_transcript: "Audio, no transcript",
    },
    // How long recordings are kept, after the media kept, e.g. "Audio only · 30 days".
    retention: {
      forever: "forever",
      // Plural; see the translator guide. {count}: days.
      days: "{count:number|num} {{day|days}}",
    },
  },
  // RecordedAtMigration.svelte and operator/recordedAtMigration.ts: the Doctor
  // card that writes the recording time into past recordings' files.
  recorded_at_migration: {
    // {message}: the error, usually the operator's own, in English.
    refresh_failed: "Couldn’t refresh: {message}",
    button: {
      // Plural; see the translator guide. {count}: recordings.
      write: "Write the recording time into {count:number|num} {{recording|recordings}}",
      pause: "Pause",
      resume: "Resume",
      // Plural; see the translator guide. {count}: blocked recordings.
      retry: "Retry {count:number|num} blocked {{recording|recordings}}",
    },
    summary: {
      // Plural; see the translator guide. {count}: recordings.
      needed:
        "{count:number|num} {{recording was|recordings were}} published before Cassini wrote the recording time into the file itself. Browse already shows the right time; writing it into the {{file|files}} makes it travel with downloads, the Nextcloud apps and the cassini command line.",
      // Added after `needed` while the first count runs.
      still_counting: "Cassini is still counting, so this number may grow; you can start once it has finished.",
      checking: "Cassini is checking which past recordings were published before it wrote the recording time into the file itself.",
      // Plural; see the translator guide. {count}: recordings left.
      running: "Writing the recording time into {count:number|num} {{recording|recordings}}.",
      // Plural; see the translator guide. {count}: recordings left; {done}: recordings finished.
      running_done: "Writing the recording time into {count:number|num} {{count:recording|recordings}}; {done:number|num} done.",
      // Follows a running sentence. Plural; see the translator guide.
      // {workers}: recordings written at once (one or a few); {seconds}: the
      // pause between files.
      pace: "Cassini works on {{workers:one|up to ??}} at a time, {seconds:number|num} {{seconds:second|seconds}} apart, so it stays out of Nextcloud's way.",
      // Plural; see the translator guide. {count}: recordings being written now (one or a few);
      // {waiting}: recordings that have not started.
      pausing:
        "Pausing. {{count:The recording already being written is|The ?? recordings already being written are}} finished first, or left as {{count:it was|they were}} if {{count:its|their}} upload had not gone through, so no file is left half-written; {waiting:number|num} {{waiting:recording then waits|recordings then wait}}.",
      // Plural; see the translator guide. {remaining}: recordings left.
      paused: "Paused with {remaining:number|num} {{recording|recordings}} left. Nothing is being written until you resume.",
      // Plural; see the translator guide. {remaining}: recordings left; {done}: recordings finished.
      paused_done: "Paused with {remaining:number|num} {{remaining:recording|recordings}} left and {done:number|num} done. Nothing is being written until you resume.",
      // Plural; see the translator guide. {count}: blocked recordings.
      blocked: "{count:number|num} {{recording|recordings}} could not be written and {{needs|need}} you. Fix what each one says, then retry.",
      // Plural; see the translator guide. {count}: blocked recordings; {done}: recordings finished.
      blocked_done:
        "{count:number|num} {{count:recording|recordings}} could not be written and {{count:needs|need}} you; {done:number|num} {{done:is|are}} done. Fix what each one says, then retry.",
      done: "Every past recording carries its recording time in the file itself ({done} written).",
      unknown: "Cassini could not read the migration's progress.",
    },
    // What Nextcloud reports, as label and value pairs.
    facts: {
      versioning: "File versioning",
      activity_app: "Activity app",
      notifications_app: "Notifications app",
      web_push: "Web push",
      unknown: "Unknown",
      enabled: "Enabled",
      not_enabled: "Not enabled",
      web_push_browsers: "Allowed, browsers too",
      web_push_apps: "Allowed, not for browsers",
      web_push_off: "Not allowed",
    },
    // The sentences under "What this does in Nextcloud", built from what
    // Nextcloud's capabilities say (operator/recordedAtNotices.ts). Quoted
    // Nextcloud wording (“A file or folder has been changed”, “file changed”,
    // Administration settings › Activity) should match Nextcloud's own
    // translation in your language. versions_retention_obligation and
    // config.php are names; keep them as they are.
    notices: {
      // First sentence, always. {account}: the account name, "cassini".
      saved_again:
        "Each recording is saved again by Cassini's {account} account, over the same file: its shares, links and file id stay as they are, and the audio is unchanged.",
      // When Nextcloud's capabilities could not be read.
      unreadable:
        "Cassini could not read Nextcloud's capabilities, so it cannot tell whether the Activity and Notifications apps or file versioning are enabled.",
      // After `unreadable`: what happens with Nextcloud's defaults.
      unreadable_defaults:
        "Where file versioning is enabled (Nextcloud's default), the file as it was before is kept as an earlier version. Where the Activity app is enabled (also the default), everyone the recording is shared with gets a “file changed” entry for each recording in their Activity stream; Nextcloud may group changes that arrive close together.",
      // Shown when the Activity app is enabled, or might be.
      email_defaults:
        "With Nextcloud's defaults the change is not emailed and not sent as a notification: both are off for “A file or folder has been changed”. An administrator can change those defaults under Administration settings › Activity, and each person can turn them on in their own Activity settings. Cassini cannot read either. Anyone who turned notifications on gets one per recording; anyone who turned email on finds the changes in their activity email, which Nextcloud collects and sends at the frequency they chose rather than one per change.",
      // {account}: the account name, "cassini".
      versioning_on:
        "File versioning is enabled, so the file as it was before is kept as an earlier version in the {account} account. It takes disk space but does not count against that account's quota. Nextcloud expires versions by versions_retention_obligation in its config.php, which Cassini cannot read, and by the Versions app's limit of half the account's free space, past which the oldest go first.",
      versioning_off: "File versioning is not enabled, so Nextcloud keeps no copy of the file as it was before.",
      activity_on:
        "The Activity app is enabled, so everyone the recording is shared with gets a “file changed” entry for each recording in their Activity stream; Nextcloud may group changes that arrive close together.",
      activity_off: "The Activity app is not enabled, so the change appears in nobody's activity stream and no activity email is sent.",
      notifications_off: "The Notifications app is not enabled, so no notification can be sent for the change.",
      notifications_on:
        "The Notifications app is enabled, so a person who turned on notifications for changed files gets one per recording in Nextcloud, and on their devices where push notifications are set up.",
      // Added after notifications_on when Nextcloud allows web push for browsers.
      web_push_browsers: "Web push is allowed, for browsers too, so it can also reach a browser where that person accepted push notifications.",
      // Added after notifications_on when Nextcloud allows web push, but not for browsers.
      web_push_apps: "Web push is allowed, but not for browsers.",
    },
    panel: {
      title: "Recording times in past recordings",
      // State words beside the title; short.
      state_done: "Done",
      state_blocked: "Needs attention",
      state_pausing: "Pausing",
      state_running: "Running",
      state_paused: "Paused",
      state_checking: "Checking",
      state_needed: "Not started",
      blocked_list_label: "Blocked recordings",
      // After the listed blocked recordings.
      // Plural; see the translator guide. {count}: how many more.
      blocked_more: "and {count:number|num} more {{recording|recordings}}",
      details_summary: "What this does in Nextcloud",
      facts_label: "What Nextcloud reports",
      pause_note:
        "You can pause at any time. A file whose upload was already sent is finished first, and one not yet uploaded is left as it was, so none is left half-written.",
    },
  },
  // Operator › Storage: DetailedStorageUsageReport.svelte,
  // StorageCategoryChart.svelte and operator/storageCharts.ts.
  storage: {
    // Each kind of stored file: its name (a row and a chart title) and a line
    // under it.
    // What each kind of file is (its name is app.file_kind).
    category: {
      published: "Published files, including legacy archives, dated by publication or recording. Not touched by this server’s cleanup.",
      recordings: "Audio, video and supporting files, dated when recording finished.",
      current: "This server's copy of the latest published meeting, dated when it was published.",
      history: "Failed recordings, failed processing, replaced output and failed publishing.",
      failed_capture: "Recordings from failed or interrupted attempts, dated when the attempt ended.",
      failed_build: "Processing output from failed or interrupted attempts, dated when the attempt ended.",
      superseded: "Older output, dated when a later publication replaced it.",
      failed_publish: "Publishing files from failed or interrupted attempts, dated when the attempt ended.",
      logs: "Logs from each recording attempt, dated when the attempt ended.",
      other: "Work in progress and files no cleanup rule covers. They have no date.",
    },
    report: {
      title: "Storage",
      // {when}: a date and time.
      last_calculated: "Last calculated {when}",
      calculating: "Calculating…",
      recalculate: "Recalculate",
      calculate: "Calculate",
      no_operator: "Cassini’s operator connection is not available.",
      // {message}: the error, usually the operator's own, in English.
      load_failed: "Couldn’t load storage details: {message}",
      recalculate_failed: "Couldn’t recalculate: {message}",
      loading: "Loading this index…",
      never_title: "Storage has not been calculated yet",
      never_line: "Last calculated: Never.",
      local_heading: "On this server",
      local_description: "Recordings, published copies, attempts and logs.",
      published_heading: "In Nextcloud Files",
      published_description: "Not touched by this server’s cleanup.",
      partial_result: "Partial result",
      not_available: "Not available",
      not_measured: "Couldn’t measure",
      partial_local: "Some local data could not be measured or classified. Values below are partial; recalculate to retry.",
      partial_published:
        "The Nextcloud breakdown is incomplete. Unmeasured roots are excluded; files whose dates could not be read are included as undated. Recalculate to retry.",
      published_dates_missing: "Nextcloud date data is not available yet. Recalculate after updating the operator.",
      errors_summary: "Measurement details",
      categories_missing: "Category data is not available yet. Recalculate after updating the operator.",
      by_type: "By type",
      by_date: "By date",
      // {category}: a category name. {size}: e.g. "1.5 GB".
      row_label: "{category}: {size}",
      explore_label: "Explore {category}: {size}",
      // Before a size that is incomplete: "Partial · 1.5 GB".
      partial_prefix: "Partial",
      partial_category: "Partial result: some files could not be measured or dated.",
      unknown_category_description: "Retained local files.",
    },
    chart: {
      range_label: "Time range",
      // {category}: the chart's category name, e.g. "Recordings".
      range_select_label: "{category} time range",
      range_all: "All time",
      // Plural; see the translator guide. {count}: days.
      range_last_days: "Last {count:number|num} {{day|days}}",
      range_last_year: "Last year",
      range_custom: "Custom range",
      from: "From",
      through: "Through",
      from_label: "{category} from date",
      through_label: "{category} through date",
      // {size}: e.g. "1.5 GB".
      // Plural; see the translator guide. {count}: files.
      undated: "{size} in {count:number|num} {{count:file|files}} without a known date. Included in the total, not in the chart.",
      empty_no_files: "No retained files in this category.",
      empty_no_dated_files: "No dated files to plot in this category.",
      // After the total for the range shown: "1.5 GB in selected range".
      in_selected_range: "in selected range",
      precision_daily: "Daily",
      // Plural; see the translator guide. {count}: days, 2 or more.
      precision_days: "{count:number|num} {{day|days}} per bar",
      // Which date the bars use.
      dates_lifecycle: "UTC lifecycle dates",
      dates_published: "publication or recording dates",
      empty_range: "No retained bytes in this date range. Try a wider range.",
      scroll_earlier: "Earlier",
      scroll_later: "Later",
      scroll_earlier_label: "Scroll {category} earlier",
      scroll_later_label: "Scroll {category} later",
      // Plural; see the translator guide. {count}: bars in the chart.
      scroll_hint: "Scroll left or right to explore all {count:number|num} {{bar|bars}}.",
      plot_label: "{category} usage by date",
      // A short label on the chart's cleanup line.
      cutoff_marker: "removed at next cleanup",
      // {date}: the cutoff date.
      // Plural; see the translator guide. {days}: how long the retention policy keeps files.
      cutoff_note: "Files dated on or before {date} are removed at the next cleanup (kept for {days:number|num} {{days:day|days}}).",
      // Plural; see the translator guide. {count}: files.
      files: "{count:number|num} {{file|files}}",
      // {range}: a date or range. {size}: e.g. "1.5 GB". {files}: `files`, "1 file" or "3 files".
      bar_label: "{range}: {size}, {files}",
      bar_hint: "Hover, focus or tap a bar for its dates and exact usage.",
      data_summary: "View chart data",
      data_caption_lifecycle: "{category} · bytes by UTC lifecycle date",
      data_caption_published: "{category} · bytes by publication or recording date",
      column_range: "Date range",
      column_bytes: "Bytes",
      column_files: "Files",
      error_invalid_dates: "Choose valid start and end dates.",
      error_end_before_start: "The end date must be on or after the start date.",
      // {max}: 9999.
      error_precision: "Days per bar must be a whole number from 1 to {max}.",
    },
  },
  // operator/ncSetup.ts: errors from setup steps run in the administrator's
  // browser (recording storage setup, the service account's password).
  nc_setup: {
    confirmation_unavailable: "Nextcloud's password confirmation isn't available on this page.",
    confirmation_cancelled: "Password confirmation was cancelled.",
    // {message}: Nextcloud's own error, usually in English.
    rejected: "Nextcloud rejected the change: {message}",
    rejected_confirmation:
      "Nextcloud rejected the change: {message}. Your password confirmation may have expired, so try again.",
    setup_unavailable: "Setup can't run on this page because Nextcloud's scripts aren't loaded here.",
    password_unavailable: "The password can't be changed on this page because Nextcloud's scripts aren't loaded here.",
    // {action}: an internal step name, e.g. "create_user".
    unknown_step: "This version of Cassini can't run the \"{action}\" step.",
  },
  // RecordingSetup.svelte: Operator › Doctor.
  doctor: {
    title: "Doctor",
    run_all: "Run all checks",
    checking: "Checking…",
    intro: "What Cassini needs to record and search meetings, and what to do when something needs attention.",
    refresh_failed: "Couldn’t refresh the checks",
    trying: "Trying…",
    // {age}: how long ago, e.g. "6 minutes ago" or "just now".
    last_checked: "Last checked {age}",
    checked: "Checked {age}",
    // A test recording someone played back. {age} as above.
    confirmed: "Confirmed {age}",
    // The heading, then a follow-up drawn as a link to the test recording row.
    // {verdict}: Doctor's heading. {follow_up}: one of
    // readiness.test_follow_up (mid-sentence; use your language's capitalisation).
    verdict_and_follow_up: "{verdict}; {follow_up}",
    // When the checks passed but the test recording failed.
    verdict_but_follow_up: "{verdict}, but {follow_up}",
    // Small state words beside a row's name.
    waiting: "Waiting",
    reindexing: "Re-indexing",
    running: "Running",
    open_runs: "Open the runs",
    raise_limit: "Raise the limit",
    // {account}: the account name, shown as code. Keep it.
    raise_limit_body:
      "Nextcloud lets each account create 20 shares every 10 minutes, and Cassini shares every recording from its {account} account. These commands raise the limit for every account's share creation:",
    what_to_do: "What to do about it",
    open_test_room: "Open test room",
    see_why_stopped: "See why it stopped",
    prepare_test: "Prepare test",
    prepare_new_test: "Prepare a new test",
    play_recording: "Play the recording",
    open_talk_settings: "Open Talk settings",
    open_talk_admin: "Open Talk's administration settings",
    test_picks_up: "Cassini picks the recording up by itself, so there is no need to reload this page.",
    reindex_updates: "This row updates by itself when re-indexing finishes, so there is no need to reload this page.",
    confirm_playback: "I played the recording and could hear the audio",
    // The form for Talk's signaling server secret.
    secret: {
      title: "Connect to Talk’s signaling server",
      intro:
        "Use the signaling server’s internal client secret. This is different from the recording-backend secret, which Cassini generates itself.",
      // {variable}: an environment variable name, shown as code. Keep it.
      managed: "Managed by deployment configuration. Change {variable} in Cassini’s deploy options.",
      saved: "An internal secret is saved. Enter a new value to replace it.",
      not_saved: "No internal secret is saved.",
      label: "Internal secret",
      save: "Save secret",
      why: "This is the internal secret your Talk signaling server is configured with. Cassini cannot read it from Talk, which is why it is asked for here.",
    },
    backend: {
      title: "Use Cassini as Talk’s recording backend",
      intro:
        "Talk needs Cassini's recording-server URL and its recording secret. Cassini generates the secret itself but cannot write Talk's configuration, so the values have to be given to Talk.",
      show_values: "Show both values",
    },
    test: {
      title: "Record a test through Talk",
      intro:
        "Cassini makes itself a conversation for this and waits. The recording is started from Talk, by you, exactly as a real one would be — which is what makes it worth running.",
      // The four steps. “Cassini recording test” is the room's name, which
      // Cassini creates in English.
      step_prepare:
        "Press Prepare test. Cassini creates a conversation named “Cassini recording test” in your name, so that you can moderate it — Talk’s Start recording action belongs to a conversation’s moderators.",
      step_record: "Open the room, join the call, and use Talk’s Start recording action.",
      step_speak: "Say a few words, then stop the recording in Talk.",
      step_play: "Wait for it to publish, then play the audio and confirm you can hear it.",
      // Read by screen readers before a finished step.
      step_done: "Done:",
      note: "A recording captures a call, so the call needs someone in it: Cassini joins to record, not to talk. The conversation and the test recording are both ordinary ones, and can be deleted afterwards.",
      // {stage} and {state}: the operator's internal names, e.g. "build: running".
      status: "{stage}: {state}",
      stage_unknown: "Test",
      // {id}: the recording's job id.
      job: "Recording {id}",
      open_published: "Open published recording",
      confirm_published: "I played the published audio",
    },
    settings_note:
      "Review optional transcription below in Publish pipeline. Recording and playback can work without a transcript. CPU transcription is supported; a GPU is optional.",
    repair_config: {
      title: "Cassini’s saved configuration cannot be read",
      // {file}: a file name.
      body: "Ask your server administrator to check Cassini’s persistent volume and restore {file} from backup, then restart Cassini and check again.",
    },
  },
  // Operator.svelte: the operator surface's left nav and the Recordings
  // console (the run list and a run's detail).
  console: {
    // The browser tab's title and description of the standalone page.
    page_title: "Cassini Control Panel",
    page_description: "Operator control panel for Cassini jobs, history, and selected-run inspection.",
    // The left nav: small uppercase group headings, then rows.
    nav: {
      group_console: "Console",
      group_diagnostics: "Diagnostics",
      group_settings: "Settings",
      group_support: "Support",
      recordings: "Recordings",
      support: "Support Cassini",
      // On narrow screens the nav is a drawer behind a button.
      open_label: "Choose an Operator section",
      close_label: "Close the section list",
      label: "Operator sections",
    },
    title: "Recordings",
    // The connection badge beside the title.
    connection: {
      live: "Live",
      connecting: "Connecting",
      reconnecting: "Reconnecting",
      offline: "Offline",
      unknown: "Unknown",
    },
    // On hover over the connection badge; mid-sentence; use your language's capitalisation.
    stream: {
      live: "live",
      connecting: "connecting…",
      reconnecting: "reconnecting…",
      // {seconds}: a number of seconds, e.g. 2.
      auto_refreshing: "auto-refreshing every {seconds}s",
      disconnected: "disconnected",
      idle: "idle",
    },
    // {seconds}: a number of seconds, e.g. 2.
    checking_every: "Checking every {seconds}s",
    retrying_every: "Retrying every {seconds}s",
    record_meeting: "Record a meeting",
    meeting_link: "Meeting link",
    starting: "Starting…",
    start_recording: "Start recording",
    meeting_url_required: "Meeting URL is required.",
    // The run list.
    list_heading: "Recordings",
    refresh_jobs: "Refresh jobs",
    loading_jobs: "Loading jobs…",
    empty_title: "No recordings yet",
    // “Record a meeting” is the button above; use its translation.
    empty_body: "Start one with “Record a meeting”.",
    // A badge on a run in the list once it has been retried. {number}: the
    // attempt number, 2 or more.
    attempt_badge: "Attempt {number}",
    // A build waiting for resources. {time}: a date and time.
    // Plural; see the translator guide. {count}: how many times the build was put off.
    next_retry_deferrals: "Next retry {time} · {count:number|num} {{count:deferral|deferrals}}",
    cleanup_hint: "Transcription-only publication; source media is deleted after processing.",
    source_expired_badge: "Recording deleted",
    source_expired_title: "The source recording was deleted by retention. This job can no longer be rerun.",
    meeting_deleted_badge: "Meeting deleted",
    meeting_deleted_title:
      "The published meeting was deleted by a user. Its local artifacts are reclaimed and this job can no longer be rerun.",
    not_shared_badge: "Not shared with everyone",
    // On hover over the badge above.
    // Plural; see the translator guide. {count}: people or groups.
    not_shared_title:
      "Not shared with {count:number|num} {{recipient|recipients}}: Nextcloud refused them or its share limit held them back. Open the run to see who.",
    // A run's name when Talk's conversation name is unknown. {token}: the
    // room token; {id}: the start of the run id.
    call_label: "Call {token}",
    recording_label: "Recording {id}",
    recording_unnamed: "Recording",
    // How the run was captured.
    capture_mode: {
      audio_only: "Audio only",
      audio_video: "Audio and video",
      unknown: "Unknown (older recording)",
    },
    // Why a recording stopped.
    stop_reason: {
      operator_requested: "Stopped by operator",
      room_empty: "Room empty",
      duration_limit: "Duration limit reached",
      signaling_connection_error: "Signaling connection error",
      join_failed: "Failed to join room",
      record_process_exit_nonzero: "Process exited with error",
      no_audio_captured: "No audio captured",
    },
    // A failed run that captured no audio at all.
    no_audio: {
      headline: "No audio was captured. Were all participants muted?",
      detail: "This recording has no audio to transcribe or publish.",
    },
    // A run's or an attempt's status. Each describes the run (one recording's
    // trip through the pipeline) or one attempt at it; agree with that noun if
    // your language needs to.
    status: {
      no_audio_captured: "No audio captured",
      build_blocked: "Build blocked",
      waiting_for_resources: "Waiting for resources",
      failed: "Failed",
      stopped: "Stopped",
      interrupted: "Interrupted",
      published: "Published",
      queued: "Queued",
      writing_summary: "Writing summary",
      running: "Running",
      // A step that is running, named for its step (run_stages.stage).
      recording: "Recording",
      building: "Building",
      sealing: "Sealing",
      publishing: "Publishing",
    },
    // What became of the source media of a transcript-only run.
    cleanup: {
      waiting: "Media deletion after processing",
      pending: "Media deletion pending",
      error: "Media deletion failed",
      completed: "Media deleted",
      body: "Transcription-only publication. Source media is deleted after processing finishes or fails.",
      // {time}: when it finished, as the operator gives it.
      completed_at: "Completed: {time}",
      retry: "Retry media deletion",
      retry_requested: "Retry requested. Cleanup is checked within 30 seconds.",
    },
    summary_changed: "The meeting changed since this was loaded. Check it and try again.",
    rerun_blocked_expired: "The source recording was deleted by the retention policy. This job can no longer be rerun.",
    rerun_no_recording: "This run produced no recording to rerun from.",
    // On hover over the Build step. {span}: a duration, e.g. "2m".
    build_includes_summary: "Includes {span} waiting on the summary model",
    // On hover over a step taken from an earlier attempt. {attempt}: its number.
    from_attempt: "From attempt {attempt}",
    // Under a step that has not finished; each describes the step.
    unfinished: {
      active: "In progress",
      queued: "Queued",
      blocked: "Blocked",
    },
    not_available: "Not available",
    // The run detail.
    back_to_recordings: "Back to recordings",
    detail_heading: "Run detail",
    detail_subtitle: "Status and history for the selected recording",
    stopping: "Stopping…",
    stop: "Stop",
    rerunning: "Rerunning…",
    rerun: "Rerun",
    no_selection_title: "No recording selected",
    no_selection_body: "Choose one from the list.",
    // After the status and a " · "; mid-sentence; use your language's capitalisation. {age}: e.g. "now" or "6 min ago".
    updated_ago: "updated {age}",
    next_retry: "Next retry",
    resource_deferrals: "Resource deferrals",
    exit_code: "Exit code",
    reason: "Reason",
    stop_requested: "Stop requested",
    recording_stopped: "Recording stopped",
    // Field names in the run detail: small uppercase labels.
    field: {
      recorded: "Recorded",
      processing: "Processing",
      completed: "Completed",
      speech_model: "Speech model",
      summary_model: "Summary model",
      capture: "Capture",
      shared_with: "Shared with",
      stopped_because: "Stopped because",
      conversation: "Conversation",
      recording: "Recording",
      run_id: "Run id",
      created: "Created",
      updated: "Updated",
      interrupted: "Interrupted",
      queued: "Queued",
    },
    open_in_talk: "Open in Talk",
    open_in_files: "Open in Files",
    technical_details: "Technical details",
    attempt_history: "Attempt history",
    // The heading of one attempt in the run detail. {number}: the attempt number.
    attempt: "Attempt {number}",
    // What started an attempt, after its number ("Attempt 2 · Rerun"): the
    // first run, a rerun, a summary written later, or a refine after speaker
    // edits. Each describes the attempt.
    trigger_kind: {
      initial: "Initial",
      rerun: "Rerun",
      summarize: "Summarize",
      refine: "Refine",
    },
    // After an attempt's status and a " · ". {span}: a duration, e.g. "12m".
    processed_in: "processed in {span}",
    recorded_for: "recorded {span}",
    // On hover over the speech model. {revision}: a revision id.
    revision: "Revision {revision}",
    // An attempt's log buttons, one per step.
    log_name: {
      record: "Record log",
      build: "Build log",
      seal: "Seal log",
      publish: "Publish log",
    },
    show_log: {
      record: "Show the record log",
      build: "Show the build log",
      seal: "Show the seal log",
      publish: "Show the publish log",
    },
    log_gone: "No longer on disk: retention or a cleanup removed it",
    log_not_part: "Not part of this attempt: only the first one records",
    log_not_written: "This step has not written a log",
    // Beside the log's name, after a " · ", when only its last part is shown.
    log_truncated: "the end of it",
    download: "Download",
    close_log: "Close the log",
    loading: "Loading…",
    log_empty: "This step wrote nothing to its log.",
    // Where an attempt's files sit inside the operator's container.
    path: {
      run: "Run artifact",
      meeting: "Meeting artifact",
      opus: "Sealed meeting",
      site: "Site artifact",
    },
    // After a path.
    no_longer_on_disk: "(no longer on disk)",
  },
  // SupportBundle.svelte and support/client.ts: the support bundle download in
  // Browse › Help and Operator › Help.
  support_bundle: {
    region_label: "Cassini support",
    // Above the scrub choice in Operator › Help: what the installation's bundle holds.
    lede_admin:
      "The bundle covers the whole installation: versions and settings, the last seven days of jobs, stage logs and sharing events. It has no recordings, transcripts or summaries.",
    // Above the scrub choice in Browse › Help: what a reader's own bundle holds.
    lede_user:
      "The bundle includes your Nextcloud user ID, your browser details, the recordings shared with you and any error messages. It has no meeting content and no server logs.",
    scrub: "Scrub sensitive information",
    // Under the scrub choice, for the installation's bundle.
    scrub_hint_admin:
      "Logs can include names, URLs, passwords or meeting text. Scrubbing removes them, but may also remove details needed to find the problem.",
    // Under the scrub choice, for a reader's own bundle.
    scrub_hint_user: "Leaves out your user ID and the error messages. Counts and status codes stay.",
    download: "Download support bundle",
    preparing: "Preparing support bundle…",
    // Small print under the download button.
    fine_print: "Nothing is sent automatically. Check the ZIP before you share it.",
    downloaded_admin_scrubbed: "Administrator support bundle downloaded. Privacy filtering was on.",
    downloaded_admin_original: "Administrator support bundle downloaded. Privacy filtering was off.",
    downloaded_user_scrubbed: "Support bundle downloaded. Privacy filtering was on.",
    downloaded_user_original: "Support bundle downloaded. Privacy filtering was off.",
    // A box under a reader's own bundle in Browse › Help, shown only to an administrator.
    admin_heading: "Administrators",
    admin_body:
      "This bundle covers only your own account. For a problem with recording or processing, download the installation bundle instead: it adds server logs and job history.",
    // {place} is where the installation bundle is, e.g. "Operator › Help".
    admin_link: "Open {place}",
    error_timeout: "The support download timed out. Try again.",
    error_unreachable: "Couldn’t reach Cassini. Check your connection and try again.",
    error_sign_in: "Sign in again, then retry the download.",
    error_forbidden: "Your account cannot download this support bundle.",
    error_unavailable: "Support bundles aren’t available on this Cassini installation yet.",
    error_busy: "Another support bundle is being prepared. Try again shortly.",
    error_failed: "Couldn’t prepare the support bundle. Try again.",
    error_not_a_bundle: "Cassini didn’t return a support bundle. Sign in again and retry.",
    error_incomplete: "The support bundle download was incomplete. Try again.",
  },
  // HelpPanel.svelte: Operator › Help, the installation's support bundle.
  help_panel: {
    title: "Help",
    intro: "Recording failing, or a meeting not showing up? Download a support bundle and attach it to your support request.",
    bundle_heading: "Support bundle",
  },
  // SupportPanel.svelte: Operator › Support Cassini.
  support_panel: {
    title: "Support Cassini",
    intro: "Cassini is built by Codemyriad, a small, independent team. Here's how you can help the project, and how we can help you.",
    fund_heading: "Fund its development",
    fund_body:
      "Cassini is free software, and every feature will stay free. If it's useful to you, you can support its development with any amount from €2. It unlocks no features, just some good karma.",
    // Button that opens the payment page.
    fund_button: "Support Cassini",
    fund_fine_print: "Payment is handled by Stripe and goes to Codemyriad, the team that builds Cassini.",
    setup_heading: "Running Cassini for an organisation?",
    setup_body: "We help with setup and support. Book a 30-minute call to talk it through.",
    setup_button: "Book a call",
  },
};

export default operator;
