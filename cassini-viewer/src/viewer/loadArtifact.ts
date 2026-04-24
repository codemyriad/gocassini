import {
  validateDisplayTranscriptV1,
  buildTranscriptIndex,
  validateReadableTranscriptV1,
  validateTranscriptWordsV1,
} from "../core/transcript";
import type {
  DisplayTranscriptV1,
  ReadableTranscriptV1,
  TranscriptIndex,
  TranscriptWordsV1,
} from "../core/types";
import {
  buildDisplayTranscriptFromArtifacts,
  buildReadableTranscriptFromPortable,
  buildTranscriptWordsFromPortable,
  extractPortableManifestFromArrayBuffer,
  type PortableMeetingManifest,
} from "./portable";

export interface LoadedArtifact {
  transcript: TranscriptWordsV1;
  displayTranscript: DisplayTranscriptV1 | null;
  readableTranscript: ReadableTranscriptV1 | null;
  summary: string | null;
  index: TranscriptIndex;
  audioSrc: string;
  captionsSrc: string | null;
  chaptersSrc: string | null;
  timingPrecision: ArtifactTimingPrecision;
  metadata: ArtifactMetadata | null;
}

export type ArtifactTimingPrecisionLevel = "word" | "mixed" | "segment";

export interface ArtifactTimingPrecision {
  level: ArtifactTimingPrecisionLevel;
  label: string;
  detail: string;
}

export interface ArtifactMetadataRow {
  label: string;
  value?: string;
  values?: string[];
  tone?: "normal" | "code";
}

export interface ArtifactMetadataSection {
  title: string;
  rows: ArtifactMetadataRow[];
}

export interface ArtifactMetadata {
  sourceKind: string;
  sections: ArtifactMetadataSection[];
  rawJson: string;
}

export interface PortableMeetingSummary {
  speakerCount: number;
  segmentCount: number;
  digestDurationMs: number;
}

const DEFAULT_TRANSCRIPT_PATH = "./transcript.words.v1.json";
const DEFAULT_DISPLAY_TRANSCRIPT_PATH = "./transcript.display.v1.json";
const DEFAULT_READABLE_TRANSCRIPT_PATH = "./transcript.readable.v1.json";
const DEFAULT_SUMMARY_PATH = "./summary.md";
const DEFAULT_CAPTIONS_PATH = "./captions.vtt";
const DEFAULT_CHAPTERS_PATH = "./chapters.vtt";
const PORTABLE_METADATA_RANGE_END = 262143;
const portableManifestCache = new Map<string, Promise<PortableMeetingManifest>>();

export async function loadBundledArtifact(): Promise<LoadedArtifact> {
  return loadArtifactFromPaths({
    transcriptPath: resolveAppAssetUrl(DEFAULT_TRANSCRIPT_PATH),
    displayTranscriptPath: resolveAppAssetUrl(DEFAULT_DISPLAY_TRANSCRIPT_PATH),
    readableTranscriptPath: resolveAppAssetUrl(DEFAULT_READABLE_TRANSCRIPT_PATH),
    summaryPath: resolveAppAssetUrl(DEFAULT_SUMMARY_PATH),
    manifestPath: resolveAppAssetUrl("./manifest.json"),
    captionsPath: resolveAppAssetUrl(DEFAULT_CAPTIONS_PATH),
    chaptersPath: resolveAppAssetUrl(DEFAULT_CHAPTERS_PATH),
  });
}

export async function loadArtifactFromDirectory(basePath: string): Promise<LoadedArtifact> {
  return loadArtifactFromPaths({
    transcriptPath: `${basePath}/transcript.words.v1.json`,
    displayTranscriptPath: `${basePath}/transcript.display.v1.json`,
    readableTranscriptPath: `${basePath}/transcript.readable.v1.json`,
    summaryPath: `${basePath}/summary.md`,
    manifestPath: `${basePath}/manifest.json`,
    captionsPath: `${basePath}/captions.vtt`,
    chaptersPath: `${basePath}/chapters.vtt`,
  });
}

export async function loadPortableArtifactFromAudioPath(audioPath: string): Promise<LoadedArtifact> {
  const resolvedAudioPath = resolveDocumentAssetUrl(audioPath);
  const portable = await loadPortableManifestFromAudioPath(audioPath);
  const transcript = validateTranscriptWordsV1(
    buildTranscriptWordsFromPortable(portable, resolvedAudioPath) as unknown,
  );
  const rawReadableTranscript = buildReadableTranscriptFromPortable(portable, transcript);
  const displayTranscript = validateDisplayTranscriptV1(
    (portable.displayTranscript ??
      buildDisplayTranscriptFromArtifacts(transcript, rawReadableTranscript)) as unknown,
  );
  const readableTranscript = validateReadableTranscriptV1(
    rawReadableTranscript as unknown,
  );

  return {
    transcript,
    displayTranscript,
    readableTranscript,
    summary: null,
    index: buildTranscriptIndex(transcript),
    audioSrc: resolvedAudioPath,
    captionsSrc: null,
    chaptersSrc: null,
    timingPrecision: classifyArtifactTimingPrecision(transcript, displayTranscript),
    metadata: buildArtifactMetadata(
      "portable-opus",
      buildPortableMetadataRaw(portable, transcript, displayTranscript, readableTranscript),
    ),
  };
}

export async function loadPortableMeetingSummary(audioPath: string): Promise<PortableMeetingSummary> {
  const portable = await loadPortableManifestFromAudioPath(audioPath);
  const transcript = buildTranscriptWordsFromPortable(portable);
  return {
    speakerCount: transcript.speakers.length,
    segmentCount: transcript.segments.length,
    digestDurationMs: transcript.media.durationMs,
  };
}

async function loadArtifactFromPaths(paths: {
  transcriptPath: string;
  displayTranscriptPath?: string;
  readableTranscriptPath?: string;
  summaryPath?: string;
  captionsPath?: string;
  chaptersPath?: string;
  manifestPath?: string;
  audioPath?: string;
}): Promise<LoadedArtifact> {
  const response = await fetch(paths.transcriptPath);
  if (!response.ok) {
    throw new Error(
      `Could not load ${paths.transcriptPath}. Serve the viewer from a meeting artifact directory or bundle transcript.words.v1.json next to index.html.`,
    );
  }

  const transcript = validateTranscriptWordsV1((await response.json()) as unknown);
  const transcriptUrl = new URL(paths.transcriptPath, window.location.href);
  const displayTranscript = paths.displayTranscriptPath
    ? await probeOptionalJson(paths.displayTranscriptPath, validateDisplayTranscriptV1)
    : null;
  const readableTranscript = paths.readableTranscriptPath
    ? await probeOptionalJson(paths.readableTranscriptPath, validateReadableTranscriptV1)
    : null;
  const summary = paths.summaryPath ? await probeOptionalText(paths.summaryPath) : null;
  const manifest = paths.manifestPath ? await probeOptionalJson(paths.manifestPath, asLooseObject) : null;
  console.log(summary);
  return {
    transcript,
    displayTranscript,
    readableTranscript,
    summary,
    index: buildTranscriptIndex(transcript),
    audioSrc: paths.audioPath
      ? resolveDocumentAssetUrl(paths.audioPath)
      : resolveAssetUrl(transcript.media.src, transcriptUrl),
    captionsSrc: paths.captionsPath
      ? await probeOptionalAsset(resolveDocumentAssetUrl(paths.captionsPath))
      : null,
    chaptersSrc: paths.chaptersPath
      ? await probeOptionalAsset(resolveDocumentAssetUrl(paths.chaptersPath))
      : null,
    timingPrecision: classifyArtifactTimingPrecision(transcript, displayTranscript),
    metadata: buildArtifactMetadata(
      paths.audioPath ? "manual-artifact" : "artifact-directory",
      buildDirectoryMetadataRaw(manifest, transcript, displayTranscript, readableTranscript),
    ),
  };
}

export function classifyArtifactTimingPrecision(
  transcript: TranscriptWordsV1,
  displayTranscript: DisplayTranscriptV1 | null,
): ArtifactTimingPrecision {
  if (displayTranscript) {
    const wordTokens = displayTranscript.blocks.flatMap((block) => block.tokens)
      .filter((token) => token.kind === "word");
    const timedWordTokens = wordTokens.filter(
      (token) => Number.isInteger(token.startMs) && Number.isInteger(token.endMs),
    );
    if (wordTokens.length === 0 || timedWordTokens.length === 0) {
      return {
        level: "segment",
        label: "Passage-timed",
        detail: "Seeking is precise to passages; visible words are not individually timed in this artifact.",
      };
    }
    if (timedWordTokens.length === wordTokens.length) {
      return {
        level: "word",
        label: "Word-timed",
        detail: "Playback highlighting can follow individual words throughout the displayed transcript.",
      };
    }
    return {
      level: "mixed",
      label: "Mixed timing",
      detail: "Some displayed words have individual timing, while rewritten or unmatched words remain passage-timed.",
    };
  }

  const textWordCount = transcript.segments.reduce((count, segment) => count + countWords(segment.text), 0);
  const timedWordCount = transcript.segments.reduce((count, segment) => count + segment.words.length, 0);
  if (textWordCount === 0 || timedWordCount === 0) {
    return {
      level: "segment",
      label: "Passage-timed",
      detail: "This artifact exposes passage timing but not individual word timing.",
    };
  }
  if (timedWordCount >= textWordCount) {
    return {
      level: "word",
      label: "Word-timed",
      detail: "Playback highlighting can follow individual words throughout the transcript.",
    };
  }
  return {
    level: "mixed",
    label: "Mixed timing",
    detail: "Some transcript passages have word timing, while others only have passage timing.",
  };
}

function countWords(text: string): number {
  return text.trim().split(/\s+/).filter(Boolean).length;
}

function resolveAssetUrl(assetPath: string, transcriptUrl: URL): string {
  return new URL(assetPath, transcriptUrl).toString();
}

function resolveAppAssetUrl(assetPath: string): string {
  const base = import.meta.env.BASE_URL;
  const baseUrl = base && base !== "/" ? new URL(base, window.location.href) : new URL(window.location.href);
  return new URL(assetPath, baseUrl).toString();
}

function resolveDocumentAssetUrl(assetPath: string): string {
  return new URL(assetPath, window.location.href).toString();
}

async function loadPortableManifestFromAudioPath(audioPath: string): Promise<PortableMeetingManifest> {
  const resolvedAudioPath = resolveDocumentAssetUrl(audioPath);
  let manifestPromise = portableManifestCache.get(resolvedAudioPath);
  if (!manifestPromise) {
    manifestPromise = fetchPortableManifest(resolvedAudioPath);
    portableManifestCache.set(resolvedAudioPath, manifestPromise);
  }
  try {
    return await manifestPromise;
  } catch (error) {
    portableManifestCache.delete(resolvedAudioPath);
    throw error;
  }
}

async function fetchPortableManifest(audioUrl: string): Promise<PortableMeetingManifest> {
  const partialResponse = await fetch(audioUrl, {
    headers: {
      Range: `bytes=0-${PORTABLE_METADATA_RANGE_END}`,
    },
  });
  if (!partialResponse.ok) {
    throw new Error(`Could not load ${audioUrl}.`);
  }

  const partialBuffer = await partialResponse.arrayBuffer();
  try {
    return await extractPortableManifestFromArrayBuffer(partialBuffer);
  } catch (error) {
    if (partialResponse.status !== 206 && !partialResponse.headers.get("content-range")) {
      throw error;
    }
  }

  const fullResponse = await fetch(audioUrl);
  if (!fullResponse.ok) {
    throw new Error(`Could not load ${audioUrl}.`);
  }
  return extractPortableManifestFromArrayBuffer(await fullResponse.arrayBuffer());
}

async function probeOptionalAsset(path: string): Promise<string | null> {
  if (window.location.protocol === "file:") {
    return null;
  }
  try {
    const response = await fetch(path, { method: "HEAD" });
    return response.ok ? path : null;
  } catch {
    return null;
  }
}

async function probeOptionalJson<T>(
  assetPath: string,
  validate: (input: unknown) => T,
): Promise<T | null> {
  if (window.location.protocol === "file:") {
    return null;
  }
  const path = resolveDocumentAssetUrl(assetPath);
  try {
    const response = await fetch(path);
    if (!response.ok) {
      return null;
    }
    return validate((await response.json()) as unknown);
  } catch {
    return null;
  }
}

async function probeOptionalText(assetPath: string): Promise<string | null> {
  if (window.location.protocol === "file:") {
    return null;
  }
  const path = resolveDocumentAssetUrl(assetPath);
  try {
    const response = await fetch(path);
    if (!response.ok) {
      return null;
    }
    return await response.text();
  } catch {
    return null;
  }
}

function asLooseObject(input: unknown): Record<string, unknown> {
  if (!input || typeof input !== "object" || Array.isArray(input)) {
    throw new Error("manifest must be an object");
  }
  return input as Record<string, unknown>;
}

function buildPortableMetadataRaw(
  portable: PortableMeetingManifest,
  transcript: TranscriptWordsV1,
  displayTranscript: DisplayTranscriptV1 | null,
  readableTranscript: ReadableTranscriptV1 | null,
): Record<string, unknown> {
  return {
    meeting: portable.meeting ?? {},
    audio: portable.audio ?? {},
    integrity: (portable as Record<string, unknown>).integrity ?? {},
    stats: {
      speakers: transcript.speakers.length,
      passages: displayTranscript?.blocks.length ?? transcript.segments.length,
      words: transcript.segments.reduce((count, segment) => count + segment.words.length, 0),
      sourceTranscriptVersion: displayTranscript?.sourceTranscriptVersion ?? "transcript.words.v1",
      sourceReadableTranscriptVersion:
        displayTranscript?.sourceReadableTranscriptVersion ??
        readableTranscript?.version ??
        undefined,
    },
    provenance: portable.provenance ?? {},
    speakers: Array.isArray(portable.speakers) ? portable.speakers : [],
  };
}

function buildDirectoryMetadataRaw(
  manifest: Record<string, unknown> | null,
  transcript: TranscriptWordsV1,
  displayTranscript: DisplayTranscriptV1 | null,
  readableTranscript: ReadableTranscriptV1 | null,
): Record<string, unknown> {
  const base = manifest ? structuredClone(manifest) : {};
  return {
    ...base,
    audio: {
      durationMs: transcript.media.durationMs,
      sha256: transcript.media.sha256,
      src: transcript.media.src,
    },
    stats: {
      speakers: transcript.speakers.length,
      passages: displayTranscript?.blocks.length ?? transcript.segments.length,
      words: transcript.segments.reduce((count, segment) => count + segment.words.length, 0),
      sourceTranscriptVersion: displayTranscript?.sourceTranscriptVersion ?? transcript.version,
      sourceReadableTranscriptVersion:
        displayTranscript?.sourceReadableTranscriptVersion ??
        readableTranscript?.version ??
        undefined,
    },
    speakers: transcript.speakers,
  };
}

function buildArtifactMetadata(
  sourceKind: string,
  raw: Record<string, unknown>,
): ArtifactMetadata | null {
  const normalized = {
    sourceKind,
    ...raw,
  };
  const sections = buildMetadataSections(normalized);
  if (sections.length === 0) {
    return null;
  }
  return {
    sourceKind,
    sections,
    rawJson: JSON.stringify(normalized, null, 2),
  };
}

function buildMetadataSections(raw: Record<string, unknown>): ArtifactMetadataSection[] {
  const sections: ArtifactMetadataSection[] = [];
  const meetingRows = buildMeetingMetadataRows(raw);
  if (meetingRows.length > 0) {
    sections.push({ title: "Meeting", rows: meetingRows });
  }
  const processingRows = buildProcessingMetadataRows(raw);
  if (processingRows.length > 0) {
    sections.push({ title: "Processing", rows: processingRows });
  }
  const technicalRows = buildTechnicalMetadataRows(raw);
  if (technicalRows.length > 0) {
    sections.push({ title: "Technical", rows: technicalRows });
  }
  if (sections.length === 0) {
    const fallbackRows = flattenObjectRows(raw);
    if (fallbackRows.length > 0) {
      sections.push({ title: "Artifact", rows: fallbackRows });
    }
  }
  return sections;
}

function buildMeetingMetadataRows(raw: Record<string, unknown>): ArtifactMetadataRow[] {
  const rows: ArtifactMetadataRow[] = [];
  const meeting = asMaybeObject(raw.meeting);
  const source = asMaybeObject(raw.source);
  const stats = asMaybeObject(raw.stats);
  const recordedAtLocal =
    asNonEmptyString(meeting?.recordedAtLocal) ??
    asNonEmptyString(source?.recordedAtLocal);
  const createdAtUtc =
    asNonEmptyString(meeting?.createdAtUtc) ??
    asNonEmptyString(meeting?.createdAtUTC);
  const durationMs =
    asFiniteNumber(meeting?.durationMs) ??
    asFiniteNumber(asMaybeObject(raw.audio)?.durationMs) ??
    asFiniteNumber(source?.durationMs);
  const speakerNames = normalizeSpeakerNames(raw.speakers);
  const passages = asFiniteNumber(stats?.passages);

  if (recordedAtLocal) {
    rows.push({
      label: "Recorded",
      value: formatLocalTimestamp(recordedAtLocal),
    });
  } else if (createdAtUtc) {
    rows.push({
      label: "Created",
      value: formatUtcTimestamp(createdAtUtc),
    });
  }
  if (durationMs !== null) {
    rows.push({
      label: "Duration",
      value: formatDurationMs(durationMs),
    });
  }
  if (speakerNames.length > 0) {
    rows.push({
      label: speakerNames.length === 1 ? "Speaker" : "Speakers",
      values: speakerNames,
    });
  } else if (asFiniteNumber(stats?.speakers) !== null) {
    rows.push({
      label: "Speakers",
      value: `${asFiniteNumber(stats?.speakers)} total`,
    });
  }
  if (passages !== null) {
    rows.push({
      label: "Passages",
      value: `${passages}`,
    });
  }
  return rows;
}

function buildProcessingMetadataRows(raw: Record<string, unknown>): ArtifactMetadataRow[] {
  const rows: ArtifactMetadataRow[] = [];
  const meeting = asMaybeObject(raw.meeting);
  const processedAtUtc =
    asNonEmptyString(meeting?.processedAtUtc) ??
    asNonEmptyString(meeting?.processedAtUTC) ??
    asNonEmptyString(raw.generatedAt);
  const provenance = asMaybeObject(raw.provenance);
  const stats = asMaybeObject(raw.stats);

  if (processedAtUtc) {
    rows.push({
      label: "Processed",
      value: formatUtcTimestamp(processedAtUtc),
    });
  }

  const stt = describeProcessingStep(asMaybeObject(provenance?.speechToText));
  if (stt) {
    rows.push({ label: "Speech to text", value: stt });
  }
  const readableCleanup = describeProcessingStep(asMaybeObject(provenance?.readableCleanup));
  if (readableCleanup) {
    rows.push({ label: "Readable cleanup", value: readableCleanup });
  }
  const displayTranscript = describeProcessingStep(asMaybeObject(provenance?.displayTranscript));
  if (displayTranscript) {
    rows.push({ label: "Display alignment", value: displayTranscript });
  }

  const sourceTranscriptVersion = asNonEmptyString(stats?.sourceTranscriptVersion);
  if (sourceTranscriptVersion) {
    rows.push({ label: "Source transcript", value: sourceTranscriptVersion });
  }
  const sourceReadableTranscriptVersion = asNonEmptyString(stats?.sourceReadableTranscriptVersion);
  if (sourceReadableTranscriptVersion) {
    rows.push({ label: "Readable transcript", value: sourceReadableTranscriptVersion });
  }
  return rows;
}

function buildTechnicalMetadataRows(raw: Record<string, unknown>): ArtifactMetadataRow[] {
  const rows: ArtifactMetadataRow[] = [];
  const meeting = asMaybeObject(raw.meeting);
  const audio = asMaybeObject(raw.audio);
  const source = asMaybeObject(raw.source);
  const stats = asMaybeObject(raw.stats);

  const sourceFile = asNonEmptyString(source?.basename);
  if (sourceFile) {
    rows.push({ label: "Source file", value: sourceFile, tone: "code" });
  }
  const meetingId = asNonEmptyString(meeting?.id);
  if (meetingId) {
    rows.push({ label: "Meeting ID", value: meetingId, tone: "code" });
  }
  const audioSummary = formatAudioSummary(audio);
  if (audioSummary) {
    rows.push({ label: "Audio", value: audioSummary });
  }
  const wordCount = asFiniteNumber(stats?.words);
  if (wordCount !== null) {
    rows.push({ label: "Words", value: `${wordCount}` });
  }
  return rows;
}

function flattenObjectRows(
  value: Record<string, unknown>,
  prefix = "",
): ArtifactMetadataRow[] {
  const rows: ArtifactMetadataRow[] = [];
  for (const [key, fieldValue] of Object.entries(value)) {
    if (fieldValue === undefined || fieldValue === null) {
      continue;
    }
    const label = prefix ? `${prefix}.${key}` : key;
    const nestedObject = asMaybeObject(fieldValue);
    if (nestedObject) {
      rows.push(...flattenObjectRows(nestedObject, label));
      continue;
    }
    rows.push({
      label,
      value: formatMetadataValue(fieldValue),
    });
  }
  return rows;
}

function formatMetadataValue(value: unknown): string {
  if (Array.isArray(value)) {
    return JSON.stringify(value);
  }
  if (typeof value === "boolean") {
    return value ? "true" : "false";
  }
  if (typeof value === "number") {
    return String(value);
  }
  if (typeof value === "string") {
    return value;
  }
  if (value && typeof value === "object") {
    return JSON.stringify(value);
  }
  return String(value);
}

function asMaybeObject(value: unknown): Record<string, unknown> | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return null;
  }
  return value as Record<string, unknown>;
}

function asFiniteNumber(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function asNonEmptyString(value: unknown): string | null {
  return typeof value === "string" && value.trim() !== "" ? value.trim() : null;
}

function normalizeSpeakerNames(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map((speaker) => asMaybeObject(speaker))
    .map((speaker) => {
      const label = asNonEmptyString(speaker?.label);
      const id = asNonEmptyString(speaker?.id);
      return humanizeSpeakerLabel(label ?? id ?? "");
    })
    .filter(Boolean);
}

function humanizeSpeakerLabel(value: string): string {
  return value
    .trim()
    .replace(/[_-]+/g, " ")
    .replace(/\s+/g, " ")
    .replace(/\b\w/g, (match) => match.toUpperCase());
}

function formatDurationMs(durationMs: number): string {
  const totalSeconds = Math.max(0, Math.round(durationMs / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (hours > 0) {
    return `${hours}:${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
  }
  return `${minutes}:${String(seconds).padStart(2, "0")}`;
}

function formatLocalTimestamp(value: string): string {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(value.trim());
  if (!match) {
    return value;
  }
  const [, year, month, day, hour, minute, second = "00"] = match;
  const asDate = new Date(Date.UTC(
    Number.parseInt(year, 10),
    Number.parseInt(month, 10) - 1,
    Number.parseInt(day, 10),
    Number.parseInt(hour, 10),
    Number.parseInt(minute, 10),
    Number.parseInt(second, 10),
  ));
  return new Intl.DateTimeFormat("en-US", {
    dateStyle: "long",
    timeStyle: "short",
    timeZone: "UTC",
  }).format(asDate);
}

function formatUtcTimestamp(value: string): string {
  const asDate = new Date(value);
  if (Number.isNaN(asDate.getTime())) {
    return value;
  }
  return `${new Intl.DateTimeFormat("en-US", {
    dateStyle: "long",
    timeStyle: "short",
    timeZone: "UTC",
  }).format(asDate)} UTC`;
}

function describeProcessingStep(step: Record<string, unknown> | null): string {
  if (!step) {
    return "";
  }
  const parts = [
    asNonEmptyString(step.backend),
    asNonEmptyString(step.engine),
    asNonEmptyString(step.model),
    asNonEmptyString(step.device),
    asNonEmptyString(step.version),
  ].filter(Boolean);
  return parts.join(" · ");
}

function formatAudioSummary(audio: Record<string, unknown> | null): string {
  if (!audio) {
    return "";
  }
  const parts: string[] = [];
  const codec = asNonEmptyString(audio.codec);
  if (codec) {
    parts.push(codec.toUpperCase());
  }
  const sampleRate = asFiniteNumber(audio.sampleRate);
  if (sampleRate !== null) {
    parts.push(`${Math.round(sampleRate / 1000)} kHz`);
  }
  const channels = asFiniteNumber(audio.channels);
  if (channels === 1) {
    parts.push("mono");
  } else if (channels === 2) {
    parts.push("stereo");
  } else if (channels !== null) {
    parts.push(`${channels} channels`);
  }
  return parts.join(" · ");
}

function toTitleCase(value: string): string {
  return value
    .replace(/[_-]+/g, " ")
    .replace(/([a-z])([A-Z])/g, "$1 $2")
    .replace(/\s+/g, " ")
    .trim()
    .replace(/\b\w/g, (match) => match.toUpperCase());
}

export async function readTranscriptFile(file: File): Promise<LoadedArtifact> {
  const raw = JSON.parse(await file.text()) as unknown;
  const transcript = validateTranscriptWordsV1(raw);
  return {
    transcript,
    displayTranscript: null,
    readableTranscript: null,
    index: buildTranscriptIndex(transcript),
    audioSrc: transcript.media.src,
    captionsSrc: null,
    chaptersSrc: null,
    timingPrecision: classifyArtifactTimingPrecision(transcript, null),
    metadata: buildArtifactMetadata("transcript-file", {
      audio: transcript.media,
      stats: {
        speakers: transcript.speakers.length,
        passages: transcript.segments.length,
        words: transcript.segments.reduce((count, segment) => count + segment.words.length, 0),
        sourceTranscriptVersion: transcript.version,
      },
      speakers: transcript.speakers,
    }),
  };
}
