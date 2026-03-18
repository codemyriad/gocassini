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
  index: TranscriptIndex;
  audioSrc: string;
  captionsSrc: string | null;
  chaptersSrc: string | null;
}

export interface PortableMeetingSummary {
  speakerCount: number;
  segmentCount: number;
  digestDurationMs: number;
}

const DEFAULT_TRANSCRIPT_PATH = "./transcript.words.v1.json";
const DEFAULT_DISPLAY_TRANSCRIPT_PATH = "./transcript.display.v1.json";
const DEFAULT_READABLE_TRANSCRIPT_PATH = "./transcript.readable.v1.json";
const DEFAULT_CAPTIONS_PATH = "./captions.vtt";
const DEFAULT_CHAPTERS_PATH = "./chapters.vtt";
const PORTABLE_METADATA_RANGE_END = 262143;
const portableManifestCache = new Map<string, Promise<PortableMeetingManifest>>();

export async function loadBundledArtifact(): Promise<LoadedArtifact> {
  return loadArtifactFromPaths({
    transcriptPath: resolveAppAssetUrl(DEFAULT_TRANSCRIPT_PATH),
    displayTranscriptPath: resolveAppAssetUrl(DEFAULT_DISPLAY_TRANSCRIPT_PATH),
    readableTranscriptPath: resolveAppAssetUrl(DEFAULT_READABLE_TRANSCRIPT_PATH),
    captionsPath: resolveAppAssetUrl(DEFAULT_CAPTIONS_PATH),
    chaptersPath: resolveAppAssetUrl(DEFAULT_CHAPTERS_PATH),
  });
}

export async function loadArtifactFromDirectory(basePath: string): Promise<LoadedArtifact> {
  return loadArtifactFromPaths({
    transcriptPath: `${basePath}/transcript.words.v1.json`,
    displayTranscriptPath: `${basePath}/transcript.display.v1.json`,
    readableTranscriptPath: `${basePath}/transcript.readable.v1.json`,
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
  const readableTranscript = validateReadableTranscriptV1(
    buildReadableTranscriptFromPortable(portable, transcript) as unknown,
  );
  const displayTranscript = validateDisplayTranscriptV1(
    buildDisplayTranscriptFromArtifacts(transcript, readableTranscript) as unknown,
  );

  return {
    transcript,
    displayTranscript,
    readableTranscript,
    index: buildTranscriptIndex(transcript),
    audioSrc: resolvedAudioPath,
    captionsSrc: null,
    chaptersSrc: null,
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
  captionsPath?: string;
  chaptersPath?: string;
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
  return {
    transcript,
    displayTranscript,
    readableTranscript,
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
  };
}

function resolveAssetUrl(assetPath: string, transcriptUrl: URL): string {
  return new URL(assetPath, transcriptUrl).toString();
}

function resolveAppAssetUrl(assetPath: string): string {
  const baseUrl = new URL(import.meta.env.BASE_URL || "/", window.location.href);
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
  };
}
