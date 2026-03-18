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

const DEFAULT_TRANSCRIPT_PATH = "./transcript.words.v1.json";
const DEFAULT_DISPLAY_TRANSCRIPT_PATH = "./transcript.display.v1.json";
const DEFAULT_READABLE_TRANSCRIPT_PATH = "./transcript.readable.v1.json";
const DEFAULT_CAPTIONS_PATH = "./captions.vtt";
const DEFAULT_CHAPTERS_PATH = "./chapters.vtt";

export async function loadBundledArtifact(): Promise<LoadedArtifact> {
  return loadArtifactFromPaths({
    transcriptPath: DEFAULT_TRANSCRIPT_PATH,
    displayTranscriptPath: DEFAULT_DISPLAY_TRANSCRIPT_PATH,
    readableTranscriptPath: DEFAULT_READABLE_TRANSCRIPT_PATH,
    captionsPath: DEFAULT_CAPTIONS_PATH,
    chaptersPath: DEFAULT_CHAPTERS_PATH,
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
  const response = await fetch(resolvedAudioPath);
  if (!response.ok) {
    throw new Error(`Could not load ${resolvedAudioPath}.`);
  }

  const portable = await extractPortableManifestFromArrayBuffer(await response.arrayBuffer());
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

function resolveDocumentAssetUrl(assetPath: string): string {
  return new URL(assetPath, window.location.href).toString();
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
