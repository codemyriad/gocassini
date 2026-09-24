import { formatClockTime, judgedDisplaySegments, normalizeSpeakerLabel } from "../core/transcript";
import { tokensPreserveText } from "../core/wordInteraction";
import { buildTranscriptRows, repairTurnFinalWordInflation, sortBlocksInReadingOrder } from "../core/overlap";
import { buildDisplayTranscriptFromArtifacts } from "./portable";
import type { JudgedDisplaySegment } from "../core/transcript";
import type { LoadedArtifact } from "./loadArtifact";
import type { MeetingCatalogEntry } from "./catalog";

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
        : normalizeSpeakerLabel(sourceSegments[0]?.speakerLabel ?? "Unknown speaker");
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

/** Plain text follows the reader's turn order and its inline interjections. */
export function transcriptText(entry: Pick<MeetingCatalogEntry, "title" | "dateLabel"> | null, segments: JudgedDisplaySegment[]): string {
  const heading = [entry?.title || "Meeting transcript", entry?.dateLabel].filter(Boolean).join(" — ");
  const rows = buildTranscriptRows(segments);
  const body = rows.map((row) => {
    const over = row.over.length ? ` (over ${row.over.join(" and ")})` : "";
    const prose = row.members.map((member) => member.kind === "speech"
      ? member.block.text
      : `(${member.speakerLabel}: ${member.blocks.map((block) => block.text).join(" ")})`
    ).join(" ");
    return `${row.speakerLabel}  ${formatClockTime(row.startMs)}${over}\n${prose}`;
  }).join("\n\n");
  return `${heading}\n\n${body || "No transcript available."}\n`;
}

export function safeMeetingStem(entry: Pick<MeetingCatalogEntry, "title" | "id">): string {
  const title = entry.title.normalize("NFKD").replace(/[^\p{L}\p{N}]+/gu, "-").replace(/^-|-$/g, "").slice(0, 80) || "meeting";
  const id = entry.id.replace(/[^a-zA-Z0-9_-]/g, "-");
  return `${title}-${id}`;
}
