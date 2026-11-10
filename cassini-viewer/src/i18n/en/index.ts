import type { BaseTranslation } from "../i18n-types.js";

// English is the source: edit these files, then run `npm run i18n:sync -w
// cassini-viewer`. Every other language is a JSON file that Weblate writes
// (docs/translations.md).
//
// `common` holds words both the shell and the viewing layer use. A string goes
// here only once a second surface needs it.
const en = {
  common: {
    // Buttons used across the app. Short: many sit in narrow rows.
    action: {
      // Closes a form or a question without doing anything.
      cancel: "Cancel",
      save: "Save",
      // A Save button while it works.
      saving: "Saving…",
      // After something failed.
      retry: "Retry",
      retrying: "Retrying…",
      // Hides a notice or an error.
      dismiss: "Dismiss",
      delete: "Delete",
      remove: "Remove",
      edit: "Edit",
      // Closes a panel or a sheet.
      close: "Close",
      // After a step failed: run it again.
      try_again: "Try again",
    },
    // Writing or rewriting a meeting's summary with a language model, from the
    // meeting itself and from the administrator's run detail.
    summary_request: {
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
    },
    // Who can see a recording: under "Who can see recordings" in Settings, and
    // beside a room in the meeting list.
    audience: {
      intro: "Nextcloud Files controls who can read each recording.",
      point_participants: "Each recording is shared with all of the Talk room's participants, whether they were present on the call or not.",
      point_guests: "Guests without a Nextcloud account can't open it.",
      point_public_links: "Cassini never creates a public link. In public rooms, people can reshare the file if this Nextcloud allows it.",
    },
    // The administrator's surface and its sections, as named in its navigation.
    // Sentences that send someone there take the path as {place}, written
    // "Operator › Publish pipeline" (operatorPlace in i18n/index.ts).
    place: {
      // The tab that holds the administrator's surface.
      operator: "Operator",
      publish_pipeline: "Publish pipeline",
      ai_providers: "AI providers",
      insight_templates: "Insight templates",
      doctor: "Doctor",
      storage: "Storage",
    },
    status: {
      // Briefly, after something was copied to the clipboard.
      copied: "Copied",
    },
  },
} satisfies BaseTranslation;

export default en;
