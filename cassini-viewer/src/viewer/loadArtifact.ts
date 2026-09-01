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
  describeTranscript,
  extractPortableManifestFromArrayBuffer,
  getDefaultTranscriptId,
  listAvailableTranscripts,
  loadPortableTranscriptBody,
  pickReadableForTranscript,
  type ExtractedPortableManifest,
  type PortableMeetingManifest,
  type PortableTranscriptDescriptor,
  type PortableTranscriptEntry,
} from "./portable";
import { readViewerBase, resolveAppBaseUrl } from "./appBase";

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
  /**
   * The producer already clipped this artifact's word ends to the audio it
   * measured (manifest `provenance.wordTimings.endsBoundedByAudio`).
   *
   * The viewer carries a display-time repair for the opposite case, where a
   * word's end was stamped at the next acoustic onset and so ran across the
   * following silence. That repair must not run on an artifact whose ends are
   * already measured: it would clip a genuinely long word — 1.44 s against a
   * 240 ms median, in the fp32 evidence that put this flag here — back to its
   * 1 s budget and undo the production fix. False for every artifact that
   * predates the marker, which is all 197 published meetings.
   */
  wordEndsBoundedByAudio: boolean;
  availableTranscripts: PortableTranscriptDescriptor[];
  currentTranscriptId: string;
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
// 1 MB - 1. v2 manifests carry an index + per-transcript chunk sets in the
// OpusTags header; a single transcript runs ~220 KB compressed, so 256 KB
// stopped covering common cases and was forcing the full-file fallback. 1 MB
// covers ~4 typical transcripts before the fallback kicks in.
const PORTABLE_METADATA_RANGE_END = 1048575;

// PortableMeetingStore owns the portable manifest + body caches that used to be
// module-level singletons (implicitly global state shared across every meeting
// load). Relocating them onto an instance lets a DataProvider own its own cache
// (see dataProvider.ts) so the state is no longer global. A single module-level
// default instance (defaultPortableStore) keeps the exported free functions —
// and therefore every existing call site and test — behaving identically.
export class PortableMeetingStore {
  // resolved audioUrl → in-flight/resolved extracted manifest.
  private readonly manifestCache = new Map<string, Promise<ExtractedPortableManifest>>();
  // resolved audioUrl → transcriptId → parsed body. Lets the switcher round-trip
  // A → B → A without re-fetching or re-decompressing payloads.
  private readonly bodyCache = new Map<string, Map<string, unknown>>();

  async loadManifest(audioUrl: string): Promise<ExtractedPortableManifest> {
    let manifestPromise = this.manifestCache.get(audioUrl);
    if (!manifestPromise) {
      manifestPromise = fetchPortableManifest(audioUrl);
      this.manifestCache.set(audioUrl, manifestPromise);
    }
    try {
      return await manifestPromise;
    } catch (error) {
      this.manifestCache.delete(audioUrl);
      this.bodyCache.delete(audioUrl);
      throw error;
    }
  }

  peekManifest(audioUrl: string): Promise<ExtractedPortableManifest> | undefined {
    return this.manifestCache.get(audioUrl);
  }

  primeBodies(
    audioUrl: string,
    manifest: PortableMeetingManifest,
    currentTranscriptId: string,
  ): void {
    // The initial extract already eager-resolved the default raw + readable
    // bodies into manifest.transcript / manifest.readableTranscript. Seed the
    // body cache with those so a round-trip back to default skips re-decoding.
    const bucket = this.bucketFor(audioUrl);
    if (manifest.transcript && !bucket.has(currentTranscriptId)) {
      bucket.set(currentTranscriptId, manifest.transcript);
    }
    const readableEntry = pickReadableForTranscript(manifest, currentTranscriptId);
    if (readableEntry && manifest.readableTranscript && !bucket.has(readableEntry.id)) {
      bucket.set(readableEntry.id, manifest.readableTranscript);
    }
  }

  async loadBody(
    audioUrl: string,
    bodyId: string,
    tags: Record<string, string>,
    entry: PortableTranscriptEntry,
  ): Promise<unknown> {
    const bucket = this.bucketFor(audioUrl);
    const cached = bucket.get(bodyId);
    if (cached !== undefined) {
      return cached;
    }
    const body = await loadPortableTranscriptBody(tags, entry.payloadRef);
    bucket.set(bodyId, body);
    return body;
  }

  private bucketFor(audioUrl: string): Map<string, unknown> {
    let bucket = this.bodyCache.get(audioUrl);
    if (!bucket) {
      bucket = new Map();
      this.bodyCache.set(audioUrl, bucket);
    }
    return bucket;
  }
}

// Module-level default store: keeps the exported free functions' behaviour
// identical to the previous module-level singletons for callers (and tests)
// that do not pass their own store.
const defaultPortableStore = new PortableMeetingStore();

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

export async function loadPortableArtifactFromAudioPath(
  audioPath: string,
  store: PortableMeetingStore = defaultPortableStore,
): Promise<LoadedArtifact> {
  const resolvedAudioPath = resolveDocumentAssetUrl(audioPath);
  const { manifest } = await store.loadManifest(resolvedAudioPath);
  const availableTranscripts = listAvailableTranscripts(manifest);
  const currentTranscriptId = getDefaultTranscriptId(manifest);
  store.primeBodies(resolvedAudioPath, manifest, currentTranscriptId);
  return buildPortableLoadedArtifact({
    manifest,
    audioSrc: resolvedAudioPath,
    availableTranscripts,
    currentTranscriptId,
  });
}

export async function loadPortableMeetingSummary(
  audioPath: string,
  store: PortableMeetingStore = defaultPortableStore,
): Promise<PortableMeetingSummary> {
  const resolvedAudioPath = resolveDocumentAssetUrl(audioPath);
  const { manifest } = await store.loadManifest(resolvedAudioPath);
  const transcript = buildTranscriptWordsFromPortable(manifest);
  return {
    speakerCount: transcript.speakers.length,
    segmentCount: transcript.segments.length,
    digestDurationMs: transcript.media.durationMs,
  };
}

/**
 * Switches the active transcript on an already-loaded portable meeting. Resolves
 * the alternate body (and its paired readable) from the cached OpusTags, then
 * re-runs the build pipeline. Caches parsed bodies per (audioUrl, transcriptId).
 * Throws if the transcript id is not present in the manifest.
 */
export async function switchPortableTranscript(
  audioPath: string,
  transcriptId: string,
  store: PortableMeetingStore = defaultPortableStore,
): Promise<LoadedArtifact> {
  const resolvedAudioPath = resolveDocumentAssetUrl(audioPath);
  const cached = store.peekManifest(resolvedAudioPath);
  if (!cached) {
    throw new Error(
      "switchPortableTranscript called before the portable meeting was loaded; load the meeting first.",
    );
  }
  const { manifest, tags } = await cached;
  const transcripts = Array.isArray(manifest.transcripts) ? manifest.transcripts : [];
  if (transcripts.length === 0) {
    if (transcriptId !== "default") {
      throw new Error(
        `portable meeting only has a single transcript; cannot switch to "${transcriptId}"`,
      );
    }
    const availableTranscripts = listAvailableTranscripts(manifest);
    return buildPortableLoadedArtifact({
      manifest,
      audioSrc: resolvedAudioPath,
      availableTranscripts,
      currentTranscriptId: "default",
    });
  }
  const entry = transcripts.find((candidate) => candidate.id === transcriptId);
  if (!entry) {
    throw new Error(`portable meeting has no transcript with id "${transcriptId}"`);
  }
  const transcriptBody = (await store.loadBody(
    resolvedAudioPath,
    transcriptId,
    tags,
    entry,
  )) as PortableMeetingManifest["transcript"];
  const readableEntry = pickReadableForTranscript(manifest, transcriptId);
  let readableBody: PortableMeetingManifest["readableTranscript"] | undefined;
  if (readableEntry) {
    readableBody = (await store.loadBody(
      resolvedAudioPath,
      readableEntry.id,
      tags,
      readableEntry,
    )) as PortableMeetingManifest["readableTranscript"];
  }
  const swappedManifest: PortableMeetingManifest = {
    ...manifest,
    transcript: transcriptBody,
    readableTranscript: readableBody,
    // The producer's pre-rendered displayTranscript only matches the default
    // transcript; suppress it when switching so we re-derive from the new body.
    displayTranscript: transcriptId === getDefaultTranscriptId(manifest)
      ? manifest.displayTranscript
      : undefined,
  };
  const availableTranscripts = listAvailableTranscripts(manifest);
  return buildPortableLoadedArtifact({
    manifest: swappedManifest,
    audioSrc: resolvedAudioPath,
    availableTranscripts,
    currentTranscriptId: transcriptId,
  });
}

function buildPortableLoadedArtifact({
  manifest,
  audioSrc,
  availableTranscripts,
  currentTranscriptId,
}: {
  manifest: PortableMeetingManifest;
  audioSrc: string;
  availableTranscripts: PortableTranscriptDescriptor[];
  currentTranscriptId: string;
}): LoadedArtifact {
  const transcript = validateTranscriptWordsV1(
    buildTranscriptWordsFromPortable(manifest, audioSrc) as unknown,
  );
  const rawReadableTranscript = buildReadableTranscriptFromPortable(manifest, transcript);
  const displayTranscript = validateDisplayTranscriptV1(
    (manifest.displayTranscript ??
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
    audioSrc,
    captionsSrc: null,
    chaptersSrc: null,
    timingPrecision: classifyArtifactTimingPrecision(transcript, displayTranscript),
    metadata: buildArtifactMetadata(
      "portable-opus",
      buildPortableMetadataRaw(manifest, transcript, displayTranscript, readableTranscript),
    ),
    wordEndsBoundedByAudio: readWordEndsBoundedByAudio(manifest.provenance),
    availableTranscripts,
    currentTranscriptId,
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
    wordEndsBoundedByAudio: readWordEndsBoundedByAudio(manifest?.provenance),
    availableTranscripts: SYNTHETIC_SINGLE_TRANSCRIPT,
    currentTranscriptId: "default",
  };
}

const SYNTHETIC_SINGLE_TRANSCRIPT: PortableTranscriptDescriptor[] = [
  { id: "default", role: "asr", label: "Transcript", description: "", isDefault: true },
];

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
  const viewerBase = readViewerBase();
  if (viewerBase) {
    return new URL(assetPath, viewerBase).toString();
  }
  return new URL(assetPath, resolveAppBaseUrl()).toString();
}

function resolveDocumentAssetUrl(assetPath: string): string {
  const viewerBase = readViewerBase();
  if (viewerBase) {
    return new URL(assetPath, viewerBase).toString();
  }
  return new URL(assetPath, window.location.href).toString();
}

async function fetchPortableManifest(audioUrl: string): Promise<ExtractedPortableManifest> {
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

/**
 * Does the manifest vouch that word ends were bounded by the measured audio?
 *
 * The marker is `provenance.wordTimings.endsBoundedByAudio` and only the
 * literal boolean `true` counts. Anything else — absent provenance, an absent
 * `wordTimings`, a string "true", a truthy number — leaves it false, because
 * the false answer is the safe one: it keeps the display-time repair running,
 * which is the behaviour every artifact had before the marker existed.
 */
export function readWordEndsBoundedByAudio(provenance: unknown): boolean {
  const wordTimings = asMaybeObject(asMaybeObject(provenance)?.wordTimings);
  return wordTimings?.endsBoundedByAudio === true;
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
  // Surfaced because it changes what the reader is looking at: with the marker
  // the word ends on screen are measured, without it the viewer is clipping
  // decoder-inflated ends back to a budget before judging simultaneity.
  if (readWordEndsBoundedByAudio(provenance)) {
    rows.push({ label: "Word timings", value: "Ends bounded by measured audio" });
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
    summary: null,
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
    // A bare words file carries no manifest, so nothing vouches for its ends.
    wordEndsBoundedByAudio: false,
    availableTranscripts: SYNTHETIC_SINGLE_TRANSCRIPT,
    currentTranscriptId: "default",
  };
}
