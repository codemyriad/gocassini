// One recording, addressed by URL (D-775).
//
// The public embed shows a single meeting that nothing catalogued: there is no
// catalog.json and no operator, just a `.opus` at a URL. MeetingView already
// takes a MeetingCatalogEntry and loads it through the DataProvider, so all
// that is missing is the entry — which is derived here, from the URL, because
// the URL is the only thing the embedding page told us.

import { describeMeeting } from "./portable";
import type { MeetingCatalogEntry } from "./catalog";

// The file's own name, which is what the export flow and the operator both
// already use as a meeting id. Query and fragment are addressing, not identity,
// so they are dropped: the same recording behind a cache-buster is the same
// recording.
export function meetingIdFromUrl(src: string): string {
  // Parsed against a base so a relative `./talk.opus` and an absolute
  // `https://host/talk.opus` are read the same way, and so a host name is never
  // mistaken for a file name.
  let path: string;
  try {
    path = new URL(src, "http://embed.invalid/").pathname;
  } catch {
    path = src.split(/[?#]/)[0] ?? "";
  }
  const last = path.split("/").filter(Boolean).at(-1) ?? "";
  let name = last;
  try {
    name = decodeURIComponent(last);
  } catch {
    // A malformed escape is not a reason to refuse to show the meeting.
  }
  return name.replace(/\.opus$/i, "") || "meeting";
}

// describeMeeting reads a title and a date out of the id the way the static
// export does, so `Daily-Standup--2026-03-13--12-00-00.opus` embeds with the
// same heading it has in a published archive. A `title` attribute overrides it,
// because the page embedding a recording may know a better name for it than
// whoever named the file.
export function singleMeetingEntry(src: string, title = ""): MeetingCatalogEntry {
  const id = meetingIdFromUrl(src);
  const described = describeMeeting(id);
  return {
    id,
    title: title.trim() || described.title,
    dateLabel: described.dateLabel,
    audioPath: src,
  };
}

// The themes app.css declares. "auto" follows the embedding page's reader.
export type EmbedTheme = "saturn-light" | "saturn-dark";

export function resolveEmbedTheme(attribute: string | null, prefersDark: boolean): EmbedTheme {
  const asked = (attribute ?? "").trim().toLowerCase();
  if (asked === "light") return "saturn-light";
  if (asked === "dark") return "saturn-dark";
  return prefersDark ? "saturn-dark" : "saturn-light";
}
