import type { BaseTranslation } from "../../i18n-types.js";
import browse from "./browse.js";
import insights from "./insights.js";
import meeting from "./meeting.js";

// The viewing layer: the meeting list, a meeting, its transcript and insights,
// and the public <cassini-meeting> embed.
const en_viewer = {
  browse,
  meeting,
  insights,
  embed: {
    badge: "Recorded with Cassini",
  },
  export: {
    button: "Export",
    menu_label: "Export this meeting",
    copy_full_context: "Copy full context",
    download_full_context: "Download full context",
    copy_transcript: "Copy transcript",
    copy_transcripts: "Copy transcripts",
    download_transcript: "Download transcript",
    download_transcripts: "Download transcripts",
    download_audio: "Download audio",
  },
} satisfies BaseTranslation;

export default en_viewer;
