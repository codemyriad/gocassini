// Operator settings: the publish pipeline, AI providers and models, retention and recording access.
//
// Keys are grouped by component or module, in snake_case. See docs/translations.md.
const settings = {
  // Operator › Publish pipeline: every step between a call ending and the meeting being published.
  pipeline: {
    heading: "Publish pipeline",
    intro: "Every step between the call ending and the meeting being published. These apply to every room.",
    loading: "Loading settings…",
    unavailable: "Settings aren't available.",
    after_meeting: "After each meeting",
    // Small uppercase label over the strip of saved settings.
    current: "Current",
    current_title: "The saved settings new recordings get",
    strip_label: "Current settings",
    // The four steps, as named on the strip's boxes. Short.
    chip_transcribe: "Transcribe",
    chip_summarise: "Summarise",
    chip_publish: "Publish",
    chip_retention: "Media retention",
    // Step 1: transcription.
    transcription_label: "Transcription settings",
    transcript_heading: "Transcript",
    tiers_label: "What transcribes recordings",
    off: "Off",
    off_description: "Recordings are published without a transcript.",
    // Quality tiers. Their names also fill {tier} in the sentences below.
    quality_fast: "Fast",
    quality_fast_description: "Fastest transcription, lower accuracy.",
    quality_balanced: "Balanced",
    quality_balanced_description: "Default — good accuracy at reasonable speed.",
    quality_best: "Best",
    // fp32 is a model precision; keep it.
    quality_best_description: "Highest accuracy (fp32), slowest.",
    // The line under the step heading. {tier} is a quality tier's name (Fast, Balanced, Best).
    lead_downloading: "Downloading the {tier} model. Transcription turns on when it finishes.",
    lead_off: "Nothing is transcribed. Install and pick a model to turn transcription on.",
    lead_on_unsaved: "Transcription is on. Save to apply it to new recordings.",
    lead_switched: "Now transcribing with the {tier} model. Save to apply it.",
    lead_switched_unnamed: "Now transcribing with the chosen model. Save to apply it.",
    lead_tier: "Transcribing every recording with the {tier} model.",
    lead_on: "Transcription is on.",
    gpu_same_model: "On the GPU, every quality setting uses the same full-precision model, so this only changes transcription on the CPU.",
    // {error} is why, a sentence of its own.
    inventory_refresh_failed: "Cannot refresh model status: {error}",
    loading_inventory: "Loading model inventory…",
    // {pack} and {import} are the commands "cassini models pack" and "cassini models import", drawn as code.
    downloads_disabled: "Downloads are disabled. Prepare a package with {pack} on a connected machine, copy it here, and run {import} inside this server's Cassini runtime. Imported models appear here automatically.",
    transcript_only_without: "Saved settings publish transcript-only meetings with no transcript. Save to publish the audio instead.",
    // Where transcription runs, used as {device} in the sentences below ("…on the CPU.").
    runs_on_gpu: "the GPU (CUDA)",
    runs_on_cpu: "the CPU",
    runs_on_cpu_no_gpu: "the CPU (no GPU found)",
    // The step's footer. {model} is a speech model's name, in bold; {device} is one of the runs_on_* phrases; {percent} is 0–100.
    in_use_missing: "{model} isn't installed, so recordings get no transcript.",
    in_use_stand_in_downloading: "Downloading {model}; the previous model transcribes recordings on {device} meanwhile.",
    in_use_stand_in_downloading_percent: "Downloading {model} ({percent}%); the previous model transcribes recordings on {device} meanwhile.",
    in_use_stand_in_failed_check: "{model} is installed but failed its runtime check; the previous model transcribes recordings on {device} meanwhile.",
    in_use_stand_in_missing: "{model} isn't installed; the previous model transcribes recordings on {device} meanwhile.",
    in_use_downloading: "Downloading {model}; recordings wait for it.",
    in_use_downloading_percent: "Downloading {model} ({percent}%); recordings wait for it.",
    in_use_transcribing: "Transcribing with {model} on {device}.",
    in_use_runs_on: "Transcription runs on {device}.",
    // Toggle that opens the hardware details.
    hardware: "Hardware",
    hw_gpu: "GPU",
    hw_gpu_found: "Found",
    hw_gpu_none: "None found",
    hw_cores: "CPU cores",
    hw_memory: "Memory",
    // {size} is an amount of memory such as "4.0 GB".
    hw_memory_wait: "Each recording waits for {size} free",
    // {size} is a number formatted for the reader.
    memory_gb: "{size} GB",
    memory_mb: "{size} MB",
    hw_device: "Device",
    // The automatic choice in the device select (the others are CPU and GPU (CUDA)).
    hw_device_auto: "Auto",
    hw_device_help: "Auto uses the GPU when there is one. Change it only if the hardware above is wrong.",
    hw_no_gpu_forced: "No GPU was found, so recordings won't be processed on this setting.",
    hw_cuda_tip: "For faster transcription, run Cassini's CUDA image on a server with a GPU.",
    // Step 2: the summary.
    summary_heading: "Summary",
    // Small pill beside the heading when transcription is off.
    needs_transcript: "needs a transcript",
    summarise_switch: "Summarise every meeting",
    // {error} is why, a sentence of its own.
    llm_load_failed: "Couldn't load the AI provider settings, so this step can't be shown or changed: {error}",
    needs_provider: "Needs a provider.",
    // Link-style button after needs_provider.
    add_provider: "Add one",
    field_provider: "Provider",
    field_template: "Insight template",
    template_shipped: "The workflow Cassini ships",
    templates_unlisted: "Templates could not be listed.",
    template_help: "Meetings published from now on are summarised with this template.",
    edit_templates: "Edit templates",
    // A saved template that cannot run. {id} is the template's id; {error} is the server's reason.
    template_cannot_run: "{id} cannot run: {error}",
    template_asks_question: "{id} cannot run: it asks for a question, and a summary has nobody to ask",
    template_not_in_build: "{id} — not in this build",
    // Placeholder of the model field. {model} is a model ID.
    provider_default_model: "{model} (the provider's default)",
    no_provider_default: "no default model set on this provider",
    // Step 3: what participants receive.
    publication_heading: "What participants receive",
    no_video_published: "Camera video is never published.",
    // Step 4: what media stays on the server.
    keep_heading: "What media stays on the server",
    keep_nothing: "Nothing",
    keep_nothing_help: "The recording is deleted after publishing and cannot be reprocessed.",
    keep_nothing_unavailable: "Unavailable while audio is published (step 3).",
    keep_video: "Audio and video",
    keep_video_help: "Uses the most space.",
    keep_audio: "Audio only",
    // An option and how long it is kept. {media} is keep_nothing, keep_video or keep_audio, in bold.
    kept_plain: "{media}.",
    kept_forever: "{media}, kept forever.",
    kept_days_one: "{media}, for 1 day.",
    kept_days_other: "{media}, for {count} days.",
    // Where the retention comes from. {storage} is a link to the Storage page (storage_link).
    source_forever: "Kept forever: change this in {storage}.",
    source_days_one: "The 1 day comes from {storage}.",
    source_days_other: "The {count} days come from {storage}.",
    source_unset: "How long kept media stays is set in {storage}.",
    storage_link: "Storage",
    must_transcribe: "Transcription must be on to keep nothing.",
    set_up_transcription: "Set up transcription",
    applies_to_new: "Applies to new recordings after saving.",
    // Search spellings: other spellings of names, for search.
    search_heading: "Search spellings",
    search_intro: "Common mishearings of a name, so searching for the name finds them too.",
    search_field: "One name per line, then its spellings, separated by commas",
    search_limit: "(up to 100 names, 25 spellings each)",
    search_note_transcripts: "Doesn't change any transcript, only what search finds.",
    search_note_results: "Results say when they matched one of these spellings.",
    search_note_when: "Use these spellings when the transcriber mishears a name.",
    // The bar at the bottom while there are unsaved changes.
    unsaved_region: "Unsaved changes",
    unsaved: "You have unsaved changes",
    saving_changes: "Saving changes…",
    saving: "Saving…",
    save: "Save",
    leave_title: "Leave without saving?",
    leave_prompt: "You have unsaved changes. Leave without saving?",
    stay: "Stay",
    leave: "Leave",
  },
  // Operator › Retention: how long each kind of file is kept.
  retention: {
    heading: "Retention",
    intro: "How long Cassini keeps each kind of file. Saving never deletes anything immediately.",
    recordings_heading: "Recordings",
    recordings_description: "Audio and video captured on this server. Deleting them means the meeting cannot be reprocessed.",
    current_heading: "Published meetings (on this server)",
    current_description: "The published meeting stays in Nextcloud Files after this copy is removed.",
    history_heading: "Previous attempts",
    history_description: "Failed recordings, failed processing, replaced output and failed publishing.",
    // Link-style button: switch from one policy for every kind of previous attempt to one each.
    split_kinds: "Set each kind separately",
    join_kinds: "Use one setting for all kinds",
    logs_heading: "Processing logs",
    logs_description: "Logs from each recording attempt.",
    nextcloud_heading: "Published meetings in Nextcloud",
    nextcloud_description: "Deletes the published meeting, including its transcript and notes.",
    schedule_heading: "Daily cleanup",
    // Followed by a time input and a timezone select: "Run at [03:00] [Europe/Paris]".
    run_at: "Run at",
    cleanup_time_label: "Cleanup time",
    timezone_label: "Timezone",
    loading: "Loading retention settings…",
    saved: "Retention settings saved.",
    operator_unavailable: "Operator unavailable",
    // Shown when a second save starts while one is still running.
    save_in_progress: "Retention settings are being saved. Try again.",
    // The kinds of previous attempt, each with its own policy when split.
    kinds: {
      failed_capture: "Failed recordings",
      failed_build: "Failed processing",
      superseded: "Replaced output",
      failed_publish: "Failed publishing",
    },
  },
  // One retention policy: keep forever, a preset number of days, or a custom number.
  retention_policy: {
    keep_forever: "Keep forever",
    // A preset button. {count} is 7, 30, 60 or 90, never 1.
    preset_days_other: "{count} days",
    custom: "Custom",
    // "Keep for [ 45 ] days": the two halves around a number input. Leave one empty if your language needs it.
    keep_for_before: "Keep for",
    keep_for_after: "days",
    // Screen-reader label of the number input. {label} is the kind of file, e.g. "Recordings".
    custom_days_label: "{label} days",
  },
  // The footer under the retention editor.
  retention_actions: {
    takes_effect: "Changes take effect at the next cleanup. Saving never deletes files immediately.",
    discard: "Discard changes",
    save: "Save",
    leave_title: "Leave without saving?",
    leave_prompt: "You have unsaved changes. Leave without saving?",
    stay: "Stay",
    leave: "Leave",
  },
  // The strip that asks an administrator to choose a retention policy.
  retention_reminder: {
    region_label: "Retention reminder",
    title: "Choose how long Cassini keeps recordings",
    keeps_forever: "Recordings and published meetings are kept indefinitely until you set a limit.",
    unsaved_first: "Save or discard your changes before dismissing.",
    review: "Review",
    dismiss: "Dismiss",
    dismissing: "Dismissing…",
    retry: "Retry",
    retrying: "Retrying…",
  },
  // Speech and voice separation model downloads.
  speech_models: {
    // {size} is a number already formatted for the reader, e.g. "61.9".
    size_mib: "{size} MiB",
    size_gib: "{size} GiB",
    // What a model download is doing right now.
    job_step: {
      queued: "Waiting to start",
      downloading: "Downloading",
      // {percent} is 0–100.
      downloading_percent: "Downloading · {percent}%",
      verifying: "Verifying the download",
      unpacking: "Unpacking",
      installed: "Downloaded · checks once no meeting is being processed",
      checking: "Checking it runs on this server",
    },
  },
  // The optional voice separation model's card in Settings.
  voice_separation: {
    heading: "Voice separation",
    // Next to the heading.
    optional: "(optional)",
    description: "For Cassini to tell apart several people talking on one device in a call, download this model. Anyone who can read the meeting can then split that device's audio into separate voices from its participant list. It runs on this server's CPU, only when someone asks.",
    state_ready: "Ready",
    state_unsupported: "Not available in this server's Cassini build",
    state_installed: "Installed — runtime check needed",
    state_cancelled: "Download cancelled. Retry resumes it.",
    state_not_installed: "Not installed",
    // Small buttons.
    cancel: "Cancel",
    check: "Check",
    retry: "Retry",
    download: "Download",
    progress_label: "Voice separation download progress",
  },
  // One speech model inside a quality row of the Transcript step.
  speech_model_cell: {
    state_ready: "Ready",
    state_installed_selected: "Installed · checked before the next recording",
    state_installed: "Installed · not checked on this build yet",
    state_cancelled: "Download cancelled. Retry resumes it.",
    state_not_installed: "Not installed",
    // Small buttons.
    cancel: "Cancel",
    check: "Check",
    retry: "Retry",
    download: "Download",
    // Badge on the model new recordings use.
    in_use: "In use",
    progress_label: "Model download progress",
    // {done} and {total} are sizes such as "120.5 MiB".
    progress: "{done} / {total}",
    // Part of the download progress line. {size} is a size such as "40.0 MiB" already on disk.
    reused: "{size} reused",
    carries_on: "You can close this page; it carries on.",
  },
  // The one time the service account's generated password is on screen.
  password_reveal: {
    dialog_label: "The Cassini service account's password",
    // {user} is the account name, drawn as code.
    save_now: "Save the password for the {user} account now.",
    // {user} is the account name, drawn as code.
    only_time: "This is the only time it's shown. Cassini doesn't store it and can't show it again. You only need it to sign in as {user} yourself; Cassini connects another way. If you lose it, set a new one here.",
    field_label: "Generated password",
    copy: "Copy",
    copied: "Copied",
    copy_blocked: "Your browser blocked copying. The password is selected above, so copy it with your keyboard.",
    // Disclosure over the occ command that sets a password on the server.
    set_on_server: "Or set one on the server instead",
    acknowledge: "I have saved it",
  },
  // Operator › Publish pipeline: the "Who can see recordings" card.
  direct_share: {
    heading: "Who can see recordings",
    intro: "Nextcloud Files controls who can read each recording.",
    point_participants: "Each recording is shared with all of the Talk room's participants, whether they were present on the call or not.",
    point_guests: "Guests without a Nextcloud account can't open it.",
    point_public_links: "Cassini never creates a public link. In public rooms, people can reshare the file if this Nextcloud allows it.",
    checking: "Checking recordings setup…",
    needs_account: "Cassini needs its recordings account before it can publish.",
    needs_attention: "Recordings setup needs attention.",
    create_account: "Create recordings account",
    creating: "Creating…",
    check_again: "Check again",
    account_created: "Recordings account created. Sharing is ready.",
    check_failed: "Could not check Nextcloud recordings setup.",
    create_failed: "Could not create the recordings account.",
  },
  // The model field: a text box that suggests the endpoint's models.
  model_combobox: {
    label: "Model",
    // Shown in the empty field: leaving it empty uses the endpoint's own default model.
    placeholder: "endpoint default",
    loading: "Loading models…",
    // {error} is why the list could not be read, a sentence of its own.
    list_failed: "{error} You can still type a model ID.",
    no_models: "This endpoint listed no models. Type a model ID.",
    // {text} is what was typed.
    no_match: "No match for “{text}”. It will be used as typed.",
    // Under a filtered list. {shown} models of {count}; {count} is always 2 or more here.
    match_count_other: "{shown} of {count} models",
  },
  // What participants receive after a meeting: audio and transcript, or the transcript alone.
  published_format: {
    // Screen-reader label of the select.
    field_label: "Published meeting",
    // .opus and .json are file extensions; keep them.
    opus_label: "Audio and transcript (.opus)",
    json_label: "Transcript only (.json)",
    opus_description: "One portable file: the recording with its transcript.",
    opus_description_no_transcript: "The recording as one portable file. No transcript while transcription is off.",
    json_description: "The transcript alone, no audio.",
    // Why the transcript-only option cannot be chosen. Step 1 is the Transcript step above.
    json_needs_transcript: "Needs transcription on in step 1.",
  },
  // Operator › AI providers: the endpoints summaries and insights are sent to.
  llm: {
    heading: "AI providers",
    // Badge beside the heading: recording and transcription need none of this.
    optional_badge: "Optional",
    subtitle: "Where Cassini sends transcripts for summaries and insights.",
    reload: "Reload AI providers",
    add_provider: "Add a provider",
    loading: "Loading AI providers…",
    unavailable: "No AI provider settings available.",
    no_client: "No operator client is available.",
    // The empty page explains what a provider is for.
    feature_summaries: "Summaries",
    feature_summaries_description: "Shown at the top of every meeting.",
    feature_insights: "Insights",
    feature_insights_description: "Answers drawn from meetings you choose.",
    only_for_ai: "Recording and transcription run on your own servers. A provider is only needed for summaries and insights.",
    // {place} is the name of the settings page, drawn in bold (publish_pipeline_place).
    first_save_turns_on: "Saving your first provider turns on summaries for every meeting. You can turn them off in {place}.",
    // Where the pipeline steps are switched on and off. Bold inside other sentences.
    publish_pipeline_place: "Operator › Publish pipeline",
    // Form headings. {name} is the provider's name or address.
    new_provider: "New provider",
    editing: "Editing {name}",
    field_provider: "Provider",
    // Example provider names.
    provider_placeholder: "OpenRouter, local Qwen…",
    field_base_url: "Base URL",
    // Example addresses; keep the URLs.
    base_url_placeholder: "https://openrouter.ai/api/v1 or http://your-host:8000/v1",
    field_api_key: "API key",
    // Small tags beside "API key".
    key_stored: "stored",
    key_will_be_removed: "will be removed",
    key_placeholder_keep: "Leave blank to keep the saved key",
    // sk-or-v1- is the literal start of an OpenRouter key; keep it.
    key_placeholder_new: "Paste the full API key (sk-or-v1-… for OpenRouter). Self-hosted servers usually don't need one.",
    openrouter_key_prefix: "OpenRouter keys start with sk-or-v1-. Check you pasted the whole key.",
    keep_saved_key: "Keep the saved key",
    remove_saved_key: "Remove the saved key",
    default_model: "Default model",
    // Example model IDs; keep them.
    model_placeholder: "e.g. openai/gpt-4o-mini or qwen3-30b",
    save_first_for_models: "Save the provider first to list its models.",
    // {detail} is why the list could not be read, without its final full stop.
    model_list_unavailable: "Model list unavailable: {detail}.",
    // {reason} is a whole model_list_unavailable sentence.
    probe_failed_hint: "{reason} You can still type a model ID.",
    request_limits: "Request limits",
    request_timeout: "Request timeout (s)",
    // {seconds} is the default timeout.
    timeout_placeholder: "{seconds} (default)",
    token_limit_field: "Response token limit",
    // {tokens} is the default limit.
    token_limit_placeholder: "{tokens} (default)",
    saving: "Saving…",
    save_provider: "Save provider",
    cancel: "Cancel",
    // A provider's card.
    probe_ok: "This endpoint answered with its model list",
    probe_failed: "This endpoint did not list its models",
    key_saved: "Key saved",
    no_key: "No key",
    // {model} is a model ID.
    model_named: "Model {model}",
    no_default_model: "No default model",
    model_count_one: "1 model",
    model_count_other: "{count} models",
    loading_models: "Loading models…",
    // {seconds} is a number of seconds.
    timeout_fact: "{seconds}s timeout",
    // {tokens} is a number of tokens.
    token_limit_fact: "{tokens} token limit",
    edit: "Edit",
    remove: "Remove",
    // The confirmation before a provider is removed.
    remove_confirm_label: "Confirm removing this endpoint",
    // {name} is the provider's name or address.
    remove_confirm_title: "Remove {name}?",
    remove_deletes_key: "The saved API key is deleted and can't be recovered.",
    // {place} is publish_pipeline_place, drawn in bold.
    remove_stops_summaries: "This stops meeting summaries until you choose another provider in {place}.",
    remove_stops_insights: "This stops insights until you choose another provider in {place}.",
    remove_stops_both: "This stops meeting summaries and insights until you choose another provider in {place}.",
    remove_keeps_summaries: "Meetings already published keep their summaries.",
    remove_confirm: "Yes, remove it",
    // Under the list. {provider} is a provider's name (medium weight), {model} a model ID (code).
    summary_uses: "Summaries use {provider}.",
    summary_uses_with_model: "Summaries use {provider} with {model}.",
    summaries_off: "Summaries are off.",
    // {place} is publish_pipeline_place, drawn in bold.
    change_in_pipeline: "Change this in {place}.",
    insight_default: "Whoever creates an insight picks which provider it uses. If they don't pick one, it uses {provider}.",
    insight_default_with_model: "Whoever creates an insight picks which provider it uses. If they don't pick one, it uses {provider} with {model}.",
    remove_all_turns_off: "Remove all providers to turn off both summaries and insights.",
  },
};

export default settings;
