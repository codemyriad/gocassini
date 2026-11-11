import { get } from "svelte/store";

import { LL, locale } from "../i18n";
import { formatClockTime, judgedDisplaySegments, normalizeSpeakerLabel, UNKNOWN_SPEAKER_LABEL } from "../core/transcript";
import { tokensPreserveText } from "../core/wordInteraction";
import { buildTranscriptRows, repairTurnFinalWordInflation, sortBlocksInReadingOrder } from "../core/overlap";
import { buildDisplayTranscriptFromArtifacts } from "./portable";
import type { JudgedDisplaySegment } from "../core/transcript";
import type { LoadedArtifact } from "./loadArtifact";
import type { MeetingCatalogEntry } from "./catalog";
import { formatMeetingStartForExport } from "./meetingTime";

/** The same display projection used by the meeting reader, including its selected transcript. */
export function displaySegmentsForArtifact(artifact: Pick<LoadedArtifact, "index" | "readableTranscript" | "displayTranscript" | "wordEndsBoundedByAudio">): JudgedDisplaySegment[] {
  const { index, readableTranscript: readable, displayTranscript: display } = artifact;
  let segments: JudgedDisplaySegment[];
  if (display) {
    segments = judgedDisplaySegments(index, display);
  } else if (!readable) {
    segments = index.segments.map((segment) => ({
      id: segment.id,
      speaker: segment.speaker,
      speakerLabel: normalizeSpeakerLabel(segment.speakerLabel),
      startMs: segment.startMs,
      endMs: segment.endMs,
      text: segment.text,
      tokens: [],
      words: segment.words,
      sourceSegmentIds: [segment.id],
    }));
  } else {
    const projected = buildDisplayTranscriptFromArtifacts(index.transcript, readable);
    const canonicalById = new Map(index.segments.map((segment) => [segment.id, segment]));
    segments = readable.segments.map((segment, segmentIndex) => {
      const sourceSegments = segment.sourceSegmentIds
        .map((segmentId) => canonicalById.get(segmentId))
        .filter((value): value is NonNullable<typeof value> => Boolean(value));
      const speakerLabel = segment.speaker
        ? normalizeSpeakerLabel(index.speakersById.get(segment.speaker)?.label ?? segment.speaker)
        : normalizeSpeakerLabel(sourceSegments[0]?.speakerLabel ?? UNKNOWN_SPEAKER_LABEL);
      return {
        id: segment.id,
        speaker: segment.speaker,
        speakerLabel,
        startMs: segment.startMs,
        endMs: segment.endMs,
        text: segment.text,
        tokens: tokensPreserveText(segment.text, projected.blocks[segmentIndex]?.tokens ?? [])
          ? projected.blocks[segmentIndex]!.tokens
          : [],
        words: sourceSegments.flatMap((sourceSegment) => sourceSegment.words),
        sourceSegmentIds: [...segment.sourceSegmentIds],
      };
    });
  }
  return sortBlocksInReadingOrder(repairTurnFinalWordInflation(segments, {
    endsBoundedByAudio: artifact.wordEndsBoundedByAudio,
  }));
}

/**
 * A speaker's name as a reader sees it. The transcript code marks a passage
 * with no speaker with UNKNOWN_SPEAKER_LABEL, in English, and compares against
 * it; it is put in the reader's language only where it is shown.
 */
export function shownSpeakerLabel(label: string): string {
  return label === UNKNOWN_SPEAKER_LABEL ? get(LL).viewer.meeting.transcript.unknown_speaker() : label;
}

/** Names as one list in the reader's language: "Ben and Cara", "Ana, Ben and Cara". */
export function formatNameList(labels: readonly string[]): string {
  const names = labels.map(shownSpeakerLabel);
  try {
    return new Intl.ListFormat(get(locale), { style: "long", type: "conjunction" }).format(names);
  } catch {
    return names.join(", ");
  }
}

/** Markdown follows the reader's turn order and its inline interjections. */
export function transcriptMarkdown(entry: Pick<MeetingCatalogEntry, "title" | "dateLabel" | "recordedAtUtc"> | null, segments: JudgedDisplaySegment[]): string {
  // The speaker lines (`**name** [m:ss]`) keep their shape in every language;
  // only the words a person reads are translated.
  const t = get(LL).viewer.meeting.export;
  // An instant start is written in the reader's clock with its zone named,
  // since the copy is read elsewhere; a label-only start keeps its raw digits.
  const when = entry?.recordedAtUtc ? formatMeetingStartForExport(entry) : entry?.dateLabel;
  const heading = [entry?.title || t.title_fallback(), when].filter(Boolean).join(" — ");
  const rows = buildTranscriptRows(segments);
  const body = rows.map((row) => {
    const over = row.over.length ? ` ${t.over({ names: formatNameList(row.over) })}` : "";
    const prose = row.members.map((member) => member.kind === "speech"
      ? member.block.text
      : `(${shownSpeakerLabel(member.speakerLabel)}: ${member.blocks.map((block) => block.text).join(" ")})`
    ).join(" ");
    return `**${escapeMarkdown(shownSpeakerLabel(row.speakerLabel))}** [${formatClockTime(row.startMs)}]${escapeMarkdown(over)}\n\n${escapeMarkdown(prose)}`;
  }).join("\n\n");
  return `# ${escapeMarkdown(heading.replace(/\s*\n\s*/g, " "))}\n\n${body || t.empty()}\n`;
}

function escapeMarkdown(text: string): string {
  return text.replace(/([\\`*_{}\[\]<>#|~])/g, "\\$1");
}

export function safeMeetingStem(entry: Pick<MeetingCatalogEntry, "title" | "id">): string {
  const title = Array.from(entry.title.normalize("NFC").replace(/[^\p{L}\p{N}]+/gu, "-"))
    .slice(0, 80).join("").replace(/^-|-$/g, "") || "meeting";
  const id = entry.id.replace(/[^a-zA-Z0-9_-]/g, "-");
  if (id.toLowerCase() === title.toLowerCase()) return title;
  if (id.toLowerCase().startsWith(`${title.toLowerCase()}-`)) {
    return id;
  }
  return `${title}-${id}`;
}
