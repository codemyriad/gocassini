// Insights in the shell: insight templates and generating insights.
//
// Keys are grouped by component or module, in snake_case. See docs/translations.md.
const insights = {
  // The "Create an insight" card in the Prepare panel.
  generate_card: {
    region_label: "Create an insight",
    title: "Create an insight",
    intro: "A written answer drawn from these meetings, saved alongside the meeting bundle rather than inside it.",
    name_label: "Name",
    name_hint: "You can rename it later from the insight.",
    template_label: "Insight template",
    // A small link beside the template picker, for administrators.
    edit_templates: "Edit templates",
    question_label: "Question",
    question_placeholder: "What should this insight answer?",
    // Shown when the list of templates is not there to pick from.
    configured_template: "This runs the template your administrator configured for insights.",
    templates_unlisted: "Templates could not be listed. The configured one runs.",
    // A folded section for choosing where the question is sent.
    provider_and_model: "Provider and model",
    provider_label: "Provider",
    model_label: "Model",
    // Placeholder in the model box: empty means the provider's own default model.
    model_placeholder: "endpoint default",
    generate: "Generate insight",
    starting: "Starting…",
    question_missing: "This template asks whatever you type above, so it needs a question.",
    where_it_goes:
      "These meetings' transcripts are sent to the AI provider to be read. The insight comes back as a document in your Nextcloud files.",
    // {count} is how many picked recordings have no words.
    textless_one:
      "1 selected recording has no transcript text. Remove it from the selection to generate an insight. Its audio is still available.",
    textless_other:
      "{count} selected recordings have no transcript text. Remove them from the selection to generate an insight. Their audio is still available.",
  },
  // Operator › Insight templates.
  templates_panel: {
    title: "Insight templates",
    intro: "Used for insights and meeting summaries. Write your own, or duplicate a built-in one and change it.",
    reload_label: "Reload insight templates",
    editor_new_label: "New insight template",
    editor_edit_label: "Edit insight template",
    new_template: "New template",
    // After "New template", when it starts as a copy. {name} is the original template's name.
    copy_of: "a copy of {name}",
    // The name given to a duplicated template. {name} is the original's name.
    copy_name: "{name} (copy)",
    name_label: "Name",
    description_label: "Description",
    description_hint: "Shown to the person choosing it, so they know what it does.",
    prompt_label: "Prompt",
    prompt_hint: "The instruction the model receives, together with the transcripts.",
    // {placeholder} is the literal text {{QUESTION}}, shown in code style.
    prompt_question_hint:
      "Put {placeholder} where a question should go, and people will be asked for one when they pick this template.",
    // An example prompt, shown in the empty prompt box. Keep "## Risks" as a
    // Markdown heading.
    prompt_example:
      "You read meeting transcripts and list the risks people raised.\n\nWrite a Markdown document with one heading, ## Risks, and under it one bullet per risk:\nwhat the risk is, who raised it, and what was agreed about it.\nIf nobody raised a risk, say so in one line.",
    // {placeholder} is the literal text {{TEMPLATE}}.
    template_placeholder_problem:
      "A custom template has no place for {placeholder}. Write the shape of the document into the prompt instead.",
    // Before the template's file name, e.g. "File: risks.md".
    file_label: "File:",
    // {file} is a file name, shown in code style.
    cannot_save_under_name:
      "{file} cannot be saved under its own name. Saving keeps this as a new template and removes that file.",
    // {file} is a file name; {error} is a technical reason, in English.
    replace_remove_failed: "Saved as a new template, but {file} could not be removed: {error}",
    save: "Save",
    saving: "Saving…",
    cancel: "Cancel",
    edit: "Edit",
    duplicate: "Duplicate",
    delete: "Delete",
    deleting: "Deleting…",
    // {file} is a file name, shown in code style; {error} is the server's reason, in English.
    cannot_run: "{file} cannot run: {error}",
    // The toggle that shows a template's prompt.
    prompt_toggle: "Prompt",
    // After "Prompt", for a template that asks the person for a question.
    asks_for_question: "asks for a question",
    needs_provider: "Add a provider to use these templates",
    load_failed: "Templates could not be listed.",
    loading: "Loading insight templates…",
    none_shipped: "This build ships no templates.",
    custom_heading: "Your templates",
    custom_none: "None yet. Start a new one, or duplicate a built-in template below.",
    // Heading over the templates that come with Cassini; a count follows it.
    built_in_heading: "Built in",
    built_in_note: "Shipped with Cassini. Duplicate one to change it.",
    // {name} is a template's name, shown in bold.
    delete_title: "Delete {name}?",
    in_use_summary:
      "This template is being used for generating summaries after calls are transcribed. Replace it with another template before deleting it.",
    in_use_insight:
      "This template is being used as the default for new insights. Replace it with another template before deleting it.",
    open_pipeline: "Open the Publish pipeline",
    ok: "OK",
    delete_note: "Insights that have already been produced from it will keep a reference to its name, id, version and hash.",
  },
  // The card that says an AI provider is needed, with a button to add one.
  needs_provider: {
    action: "Add a provider",
  },
  // Messages about requests for insights, templates and AI providers.
  client: {
    // {error} is the server's reason, in English.
    templates_failed_served: "The insight templates could not be listed: {error}",
    // {status} is an HTTP status number.
    templates_failed_status: "The insight templates could not be listed (HTTP {status}).",
    providers_failed_served: "The AI endpoints could not be listed: {error}",
    providers_failed_status: "The AI endpoints could not be listed (HTTP {status}).",
    models_failed_served: "This endpoint's model list could not be read: {error}",
    models_failed_status: "This endpoint's model list could not be read (HTTP {status}).",
    pick_a_meeting: "Pick at least one meeting first.",
    list_unreadable: "Cassini could not read the list of insights.",
    answer_unreadable: "Cassini could not read the operator's answer.",
    // {status} is a status word the app does not know.
    record_not_understood: "Cassini does not understand this insight record (status {status}).",
    request_unreadable: "Cassini could not read that request for an insight.",
    create_unavailable: "One of these meetings is not available to you, or this deployment cannot create insights.",
    list_unavailable: "Insights could not be listed.",
    rename_unavailable: "That insight is not available to you, or this deployment cannot rename insights.",
    insight_unavailable: "That insight is not available to you, or this deployment cannot create insights.",
    already_running: "This insight is already running.",
    // Cassini could not be reached. {detail} is empty, or the browser's reason
    // in brackets with a space before it, e.g. " (Failed to fetch)".
    unreachable: {
      create: "Could not reach Cassini to start the insight{detail}.",
      list: "Could not reach Cassini to list your insights{detail}.",
      read: "Could not reach Cassini to read the insight{detail}.",
      retry: "Could not reach Cassini to retry the insight{detail}.",
      rename: "Could not reach Cassini to rename the insight{detail}.",
    },
    // Cassini answered that it could not do it.
    cassini_failed: {
      create: "Cassini could not start the insight.",
      list: "Cassini could not list your insights.",
      read: "Cassini could not read the insight.",
      retry: "Cassini could not retry the insight.",
      rename: "Cassini could not rename the insight.",
    },
    // Any other failure. {status} is an HTTP status number.
    failed_status: {
      create: "Could not start the insight (HTTP {status}).",
      list: "Could not list your insights (HTTP {status}).",
      read: "Could not read the insight (HTTP {status}).",
      retry: "Could not retry the insight (HTTP {status}).",
      rename: "Could not rename the insight (HTTP {status}).",
    },
  },
  // Writing or rewriting a meeting's summary from Operator, and what Settings
  // says the summary step costs.
  summary_action: {
    write: "Write summary",
    regenerate: "Regenerate summary",
    confirm_write_title: "Write a summary for this meeting?",
    confirm_replace_title: "Replace this meeting's summary?",
    // What writing a summary costs. {target} is a model and provider, e.g.
    // "deepseek-v4 (Open Router)"; {tokens} is a rounded number, e.g. "7.5K";
    // {words} is a number.
    cost_one: "1 call to {target} · about {tokens} input tokens, {words} words",
    cost_other: "{count} calls to {target} · about {tokens} input tokens, {words} words",
    // {target} when no model is named.
    cost_target_fallback: "the summary model",
    // {model} with the provider it runs on.
    cost_target: "{model} ({provider})",
    endpoint_external: "The transcript is sent to an external service, which may bill for it.",
    endpoint_local: "The model runs on your own network.",
    replace_note: "Everyone who can open the meeting sees the new summary instead of the current one.",
    // "Settings › Publish pipeline" is the path to a settings page.
    summaries_off: "Summaries are off. Turn them on in Settings › Publish pipeline to write one.",
    no_local_copy: "This server no longer keeps a copy of the meeting, so a summary can't be written for this run.",
    // In Settings, under the summary step. {provider} is the AI provider's name.
    step_lead_local: "Sends each transcript to {provider} on your own network. One request per meeting.",
    step_lead_external: "Sends each transcript to {provider}. One request per meeting, which it may bill.",
  },
};

export default insights;
