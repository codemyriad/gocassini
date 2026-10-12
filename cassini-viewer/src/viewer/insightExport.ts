import { formatMeetingDateWithDay, type MeetingCatalogEntry } from "./catalog";
import {
  insightHeadline,
  insightTimestampMs,
  stripInsightFrontMatter,
  type InsightRecord,
} from "./insights";

// The offline copy of an insight (D-792): what the drawer shows, as one
// markdown file that still makes sense once it has left Cassini.
//
// Built from what the drawer already holds rather than fetched again, so the
// copy is the answer the reader is looking at. The document's YAML front matter
// is cut for the reason the drawer cuts it, and the facts a reader needs are
// put back as a header written for a person.
//
// The sources are the ones resolved for THIS reader. A copy is something they
// hand on, and naming a meeting they cannot see — or counting it — would let
// the copy disclose what the drawer was careful not to.

export interface InsightExportInput {
  record: InsightRecord;
  sources: readonly MeetingCatalogEntry[];
  markdown: string;
  // Where a source opens in this Cassini, or "" where there is no such place.
  // Injected so this stays pure: the shell owns the URL. Someone with access
  // follows it to the meeting; anyone else gets the app's own refusal.
  meetingUrl: (source: MeetingCatalogEntry) => string;
}

export function renderInsightExport({
  record,
  sources,
  markdown,
  meetingUrl,
}: InsightExportInput): string {
  // One line: the headline can be a question typed over several, and a line
  // break in an H1 leaves the rest as a stray paragraph.
  const blocks: string[] = [`# ${oneLine(insightHeadline(record))}`];

  const question = record.question?.trim() ?? "";
  if (question !== "") {
    const quoted = `"${question}"`.split(/\r?\n/).map((line) => `> ${line}`.trimEnd());
    blocks.push(quoted.join("\n"));
  }

  // Template and model, and not the provider or the attempt: those say how the
  // run was carried out, not what produced the words, and mean nothing to
  // whoever this is passed to.
  const template = [record.workflowId, record.workflowVersion].filter(Boolean).join(" ");
  const provenance = [`Template: ${template}`];
  if (record.model) {
    provenance.push(`Model: ${record.model}`);
  }
  blocks.push(`Written by Cassini on ${formatExportCreated(record)}  \n${provenance.join(" · ")}`);

  if (sources.length > 0) {
    const noun = sources.length === 1 ? "meeting" : "meetings";
    const items = sources.map((source) => {
      // A title is free text; a line break in a list item ends the item.
      const label = `${oneLine(source.title)}, ${formatMeetingDateWithDay(source.dateLabel)}`;
      const url = meetingUrl(source);
      return url ? `- [${escapeLinkText(label)}](${url})` : `- ${label}`;
    });
    blocks.push(`Drawn from ${sources.length} ${noun}:\n\n${items.join("\n")}`);
  }

  blocks.push("---");
  blocks.push(stripInsightFrontMatter(markdown).trim());

  return `${blocks.join("\n\n")}\n`;
}

// A meeting title is free text, and an unescaped bracket in it ends the link
// early.
function escapeLinkText(text: string): string {
  return text.replace(/[\\[\]]/g, (char) => `\\${char}`);
}

function oneLine(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}

// The drawer shows createdAt in the reader's clock with no zone, which is right
// on screen (formatInsightCreated). A copy is read somewhere else, by someone
// whose clock this may not be, so here the zone goes with it.
const EXPORT_DATE_FORMAT = new Intl.DateTimeFormat("en-GB", {
  day: "numeric",
  month: "short",
  year: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
  timeZoneName: "short",
});

function formatExportCreated(record: InsightRecord): string {
  const ms = insightTimestampMs(record);
  return ms === null ? (record.createdAt ?? "") : EXPORT_DATE_FORMAT.format(new Date(ms));
}

const MAX_FILE_STEM = 100;

// Names Windows reserves whatever the extension: "CON.md" cannot be created.
const RESERVED_FILE_STEM = /^(con|prn|aux|nul|com\d|lpt\d)$/i;

// exportFileName names the copy after the insight, as something every file
// system will take: the headline is usually a question, and questions carry
// the very characters Windows refuses.
export function exportFileName(record: InsightRecord): string {
  const cleaned = insightHeadline(record)
    // eslint-disable-next-line no-control-regex
    .replace(/[/\\:*?"<>|\u0000-\u001f\u007f]/g, " ")
    .replace(/\s+/g, " ")
    .trim();
  // Cut by code point, not by UTF-16 unit: a cut through an emoji leaves a
  // lone surrogate that browsers replace or refuse in a file name.
  const stem = Array.from(cleaned)
    .slice(0, MAX_FILE_STEM)
    .join("")
    .trim()
    // A trailing dot is dropped by Windows, leaving "name..md" or a hidden file.
    .replace(/[.\s]+$/, "");
  return stem === "" || RESERVED_FILE_STEM.test(stem) ? "insight.md" : `${stem}.md`;
}
