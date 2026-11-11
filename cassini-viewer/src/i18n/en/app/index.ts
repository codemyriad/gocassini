import type { BaseTranslation } from "../../i18n-types.js";
import doctor_checks from "./doctor_checks.js";
import insights from "./insights.js";
import operator from "./operator.js";
import settings from "./settings.js";
import shell from "./shell.js";

// The Nextcloud shell (cassini-app): navigation, settings and the operator
// surface.
const en_app = {
  shell,
  operator,
  doctor_checks,
  settings,
  insights,
  // The kinds of file Cassini keeps, as Storage charts them and Retention sets
  // how long each is kept. failed_capture to failed_publish are the kinds of
  // previous attempt.
  file_kind: {
    published: "In Nextcloud Files",
    recordings: "Recordings",
    current: "Published meetings (on this server)",
    history: "Previous attempts",
    failed_capture: "Failed recordings",
    failed_build: "Failed processing",
    superseded: "Replaced output",
    failed_publish: "Failed publishing",
    logs: "Processing logs",
    other: "Other local files",
  },
  // The speech quality tiers, in Settings and in the pipeline's status strip.
  quality: {
    fast: "Fast",
    balanced: "Balanced",
    best: "Best",
  },
  // What media stays on the server after publishing, in Settings and in the
  // pipeline's status strip.
  keep: {
    nothing: "Nothing",
    audio_video: "Audio and video",
    audio_only: "Audio only",
  },
} satisfies BaseTranslation;

export default en_app;
