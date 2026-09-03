import { cpSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { gunzipSync } from "node:zlib";
import { dirname, join, resolve, extname } from "node:path";
import { fileURLToPath } from "node:url";

const CATALOG_VERSION = "cassini.viewer.catalog.v1";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const viewerDir = resolve(scriptDir, "..");
// Overridable so tests can exercise the real CLI entry path against a throwaway
// dist without touching the checked-out build output.
const distDir = process.env.CASSINI_VIEWER_DIST_DIR
  ? resolve(process.env.CASSINI_VIEWER_DIST_DIR)
  : resolve(viewerDir, "dist");
const defaultSourceDir = resolve(viewerDir, "public", "demo");
const defaultOutputDir = resolve(viewerDir, "exports", "static-meetings");

export function main(argv = process.argv.slice(2)) {
  const { outputDir, sourceDir, recordingsBaseUrl, rebuildViewer } = parseArgs(argv);
  const distIndexPath = join(distDir, "index.html");

  // Lightweight by default (D-531): publish scans meetings and writes
  // catalog.json + meetings/* only. The viewer shell (index.html + assets/) is
  // served from the Docker image at runtime, so it is embedded into the site
  // only on explicit --rebuild-viewer (backwards-compat / standalone static
  // site). Only that opt-in path requires the built viewer dist.
  if (rebuildViewer && !existsSync(distIndexPath)) {
    throw new Error(`Missing ${distIndexPath}. Run "npm run build" first.`);
  }
  if (!existsSync(sourceDir)) {
    throw new Error(
      `Missing meeting source directory: ${sourceDir}. Pass --source-dir <artifact-root> when artifacts are stored outside this repo.`,
    );
  }

  const sourceEntries = readdirSync(sourceDir, { withFileTypes: true });
  const selected = [];
  const seen = new Set();
  const byName = (a, b) => a.name.localeCompare(b.name);

  for (const entry of sourceEntries.sort(byName)) {
    if (entry.name.startsWith(".")) {
      continue;
    }
    if (entry.isDirectory()) {
      if (seen.has(entry.name)) {
        continue;
      }
      selected.push({
        meetingId: entry.name,
        sourcePath: join(sourceDir, entry.name),
        sourceType: "directory",
      });
      seen.add(entry.name);
      continue;
    }

    if (entry.isFile() && isPortableMeeting(entry.name)) {
      const meetingId = entry.name.slice(0, -extname(entry.name).length) || "meeting";
      if (seen.has(meetingId)) {
        continue;
      }
      selected.push({
        meetingId,
        sourcePath: join(sourceDir, entry.name),
        sourceType: "portable",
      });
      seen.add(meetingId);
    }
  }

  if (selected.length === 0) {
    throw new Error(`No meeting directories or .opus files found in ${sourceDir}.`);
  }

  rmSync(outputDir, { recursive: true, force: true });
  mkdirSync(outputDir, { recursive: true });
  mkdirSync(join(outputDir, "meetings"), { recursive: true });

  if (rebuildViewer) {
    const builtIndexHtml = readFileSync(distIndexPath, "utf8");
    writeFileSync(join(outputDir, "index.html"), builtIndexHtml, "utf8");
    cpSync(join(distDir, "assets"), join(outputDir, "assets"), { recursive: true });
  }

  const meetings = selected.map(({ meetingId, sourcePath, sourceType }) =>
    exportMeeting({
      meetingId,
      sourcePath,
      sourceType,
      outputDir,
      recordingsBaseUrl,
    }),
  );

  writeFileSync(
    join(outputDir, "catalog.json"),
    `${JSON.stringify({ version: CATALOG_VERSION, meetings }, null, 2)}\n`,
    "utf8",
  );

  if (rebuildViewer) {
    console.log(`viewer index -> ${join(outputDir, "index.html")}`);
  }
  console.log(`viewer catalog -> ${join(outputDir, "catalog.json")}`);
  for (const meeting of meetings) {
    const meetingRef = meeting.audioPath ? `${meeting.id}.opus` : meeting.id;
    console.log(`${meeting.id} -> ${join(outputDir, "meetings", meetingRef)}`);
  }
}

export function parseArgs(argv) {
  let outputDir = defaultOutputDir;
  let sourceDir = defaultSourceDir;
  let recordingsBaseUrl = null;
  let rebuildViewer = false;
  for (let index = 0; index < argv.length; index += 1) {
    if (argv[index] === "--output-dir") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --output-dir");
      }
      outputDir = resolve(viewerDir, next);
      index += 1;
      continue;
    }
    if (argv[index] === "--source-dir") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --source-dir");
      }
      sourceDir = resolve(viewerDir, next);
      index += 1;
      continue;
    }
    if (argv[index] === "--recordings-base-url") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --recordings-base-url");
      }
      recordingsBaseUrl = next.endsWith("/") ? next : `${next}/`;
      index += 1;
      continue;
    }
    // Value-less: embed the built viewer shell (index.html + assets/) into the
    // output. Off by default — publish is lightweight (catalog + meetings only)
    // and the viewer is served from the Docker image (D-531).
    if (argv[index] === "--rebuild-viewer") {
      rebuildViewer = true;
    }
  }
  return { outputDir, sourceDir, recordingsBaseUrl, rebuildViewer };
}

export function exportMeeting({ meetingId, sourcePath, sourceType, outputDir, recordingsBaseUrl = null }) {
  if (sourceType === "portable") {
    const portable = extractPortableManifest(sourcePath);
    const { title, dateLabel } = describeMeeting(
      meetingId,
      portable?.meeting?.createdAtUtc,
      portable?.meeting?.recordedAtLocal,
    );
    const transcript = buildTranscriptWordsFromPortable(portable);
    // A real embedded title (e.g. the Talk room name the operator captured
    // at recording time) beats anything derived from the file name; packer
    // defaults that merely echo the id fall through to describeMeeting.
    const meetingTitle = preferredPortableTitle(portable, meetingId) || title;
    const targetFileName = `${meetingId}.opus`;
    if (!recordingsBaseUrl) {
      cpSync(sourcePath, join(outputDir, "meetings", targetFileName));
    }
    const sttVariantLabel = describeSpeechToTextVariant({ provenance: portable.provenance }) || describeVariantSuffix(meetingId);
    return {
      id: meetingId,
      audioPath: recordingsBaseUrl ? `${recordingsBaseUrl}meetings/${targetFileName}` : `./meetings/${targetFileName}`,
      title: sttVariantLabel ? `${meetingTitle} (${sttVariantLabel})` : meetingTitle,
      dateLabel,
      speakerCount: transcript.speakers?.length ?? 0,
      segmentCount: transcript.segments?.length ?? 0,
      digestDurationMs: transcript.media?.durationMs ?? 0,
      // The room the recording came from, when the file carries one (D-622).
      // Spread last and conditionally, so a meeting with no room ships no room
      // keys at all rather than two empty strings.
      ...portableRoomFields(portable),
    };
  }

  // Directory packs carry no createdAtUtc in the metadata the exporter reads
  // (manifest.json), and no room either — manifest.json has never had one, and
  // this branch serves the pre-portable format that will not gain fields. Such
  // meetings are exported without a room, which is what they have.
  const { title, dateLabel } = describeMeeting(meetingId);

  if (recordingsBaseUrl) {
    const manifest = readManifest(sourcePath, meetingId);
    const sttVariantLabel = describeSpeechToTextVariant(manifest) || describeVariantSuffix(meetingId);
    return {
      id: meetingId,
      artifactPath: `${recordingsBaseUrl}meetings/${meetingId}`,
      title: sttVariantLabel ? `${title} (${sttVariantLabel})` : title,
      dateLabel,
      speakerCount: manifest.speakerCount ?? 0,
      segmentCount: manifest.segmentCount ?? 0,
      digestDurationMs: manifest.digestDurationMs ?? 0,
    };
  }

  const targetMeetingDir = join(outputDir, "meetings", meetingId);
  mkdirSync(targetMeetingDir, { recursive: true });
  const manifest = readManifest(sourcePath, meetingId);
  copyPublicMeetingFiles(sourcePath, targetMeetingDir, manifest);
  ensureDisplayTranscript(targetMeetingDir);
  const targetManifest = readManifest(targetMeetingDir, meetingId);
  const sttVariantLabel = describeSpeechToTextVariant(targetManifest) || describeVariantSuffix(meetingId);
  return {
    id: meetingId,
    artifactPath: `./meetings/${meetingId}`,
    title: sttVariantLabel ? `${title} (${sttVariantLabel})` : title,
    dateLabel,
    speakerCount: targetManifest.speakerCount ?? 0,
    segmentCount: targetManifest.segmentCount ?? 0,
    digestDurationMs: targetManifest.digestDurationMs ?? 0,
  };
}

export function extractPortableManifest(path) {
  return readPortableMeeting(path).manifest;
}

// readPortableMeeting returns both the published index and the default bodies
// it references. The resolved body fields exist only in memory; the main wire
// manifest remains an index.
export function readPortableMeeting(path) {
  const output = execFileSync("ffprobe", [
    "-v",
    "error",
    "-show_entries",
    "format_tags:stream_tags",
    "-of",
    "json",
    path,
  ], { encoding: "utf8" });

  const report = JSON.parse(output);
  const tags = {};
  for (const [key, value] of Object.entries(report.format?.tags || {})) {
    tags[key.toUpperCase()] = String(value ?? "");
  }
  for (const stream of report.streams || []) {
    for (const [key, value] of Object.entries(stream.tags || {})) {
      tags[key.toUpperCase()] = String(value ?? "");
    }
  }

  const format = safeToString(tags.CASSINI_FORMAT).trim();
  if (!format) {
    throw new Error(`Missing CASSINI_FORMAT in ${path}`);
  }
  if (format !== "org.cassini.portable-meeting/1") {
    throw new Error(`Unsupported CASSINI_FORMAT=${format} in ${path}`);
  }
  for (const [name, expected] of Object.entries({
    CASSINI_PROFILE: "ogg-opus",
    CASSINI_PAYLOAD_MIME: "application/vnd.cassini.portable-meeting+json",
    CASSINI_PAYLOAD_ENCODING: "base64url+gzip+utf8json",
    CASSINI_PAYLOAD_SCHEMA:
      "https://cassini-format.codemyriad.io/schema/cassini-portable-meeting-manifest-v1.schema.json",
    CASSINI_AUDIO_MATCH_POLICY: "exact-opus-audio-v1",
  })) {
    if (safeToString(tags[name]).trim() !== expected) {
      throw new Error(`Unsupported ${name}=${safeToString(tags[name])} in ${path}`);
    }
  }
  if (!/^[0-9a-f]{64}$/.test(safeToString(tags.CASSINI_AUDIO_OPUS_SHA256).trim())) {
    throw new Error(`Missing or invalid CASSINI_AUDIO_OPUS_SHA256 in ${path}`);
  }

  const chunkCount = safeToInt(tags.CASSINI_PAYLOAD_CHUNK_COUNT, 0);
  if (chunkCount <= 0) {
    throw new Error(`Missing or invalid CASSINI_PAYLOAD_CHUNK_COUNT in ${path}`);
  }

  const manifestJSON = decodePortableChunkSet({
    tags,
    prefix: "CASSINI_PAYLOAD_",
    chunkCount,
    expectedSHA256: tags.CASSINI_PAYLOAD_SHA256,
    expectedRawBytes: tags.CASSINI_PAYLOAD_RAW_BYTES,
    expectedGzipBytes: tags.CASSINI_PAYLOAD_GZIP_BYTES,
    label: `portable manifest in ${path}`,
  });
  const indexManifest = JSON.parse(manifestJSON.toString("utf8"));
  validatePublishedPortableManifest(indexManifest, path);
  if (safeToString(tags.CASSINI_AUDIO_OPUS_SHA256).trim() !== String(indexManifest.integrity.opusAudioSha256)) {
    throw new Error(`Portable Opus audio digest disagrees between tags and manifest in ${path}`);
  }

  const manifest = structuredClone(indexManifest);
  const defaultRaw = pickPortableEntry(indexManifest.transcripts, "", "");
  manifest.transcript = decodePortableTranscriptBody(tags, defaultRaw, path);

  const readables = Array.isArray(indexManifest.readableTranscripts)
    ? indexManifest.readableTranscripts
    : [];
  const readable = pickPortableEntry(readables, "readable-cleanup", defaultRaw.id);
  if (readable) {
    manifest.readableTranscript = decodePortableTranscriptBody(tags, readable, path);
  }
  const display = pickPortableEntry(readables, "display", defaultRaw.id);
  if (display) {
    manifest.displayTranscript = decodePortableTranscriptBody(tags, display, path);
  }
  return { manifest, indexManifest, tags };
}

export function validatePublishedPortableManifest(manifest, path = "portable meeting") {
  if (!manifest || typeof manifest !== "object" || Array.isArray(manifest)) {
    throw new Error(`Invalid portable manifest in ${path}: expected an object`);
  }
  if (manifest.kind !== "cassini-portable-meeting") {
    throw new Error(`Unsupported portable manifest kind ${String(manifest.kind)} in ${path}`);
  }
  if (manifest.version !== 1) {
    throw new Error(`Unsupported portable manifest version ${String(manifest.version)} in ${path}`);
  }
  if (manifest.profile !== "ogg-opus") {
    throw new Error(`Unsupported portable profile ${String(manifest.profile)} in ${path}`);
  }
  if (manifest?.integrity?.matchPolicy !== "exact-opus-audio-v1") {
    throw new Error(`Unsupported portable audio integrity policy in ${path}`);
  }
  if (!/^[0-9a-f]{64}$/.test(String(manifest?.integrity?.opusAudioSha256 ?? ""))) {
    throw new Error(`Invalid portable Opus digest in ${path}`);
  }
  if (!Array.isArray(manifest.transcripts) || manifest.transcripts.length === 0) {
    throw new Error(`Invalid portable manifest in ${path}: transcripts must contain at least one entry`);
  }
  const readable = Array.isArray(manifest.readableTranscripts)
    ? manifest.readableTranscripts
    : [];
  const wordIDs = new Set();
  const allIDs = new Set();
  for (const entry of manifest.transcripts) {
    validatePortableTranscriptEntry(
      entry,
      path,
      new Set(["raw-asr", "human-corrected", "translation", "scripted"]),
    );
    if (allIDs.has(entry.id)) {
      throw new Error(`Duplicate portable transcript id ${String(entry.id)} in ${path}`);
    }
    allIDs.add(entry.id);
    wordIDs.add(entry.id);
  }
  for (const entry of readable) {
    validatePortableTranscriptEntry(entry, path, new Set(["readable-cleanup", "display"]));
    if (allIDs.has(entry.id)) {
      throw new Error(`Duplicate portable transcript id ${String(entry.id)} in ${path}`);
    }
    allIDs.add(entry.id);
  }
  for (const entry of manifest.transcripts) {
    if (entry.role === "raw-asr" || entry.role === "scripted") {
      if (entry.sourceTranscriptId !== undefined) {
        throw new Error(`Portable transcript ${entry.id} must not set sourceTranscriptId in ${path}`);
      }
      continue;
    }
    if (!entry.sourceTranscriptId || !wordIDs.has(entry.sourceTranscriptId)) {
      throw new Error(
        `Portable transcript ${String(entry.id)} has unknown sourceTranscriptId in ${path}`,
      );
    }
  }
  for (const entry of readable) {
    if (!entry.sourceTranscriptId || !wordIDs.has(entry.sourceTranscriptId)) {
      throw new Error(
        `Portable transcript ${String(entry.id)} has unknown sourceTranscriptId in ${path}`,
      );
    }
  }
}

function validatePortableTranscriptEntry(entry, path, roles) {
  const id = String(entry?.id ?? "");
  if (!/^[a-z0-9][a-z0-9-]{0,31}$/.test(id)) {
    throw new Error(`Invalid portable transcript id ${JSON.stringify(id)} in ${path}`);
  }
  if (!roles.has(entry?.role)) {
    throw new Error(`Unsupported portable transcript role ${String(entry?.role)} in ${path}`);
  }
  if (typeof entry?.format !== "string" || entry.format.trim() === "") {
    throw new Error(`Invalid portable transcript format for ${id} in ${path}`);
  }
  const ref = entry?.payloadRef;
  if (
    !ref ||
    !/^CASSINI_TX_[A-Z0-9_]+_PAYLOAD_$/.test(ref.prefix) ||
    !Number.isInteger(ref.chunkCount) ||
    ref.chunkCount < 1
  ) {
    throw new Error(`Invalid transcript payloadRef for ${id} in ${path}`);
  }
  if (ref.encoding !== "base64url+gzip+utf8json") {
    throw new Error(`Unsupported transcript encoding for ${id} in ${path}`);
  }
  if (!/^[0-9a-f]{64}$/.test(String(ref.sha256 ?? ""))) {
    throw new Error(`Invalid transcript digest for ${id} in ${path}`);
  }
  if (
    !Number.isInteger(ref.rawBytes) ||
    ref.rawBytes < 0 ||
    !Number.isInteger(ref.gzipBytes) ||
    ref.gzipBytes < 0 ||
    typeof ref.mime !== "string" ||
    ref.mime.trim() === ""
  ) {
    throw new Error(`Invalid transcript payload metadata for ${id} in ${path}`);
  }
}

function pickPortableEntry(entries, role, sourceTranscriptId) {
  const candidates = Array.isArray(entries)
    ? entries.filter((entry) => !role || entry?.role === role)
    : [];
  if (candidates.length === 0) {
    return null;
  }
  if (sourceTranscriptId) {
    const paired = candidates.find((entry) => entry?.sourceTranscriptId === sourceTranscriptId);
    return paired ?? null;
  }
  return candidates.find((entry) => entry?.default) ?? candidates[0];
}

function decodePortableTranscriptBody(tags, entry, path) {
  const ref = entry?.payloadRef;
  const prefix = safeToString(ref?.prefix);
  const chunkCount = safeToInt(ref?.chunkCount, 0);
  if (!/^CASSINI_TX_[A-Z0-9_]+_PAYLOAD_$/.test(prefix) || chunkCount <= 0) {
    throw new Error(`Invalid transcript payloadRef for ${String(entry?.id)} in ${path}`);
  }
  if (ref?.encoding !== "base64url+gzip+utf8json") {
    throw new Error(`Unsupported transcript encoding for ${String(entry?.id)} in ${path}`);
  }
  const raw = decodePortableChunkSet({
    tags,
    prefix,
    chunkCount,
    expectedSHA256: ref.sha256,
    expectedRawBytes: ref.rawBytes,
    expectedGzipBytes: ref.gzipBytes,
    label: `transcript ${String(entry?.id)} in ${path}`,
  });
  return JSON.parse(raw.toString("utf8"));
}

function decodePortableChunkSet({
  tags,
  prefix,
  chunkCount,
  expectedSHA256,
  expectedRawBytes,
  expectedGzipBytes,
  label,
}) {
  let encoded = "";
  for (let index = 0; index < chunkCount; index += 1) {
    const key = `${prefix}${String(index).padStart(3, "0")}`;
    if (typeof tags[key] !== "string" || tags[key].trim() === "") {
      throw new Error(`Missing payload chunk ${key} for ${label}`);
    }
    encoded += tags[key];
  }
  const compressed = Buffer.from(encoded, "base64url");
  const gzipBytes = safeToInt(expectedGzipBytes, 0);
  if (gzipBytes <= 0 || compressed.byteLength !== gzipBytes) {
    throw new Error(`${label} gzip byte count mismatch`);
  }
  const raw = gunzipSync(compressed);
  const rawBytes = safeToInt(expectedRawBytes, 0);
  if (rawBytes <= 0 || raw.byteLength !== rawBytes) {
    throw new Error(`${label} raw byte count mismatch`);
  }
  const expected = safeToString(expectedSHA256).trim();
  if (!/^[0-9a-f]{64}$/.test(expected)) {
    throw new Error(`${label} has a missing or invalid sha256`);
  }
  const actual = createHash("sha256").update(raw).digest("hex");
  if (actual !== expected) {
    throw new Error(`${label} sha256 mismatch`);
  }
  return raw;
}

export function buildTranscriptWordsFromPortable(portable) {
  const speakers = normalizeSpeakers(portable.speakers || []);
  const items = Array.isArray(portable.transcript?.items) ? portable.transcript.items : [];
  const segments = items.map((item, index) => {
    const segmentId = typeof item?.id === "string" && item.id.trim() !== "" ? item.id : `seg_${String(index).padStart(6, "0")}`;
    const startMs = safeToInt(item?.startMs, 0);
    const endMs = safeToInt(item?.endMs, startMs);
    const text = typeof item?.text === "string" ? item.text : "";
    const wordText = text.trim();
    if (!wordText || /\s/u.test(text)) {
      throw new Error(`portable transcript item ${index} must contain exactly one word`);
    }
    const words = [{ id: "w_0", text: wordText, startMs, endMs }];
    const speaker = typeof item?.speaker === "string" && item.speaker.trim() !== "" ? item.speaker : undefined;

    return {
      id: segmentId,
      speaker: speaker || undefined,
      startMs,
      endMs,
      text,
      words: words.map((word) => ({
        ...word,
        id: `${segmentId}:${word.id}`,
      })),
    };
  });

  return {
    version: "transcript.words.v1",
    media: {
      src: "meeting.opus",
      durationMs: safeToInt(portable.meeting?.durationMs, 0),
      sha256: safeToString(portable.integrity?.opusAudioSha256),
    },
    speakers,
    segments,
  };
}

function extractPortableReadableWords(segment, segmentId) {
  if (!Array.isArray(segment?.words)) {
    return [];
  }
  return segment.words.flatMap((word, index) => {
    const text = safeToString(word?.text).trim();
    if (!text) {
      return [];
    }
    const startMs = safeToInt(word?.startMs, NaN);
    const endMs = safeToInt(word?.endMs, startMs);
    if (!Number.isFinite(startMs) || !Number.isFinite(endMs)) {
      return [];
    }
    return [{
      id: typeof word?.id === "string" && word.id.trim() !== "" ? word.id : `${segmentId}:w_${index}`,
      text,
      startMs,
      endMs,
    }];
  });
}

function extractTranscriptArtifactWords(segment, segmentId) {
  // Go transcript artifacts do not currently ship per-word IDs. Synthesize
  // stable IDs here so cleaned display tokens can align back to exact source
  // words and preserve word seek in exported sites.
  if (!Array.isArray(segment?.words)) {
    return [];
  }
  return segment.words.flatMap((word, index) => {
    const text = safeToString(word?.text).trim();
    if (!text) {
      return [];
    }
    const startMs = safeToInt(word?.startMs, NaN);
    const endMs = safeToInt(word?.endMs, startMs);
    if (!Number.isFinite(startMs) || !Number.isFinite(endMs)) {
      return [];
    }
    return [{
      id: typeof word?.id === "string" && word.id.trim() !== "" ? word.id : `${segmentId}:w_${index}`,
      text,
      startMs,
      endMs,
    }];
  });
}

export function buildReadableTranscriptFromPortable(portable, transcript) {
  const provided = portable.readableTranscript;
  const speakers = normalizeSpeakers(portable.speakers || transcript.speakers || []);
  const validSpeakerIds = new Set(speakers.map((speaker) => speaker.id));

  if (
    provided &&
    provided.version === "transcript.readable.v1" &&
    Array.isArray(provided.segments)
  ) {
    return {
      version: "transcript.readable.v1",
      media: {
        src: transcript.media.src,
        durationMs: transcript.media.durationMs,
        sha256: transcript.media.sha256,
      },
      speakers: normalizeSpeakers(provided.speakers || speakers),
      segments: provided.segments.map((segment, index) => {
        const segmentId =
          typeof segment?.id === "string" && segment.id.trim() !== ""
            ? segment.id
            : `readable_${String(index).padStart(6, "0")}`;
        const sourceSegmentIds = Array.isArray(segment?.sourceSegmentIds)
          ? segment.sourceSegmentIds.filter((value) => typeof value === "string" && value.trim() !== "")
          : [];
        const speaker =
          typeof segment?.speaker === "string" && segment.speaker.trim() !== "" && validSpeakerIds.has(segment.speaker)
            ? segment.speaker
            : undefined;
        const words = extractPortableReadableWords(segment, segmentId);
        return {
          id: segmentId,
          speaker,
          startMs: safeToInt(segment?.startMs, 0),
          endMs: safeToInt(segment?.endMs, safeToInt(segment?.startMs, 0)),
          text: typeof segment?.text === "string" ? segment.text : "",
          sourceSegmentIds,
          ...(words.length > 0 ? { words } : {}),
        };
      }),
      sourceTranscriptVersion: provided.sourceTranscriptVersion ?? "transcript.words.v1",
    };
  }

  return {
    version: "transcript.readable.v1",
    media: {
      src: transcript.media.src,
      durationMs: transcript.media.durationMs,
      sha256: transcript.media.sha256,
    },
    speakers,
    sourceTranscriptVersion: "transcript.words.v1",
    segments: groupTranscriptSegmentsAsReadable(transcript.segments || []),
  };
}

function buildReadableDisplaySourceBlocks(readable, speakerLabels) {
  return readable.segments.map((segment, index) => {
    const blockId =
      typeof segment?.id === "string" && segment.id.trim() !== ""
        ? segment.id
        : `rseg_${String(index).padStart(6, "0")}`;
    const sourceSegmentIds = Array.isArray(segment?.sourceSegmentIds) ? [...segment.sourceSegmentIds] : [];
    return {
      id: blockId,
      speaker: segment?.speaker,
      speakerLabel: segment?.speaker ? speakerLabels.get(segment.speaker) || segment.speaker : "Unknown speaker",
      startMs: safeToInt(segment?.startMs, 0),
      endMs: safeToInt(segment?.endMs, safeToInt(segment?.startMs, 0)),
      text: typeof segment?.text === "string" ? segment.text : "",
      sourceSegmentIds,
      words: extractPortableReadableWords(segment, blockId),
    };
  });
}

export function groupTranscriptSegmentsAsReadable(segments) {
  const grouped = [];
  let current = null;
  const hardGapMs = 4200;
  const softGapMs = 2200;
  const targetParagraphWords = 96;
  const targetParagraphDurationMs = 45000;
  const maxParagraphWords = 140;
  const maxParagraphDurationMs = 90000;
  const minStandaloneWords = 18;
  const minStandaloneDurationMs = 8000;

  const flush = () => {
    if (!current || current.sourceSegments.length === 0) {
      current = null;
      return;
    }
    grouped.push({
      id: `r_${current.sourceSegments[0].id}`,
      speaker: current.speaker,
      startMs: current.startMs,
      endMs: current.endMs,
      text: joinSegmentTexts(current.sourceSegments),
      sourceSegmentIds: current.sourceSegments.map((segment) => segment.id),
    });
    current = null;
  };

  const normalizedSegments = Array.isArray(segments) ? segments : [];
  for (const segment of normalizedSegments) {
    if (!segment || typeof segment !== "object") {
      continue;
    }
    const speaker = typeof segment.speaker === "string" && segment.speaker.trim() !== "" ? segment.speaker : undefined;
    const startMs = safeToInt(segment.startMs, 0);
    const endMs = safeToInt(segment.endMs, startMs);
    const segmentWordCount = Array.isArray(segment.words) && segment.words.length > 0
      ? segment.words.length
      : countWordsInText(segment.text);

    if (!current) {
      current = {
        speaker,
        startMs,
        endMs,
        wordCount: segmentWordCount,
        text: safeToString(segment.text).trim(),
        sourceSegments: [segment],
      };
      continue;
    }

    const gapMs = Math.max(0, startMs - current.endMs);
    const durationMs = Math.max(0, endMs - current.startMs);
    const nextWordCount = current.wordCount + segmentWordCount;
    const speakerChanged = current.speaker !== speaker;
    const paragraphTargetReached = (
      current.wordCount >= targetParagraphWords ||
      (current.endMs - current.startMs) >= targetParagraphDurationMs
    );
    const naturalBoundary = endsSentence(current.text) || gapMs >= softGapMs;
    const shouldFlush = (
      speakerChanged ||
      gapMs > hardGapMs ||
      durationMs > maxParagraphDurationMs ||
      nextWordCount > maxParagraphWords ||
      (paragraphTargetReached && naturalBoundary)
    );

    if (shouldFlush) {
      flush();
      current = {
        speaker,
        startMs,
        endMs,
        wordCount: segmentWordCount,
        text: safeToString(segment.text).trim(),
        sourceSegments: [segment],
      };
      continue;
    }

    current.sourceSegments.push(segment);
    current.endMs = endMs;
    current.wordCount = nextWordCount;
    current.text = joinSegmentTexts(current.sourceSegments);
  }

  flush();
  return mergeSmallReadableSegments(grouped, {
    maxParagraphWords,
    maxParagraphDurationMs,
    hardGapMs,
    minStandaloneWords,
    minStandaloneDurationMs,
  });
}

function mergeSmallReadableSegments(grouped, options) {
  const merged = [];
  const items = Array.isArray(grouped) ? grouped : [];
  for (const segment of items) {
    const previous = merged.at(-1);
    if (!previous) {
      merged.push(segment);
      continue;
    }

    const previousWordCount = countWordsInText(previous.text);
    const currentWordCount = countWordsInText(segment.text);
    const previousDurationMs = Math.max(0, safeToInt(previous.endMs, 0) - safeToInt(previous.startMs, 0));
    const currentDurationMs = Math.max(0, safeToInt(segment.endMs, 0) - safeToInt(segment.startMs, 0));
    const gapMs = Math.max(0, safeToInt(segment.startMs, 0) - safeToInt(previous.endMs, 0));
    const combinedWordCount = previousWordCount + currentWordCount;
    const combinedDurationMs = Math.max(0, safeToInt(segment.endMs, 0) - safeToInt(previous.startMs, 0));
    const sameSpeaker = previous.speaker === segment.speaker;
    const oneSideTooSmall = (
      previousWordCount < options.minStandaloneWords ||
      currentWordCount < options.minStandaloneWords ||
      previousDurationMs < options.minStandaloneDurationMs ||
      currentDurationMs < options.minStandaloneDurationMs
    );

    if (
      sameSpeaker &&
      gapMs <= options.hardGapMs &&
      combinedWordCount <= options.maxParagraphWords &&
      combinedDurationMs <= options.maxParagraphDurationMs &&
      oneSideTooSmall
    ) {
      previous.endMs = segment.endMs;
      previous.text = joinSegmentTexts([{ text: previous.text }, { text: segment.text }]);
      previous.sourceSegmentIds = [...(previous.sourceSegmentIds || []), ...(segment.sourceSegmentIds || [])];
      continue;
    }

    merged.push(segment);
  }
  return merged;
}

function joinSegmentTexts(segments) {
  let text = "";
  for (const segment of segments) {
    const part = safeToString(segment?.text).trim();
    if (!part) {
      continue;
    }
    if (!text) {
      text = part;
      continue;
    }
    if (/^[,.;:!?)]/.test(part)) {
      text += part;
      continue;
    }
    text += ` ${part}`;
  }
  return text;
}

function countWordsInText(text) {
  return safeToString(text)
    .trim()
    .split(/\s+/)
    .filter(Boolean)
    .length;
}

function endsSentence(text) {
  return /[.!?]["')\]]*$/.test(safeToString(text).trim());
}

export function buildDisplayTranscriptFromArtifacts(transcript, readable) {
  const speakers = normalizeSpeakers(transcript.speakers || []);
  const speakerLabels = new Map(speakers.map((speaker) => [speaker.id, speaker.label]));
  const transcriptSegments = Array.isArray(transcript.segments) ? transcript.segments : [];
  const segmentById = new Map(transcriptSegments.map((segment) => [segment.id, segment]));
  const sourceBlocks =
    readable && readable.version === "transcript.readable.v1" && Array.isArray(readable.segments)
      ? buildReadableDisplaySourceBlocks(readable, speakerLabels)
      : transcriptSegments.map((segment) => ({
          id: `d_${segment.id}`,
          speaker: segment.speaker,
          speakerLabel: segment.speaker ? speakerLabels.get(segment.speaker) || segment.speaker : "Unknown speaker",
          startMs: segment.startMs,
          endMs: segment.endMs,
          text: segment.text,
          sourceSegmentIds: [segment.id],
          words: [],
        }));

  return {
    version: "transcript.display.v1",
    media: { ...transcript.media },
    speakers,
    blocks: sourceBlocks.map((block, blockIndex) => {
      const sourceSegmentIds = Array.isArray(block.sourceSegmentIds)
        ? block.sourceSegmentIds.filter((value) => typeof value === "string" && value.trim() !== "")
        : [];
      const sourceSegments = resolveDisplaySourceSegments({
        block,
        sourceSegmentIds,
        segmentById,
        transcriptSegments,
      });
      const sourceWordsFromTranscript = sourceSegments.flatMap((segment, index) =>
        extractTranscriptArtifactWords(
          segment,
          typeof segment?.id === "string" && segment.id.trim() !== ""
            ? segment.id
            : `seg_${String(index).padStart(6, "0")}`,
        ),
      );
      const sourceWordsFromReadable = extractPortableReadableWords(
        block,
        `block_${String(blockIndex).padStart(6, "0")}`,
      );
      const sourceWords = sourceWordsFromTranscript.length > 0 ? sourceWordsFromTranscript : sourceWordsFromReadable;
      const sourceWordById = new Map(
        sourceWords
          .filter((word) => typeof word.id === "string" && word.id.trim() !== "")
          .map((word) => [word.id, word]),
      );
      const sourceWordIndexById = new Map(
        sourceWords
          .filter((word) => typeof word.id === "string" && word.id.trim() !== "")
          .map((word, index) => [word.id, index]),
      );
      const alignment = alignReadableTokensToSourceWords(sourceWords, typeof block.text === "string" ? block.text : "");
      const fallbackStartMs = sourceWords.length > 0
        ? safeToInt(sourceWords[0]?.startMs, safeToInt(block.startMs, 0))
        : sourceSegments.length > 0
          ? safeToInt(sourceSegments[0]?.startMs, safeToInt(block.startMs, 0))
          : safeToInt(block.startMs, 0);
      const fallbackEndMs = sourceWords.length > 0
        ? safeToInt(sourceWords[sourceWords.length - 1]?.endMs, safeToInt(block.endMs, fallbackStartMs))
        : sourceSegments.length > 0
          ? safeToInt(sourceSegments[sourceSegments.length - 1]?.endMs, safeToInt(block.endMs, fallbackStartMs))
          : safeToInt(block.endMs, fallbackStartMs);
      const exactTokens = alignment.tokens.map((token) => {
        const matchedWords = token.sourceWordIds
          .map((wordId) => sourceWordById.get(wordId))
          .filter(Boolean);
        if (matchedWords.length > 0) {
          return {
            text: token.text,
            spaceBefore: token.spaceBefore,
            kind: token.kind,
            sourceWordIds: [...token.sourceWordIds],
            startMs: Math.min(...matchedWords.map((word) => safeToInt(word.startMs, fallbackStartMs))),
            endMs: Math.max(...matchedWords.map((word) => safeToInt(word.endMs, fallbackEndMs))),
            alignment: "source",
          };
        }
        return {
          text: token.text,
          spaceBefore: token.spaceBefore,
          kind: token.kind,
          sourceWordIds: [...token.sourceWordIds],
          alignment: token.kind === "punctuation" ? "none" : "none",
        };
      });
      const tokens = interpolateUntimedWordRuns(
        exactTokens,
        fallbackStartMs,
        fallbackEndMs,
        sourceWords,
        sourceWordIndexById,
      );
      const wordCount = tokens.filter((token) => token.kind === "word").length;
      const timedWordCount = tokens.filter(
        (token) =>
          token.kind === "word" &&
          Number.isInteger(token.startMs) &&
          Number.isInteger(token.endMs),
      ).length;
      const timedTokens = tokens.filter(
        (token) =>
          Number.isInteger(token.startMs) &&
          Number.isInteger(token.endMs),
      );
      return {
        id:
          typeof block.id === "string" && block.id.trim() !== ""
            ? block.id
            : `dseg_${String(blockIndex).padStart(6, "0")}`,
        speaker:
          typeof block.speaker === "string" && speakerLabels.has(block.speaker) ? block.speaker : undefined,
        speakerLabel:
          typeof block.speakerLabel === "string" && block.speakerLabel.trim() !== ""
            ? block.speakerLabel
            : speakerLabels.get(block.speaker) || "Unknown speaker",
        startMs: timedTokens.length > 0 ? safeToInt(timedTokens[0].startMs, fallbackStartMs) : fallbackStartMs,
        endMs: timedTokens.length > 0
          ? safeToInt(timedTokens[timedTokens.length - 1].endMs, fallbackEndMs)
          : fallbackEndMs,
        text: typeof block.text === "string" ? block.text : "",
        sourceSegmentIds,
        wordCount,
        timedWordCount,
        timingCoverage: wordCount === 0 ? 1 : timedWordCount / wordCount,
        tokens,
      };
    }),
    sourceTranscriptVersion: transcript.version,
    sourceReadableTranscriptVersion: readable?.version,
  };
}

function interpolateUntimedWordRuns(tokens, fallbackStartMs, fallbackEndMs, sourceWords, sourceWordIndexById) {
  const next = tokens.map((token) => ({ ...token, sourceWordIds: [...token.sourceWordIds] }));
  const wordTokenIndexes = next
    .map((token, index) => (token.kind === "word" ? index : -1))
    .filter((index) => index >= 0);

  let cursor = 0;
  while (cursor < wordTokenIndexes.length) {
    const tokenIndex = wordTokenIndexes[cursor];
    if (tokenHasTiming(next[tokenIndex])) {
      cursor += 1;
      continue;
    }

    const runStart = cursor;
    while (cursor < wordTokenIndexes.length && !tokenHasTiming(next[wordTokenIndexes[cursor]])) {
      cursor += 1;
    }
    const runTokenIndexes = wordTokenIndexes.slice(runStart, cursor);
    const prevTimedToken = runStart > 0 ? next[wordTokenIndexes[runStart - 1]] : null;
    const nextTimedToken = cursor < wordTokenIndexes.length ? next[wordTokenIndexes[cursor]] : null;
    const hasPrevAnchor = tokenHasTiming(prevTimedToken);
    const hasNextAnchor = tokenHasTiming(nextTimedToken);
    if (!hasPrevAnchor || !hasNextAnchor) {
      continue;
    }
    if (!shouldInterpolateUntimedRun({
      tokens: next,
      runTokenIndexes,
      prevTimedToken,
      nextTimedToken,
      sourceWords,
      sourceWordIndexById,
    })) {
      continue;
    }
    const { startMs, endMs } = resolveInterpolatedSpan({
      prevTimedToken,
      nextTimedToken,
      fallbackStartMs,
      fallbackEndMs,
    });
    const span = Math.max(0, endMs - startMs);

    // Preserve exact source matches when we have them, but keep rewritten runs
    // seekable by spreading them across the surrounding source span.
    for (let index = 0; index < runTokenIndexes.length; index += 1) {
      const runTokenIndex = runTokenIndexes[index];
      const tokenStart = runTokenIndexes.length <= 1
        ? startMs
        : startMs + Math.floor((span * index) / runTokenIndexes.length);
      const tokenEnd = runTokenIndexes.length <= 1
        ? endMs
        : startMs + Math.floor((span * (index + 1)) / runTokenIndexes.length);
      next[runTokenIndex] = {
        ...next[runTokenIndex],
        startMs: tokenStart,
        endMs: Math.max(tokenEnd, tokenStart),
        alignment: "interpolated",
      };
    }
  }

  return next;
}

function shouldInterpolateUntimedRun({
  tokens,
  runTokenIndexes,
  prevTimedToken,
  nextTimedToken,
  sourceWords,
  sourceWordIndexById,
}) {
  const prevIndexes = resolveTokenSourceIndexes(prevTimedToken, sourceWordIndexById);
  const nextIndexes = resolveTokenSourceIndexes(nextTimedToken, sourceWordIndexById);
  if (prevIndexes.length === 0 || nextIndexes.length === 0) {
    return false;
  }

  const prevEndIndex = Math.max(...prevIndexes);
  const nextStartIndex = Math.min(...nextIndexes);
  if (nextStartIndex <= prevEndIndex) {
    return false;
  }

  const runWords = runTokenIndexes
    .map((tokenIndex) => normalizeAlignmentToken(tokens[tokenIndex]?.text ?? ""))
    .filter(Boolean);
  if (runWords.length === 0) {
    return false;
  }

  const sourceGapWords = sourceWords
    .slice(prevEndIndex + 1, nextStartIndex)
    .map((word) => normalizeAlignmentToken(word?.text ?? ""))
    .filter(Boolean);

  if (sourceGapWords.length === 0) {
    return false;
  }

  const sourceGapSet = new Set(sourceGapWords);
  const intersectionCount = runWords.filter((word) => sourceGapSet.has(word)).length;
  const overlap = intersectionCount / Math.max(runWords.length, sourceGapWords.length);
  return intersectionCount > 0 && overlap >= 0.5 && Math.abs(runWords.length - sourceGapWords.length) <= 2;
}

function resolveTokenSourceIndexes(token, sourceWordIndexById) {
  if (!token || !Array.isArray(token.sourceWordIds)) {
    return [];
  }
  return token.sourceWordIds
    .map((wordId) => sourceWordIndexById.get(wordId))
    .filter((index) => Number.isInteger(index));
}

function resolveInterpolatedSpan({ prevTimedToken, nextTimedToken, fallbackStartMs, fallbackEndMs }) {
  let startMs = tokenHasTiming(prevTimedToken) ? safeToInt(prevTimedToken.endMs, fallbackStartMs) : fallbackStartMs;
  let endMs = tokenHasTiming(nextTimedToken) ? safeToInt(nextTimedToken.startMs, fallbackEndMs) : fallbackEndMs;

  if (endMs <= startMs) {
    const altStartMs = tokenHasTiming(prevTimedToken)
      ? safeToInt(prevTimedToken.startMs, fallbackStartMs)
      : fallbackStartMs;
    const altEndMs = tokenHasTiming(nextTimedToken)
      ? safeToInt(nextTimedToken.endMs, fallbackEndMs)
      : fallbackEndMs;
    startMs = Math.min(altStartMs, altEndMs);
    endMs = Math.max(altStartMs, altEndMs);
  }

  return {
    startMs,
    endMs: Math.max(endMs, startMs),
  };
}

function tokenHasTiming(token) {
  return Boolean(token) && Number.isInteger(token.startMs) && Number.isInteger(token.endMs);
}

export function ensureDisplayTranscript(targetMeetingDir) {
  const displayPath = join(targetMeetingDir, "transcript.display.v1.json");
  const transcriptPath = join(targetMeetingDir, "transcript.words.v1.json");
  if (existsSync(displayPath) || !existsSync(transcriptPath)) {
    return;
  }
  const transcript = JSON.parse(readFileSync(transcriptPath, "utf8"));
  const readablePath = join(targetMeetingDir, "transcript.readable.v1.json");
  const readable = existsSync(readablePath) ? JSON.parse(readFileSync(readablePath, "utf8")) : null;
  const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
  writeFileSync(displayPath, `${JSON.stringify(display, null, 2)}\n`, "utf8");
}

export function normalizeSpeakers(value) {
  if (!Array.isArray(value)) {
    return [];
  }

  const speakers = [];
  const seen = new Set();
  for (const item of value) {
    if (!item || typeof item !== "object") {
      continue;
    }
    const id = safeToString(item.id);
    const label = safeToString(item.label) || id;
    if (id.trim() === "" || seen.has(id)) {
      continue;
    }
    seen.add(id);
    speakers.push({ id, label });
  }
  return speakers;
}

export function readManifest(sourceDir, meetingId) {
  const manifestPath = join(sourceDir, "manifest.json");
  if (!existsSync(manifestPath)) {
    return {};
  }
  try {
    return JSON.parse(readFileSync(manifestPath, "utf8"));
  } catch (error) {
    throw new Error(`could not parse manifest for ${meetingId}: ${error}`);
  }
}

export function copyPublicMeetingFiles(sourceMeetingDir, targetMeetingDir, manifest) {
  const filesToCopy = new Set([
    "manifest.json",
    "meeting.webm",
    "transcript.words.v1.json",
    "transcript.display.v1.json",
    "transcript.readable.v1.json",
    "captions.vtt",
    "chapters.vtt",
    "timeline.map.v1.json",
  ]);

  if (manifest && typeof manifest === "object" && manifest.files && typeof manifest.files === "object") {
    for (const value of Object.values(manifest.files)) {
      if (typeof value === "string" && value.trim() !== "") {
        filesToCopy.add(value);
      }
    }
  }

  for (const optionalFile of ["transcript.display.v1.json", "transcript.readable.v1.json", "chapters.vtt"]) {
    const optionalPath = join(sourceMeetingDir, optionalFile);
    if (existsSync(optionalPath)) {
      filesToCopy.add(optionalFile);
    }
  }

  for (const relativePath of filesToCopy) {
    const sourcePath = join(sourceMeetingDir, relativePath);
    if (!existsSync(sourcePath)) {
      continue;
    }
    const targetPath = join(targetMeetingDir, relativePath);
    mkdirSync(dirname(targetPath), { recursive: true });
    cpSync(sourcePath, targetPath, { recursive: true });
  }
}

export function isPortableMeeting(fileName) {
  return extname(fileName).toLowerCase() === ".opus";
}

// portableRoomFields returns the room and lineage fields a catalog entry should
// carry, or {} when the meeting has none of them.
//
// Absent rather than empty: an entry with `roomId: ""` would read as "this
// meeting has a room whose id is the empty string", and every consumer — the
// viewer's grouping, the CLI's --room filter — would have to check presence
// AND emptiness. Hand-packed meetings and Talk recordings whose room lookup
// failed genuinely have no room, and saying so is the correct answer.
//
export function portableRoomFields(portable) {
  const fields = {};
  const roomId = typeof portable?.meeting?.roomId === "string" ? portable.meeting.roomId.trim() : "";
  const jobId = typeof portable?.meeting?.jobId === "string" ? portable.meeting.jobId.trim() : "";
  // Strict on the type, like every other optional field here and in
  // catalog.ts: a wrong-typed value means "not recorded", never "coerce it".
  // Number("2") is 2, so a lenient read would quietly promote a string in a
  // hand-edited or third-party manifest into a lineage claim.
  const attemptNumber = portable?.meeting?.attemptNumber;
  if (roomId !== "") {
    fields.roomId = roomId;
  }
  // Which job and attempt produced the artifact (D-640). jobId normally equals
  // the entry's own id — the operator publishes meetings/<jobID>.opus — and is
  // carried anyway, because that equality is a convention of one publish path
  // and not something a consumer should have to assume.
  if (jobId !== "") {
    fields.jobId = jobId;
  }
  if (typeof attemptNumber === "number" && Number.isInteger(attemptNumber) && attemptNumber > 0) {
    fields.attemptNumber = attemptNumber;
  }
  return fields;
}

// preferredPortableTitle returns the title embedded in a portable meeting's
// manifest when it is a real human-readable name (e.g. the Talk room name the
// operator resolved at recording time, D-462). Packer defaults — the meeting
// id echoed back or the generic "Cassini Meeting" — yield "" so the caller
// falls back to id-derived naming.
export function preferredPortableTitle(portable, meetingId) {
  const raw = typeof portable?.meeting?.title === "string" ? portable.meeting.title.trim() : "";
  if (raw === "") {
    return "";
  }
  if (raw === meetingId || raw === stripVariantSuffix(meetingId)) {
    return "";
  }
  if (raw === "Cassini Meeting") {
    return "";
  }
  return raw;
}

// describeMeeting picks the catalog title and dateLabel for a meeting.
//
// The dateLabel answers "when did this meeting happen", NOT "when did we
// process it" — the two diverge every time a pack is rebuilt, and a re-run of
// the whole archive would otherwise stamp every meeting with the rebuild day
// (D-685). Sources, best first:
//
//   1. recordedAtLocal — the recording's own wall clock, the only field that
//      states when people were actually in the room.
//   2. the meeting id — filename stamps and ULID job ids both encode the
//      recording start. A slug-only id like "daily-meeting-2026-04-08" yields a
//      date with no time, which is still the right DAY.
//   3. createdAtUtc — when the pack was written. Wrong by construction for any
//      rebuild, and kept only as a last resort so a meeting with no other
//      timestamp still parses and sorts instead of showing a raw slug (D-588).
//
// The title always comes from the id.
export function describeMeeting(meetingId, createdAtUtc = "", recordedAtLocal = "") {
  const described = describeMeetingFromId(meetingId);
  const { dateFromId, ...describedFields } = described;

  const recordedDateLabel = dateLabelFromLocalTimestamp(recordedAtLocal);
  if (recordedDateLabel !== "") {
    return { title: described.title, dateLabel: recordedDateLabel };
  }
  if (dateFromId) {
    return describedFields;
  }
  const metadataDateLabel = dateLabelFromTimestamp(createdAtUtc);
  if (metadataDateLabel !== "") {
    return { title: described.title, dateLabel: metadataDateLabel };
  }
  return describedFields;
}

// describeMeetingFromId also reports `dateFromId`: false means the dateLabel is
// just the raw id echoed back because nothing in it looked like a timestamp.
// describeMeeting uses that to decide whether pack metadata is worth reaching
// for; the flag is stripped before the result leaves describeMeeting.
function describeMeetingFromId(meetingId) {
  const normalizedMeetingId = stripVariantSuffix(meetingId);
  const colonTimeStamp = parseTimestampFromDoubledDashParts(normalizedMeetingId, "--");
  if (colonTimeStamp) {
    return { ...colonTimeStamp, dateFromId: true };
  }

  const modernStamp = /^(.*)--(\d{8})T(\d{2})(\d{2})(\d{2})$/.exec(normalizedMeetingId);
  if (modernStamp) {
    const [, rawTitle, yyyymmdd, hour, minute] = modernStamp;
    return {
      title: toTitleCase(rawTitle),
      dateLabel: `${yyyymmdd.slice(0, 4)}-${yyyymmdd.slice(4, 6)}-${yyyymmdd.slice(6, 8)} ${hour}:${minute}`,
      dateFromId: true,
    };
  }

  const legacyStamp = /^(.*)--(\d{4})-(\d{2})-(\d{2})--(\d{2})-(\d{2})-(\d{2})$/.exec(normalizedMeetingId);
  if (legacyStamp) {
    const [, rawTitle, year, month, day, hour, minute] = legacyStamp;
    return {
      title: toTitleCase(rawTitle),
      dateLabel: `${year}-${month}-${day} ${hour}:${minute}`,
      dateFromId: true,
    };
  }

  // Some backfilled dailies have only a calendar date in their id and a
  // generic source basename (recording.mkv). They therefore have neither a
  // trustworthy createdAtUtc nor a recordedAtLocal after reprocessing. The
  // date carried by the id is still authoritative even though no start time is
  // recoverable (D-685).
  const dateOnlyStamp = /^(.*)-(\d{4})-(\d{2})-(\d{2})$/.exec(
    normalizedMeetingId,
  );
  if (dateOnlyStamp) {
    const [, rawTitle, year, month, day] = dateOnlyStamp;
    if (rawTitle && isValidCalendarDate(year, month, day)) {
      return {
        title: toTitleCase(rawTitle),
        dateLabel: `${year}-${month}-${day}`,
        dateFromId: true,
      };
    }
  }

  // Talk recordings are named by the operator's ULID job id, which carries no
  // human-readable name but does encode the recording start time. Surface that
  // timestamp instead of showing the raw id as both title and date (D-462).
  const ulidDateLabel = dateLabelFromUlid(normalizedMeetingId);
  if (ulidDateLabel) {
    return {
      title: "Untitled meeting",
      dateLabel: ulidDateLabel,
      dateFromId: true,
    };
  }

  return {
    title: toTitleCase(normalizedMeetingId),
    dateLabel: normalizedMeetingId,
    dateFromId: false,
  };
}

// Canonical 26-char Crockford base32 ULID. The first character of a real ULID
// never exceeds 7 (48-bit timestamp bound). Deliberately uppercase-only —
// operator job ids are always uppercase, and rejecting lowercase keeps
// human-chosen meeting names from ever matching.
const ULID_PATTERN = /^[0-7][0-9ABCDEFGHJKMNPQRSTVWXYZ]{25}$/;
const CROCKFORD_ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
// Plausibility window for a decoded ULID timestamp; a 26-char Crockford-only
// word that is not a ULID almost never decodes into it.
const ULID_TIMESTAMP_MIN_MS = Date.UTC(2015, 0, 1);
const ULID_TIMESTAMP_MAX_MS = Date.UTC(2100, 0, 1);

// dateLabelFromUlid returns "YYYY-MM-DD HH:MM" (UTC) decoded from a ULID's
// 48-bit timestamp prefix, or "" when the id is not a plausible ULID.
function dateLabelFromUlid(meetingId) {
  if (!ULID_PATTERN.test(meetingId)) {
    return "";
  }
  let ms = 0;
  for (const char of meetingId.slice(0, 10)) {
    ms = ms * 32 + CROCKFORD_ALPHABET.indexOf(char);
  }
  if (ms < ULID_TIMESTAMP_MIN_MS || ms > ULID_TIMESTAMP_MAX_MS) {
    return "";
  }
  return formatUtcDateLabel(new Date(ms));
}

// dateLabelFromLocalTimestamp renders "YYYY-MM-DD HH:MM" from a pack's
// recordedAtLocal ("2026-03-10T12:30:00") — a LOCAL wall clock with no zone.
// Its digits are reformatted as-is rather than round-tripped through Date:
// Date.parse would read a zoneless string in the exporter's local zone, and the
// UTC render on the far side would shift a 12:30 meeting to 11:30 whenever the
// machine running the export is not on UTC. The viewer makes no timezone claim
// about these labels either (D-484), so the digits are the whole truth.
function dateLabelFromLocalTimestamp(value) {
  if (typeof value !== "string") {
    return "";
  }
  const match = /^(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2})(?::(\d{2})(?:\.\d{1,9})?)?$/.exec(
    value.trim(),
  );
  if (!match) {
    return "";
  }
  const [, year, month, day, hour, minute, second] = match;
  // Out-of-range fields mean the value is not a timestamp we can trust; fall
  // through to the next source rather than emit a label that sorts wrongly.
  if (!isValidCalendarDate(year, month, day)) {
    return "";
  }
  if (
    Number(hour) > 23 ||
    Number(minute) > 59 ||
    (second !== undefined && Number(second) > 59)
  ) {
    return "";
  }
  return `${year}-${month}-${day} ${hour}:${minute}`;
}

function isValidCalendarDate(year, month, day) {
  const y = Number(year);
  const m = Number(month);
  const d = Number(day);
  if (
    !Number.isInteger(y) ||
    !Number.isInteger(m) ||
    !Number.isInteger(d) ||
    m < 1 ||
    m > 12 ||
    d < 1
  ) {
    return false;
  }
  const leap = y % 4 === 0 && (y % 100 !== 0 || y % 400 === 0);
  const daysInMonth = [
    31,
    leap ? 29 : 28,
    31,
    30,
    31,
    30,
    31,
    31,
    30,
    31,
    30,
    31,
  ];
  return d <= daysInMonth[m - 1];
}

// dateLabelFromTimestamp renders "YYYY-MM-DD HH:MM" (UTC, same shape as the
// ULID-derived labels) from a pack's RFC3339 createdAtUtc, or "" when the
// value is absent or unparseable.
function dateLabelFromTimestamp(timestamp) {
  if (typeof timestamp !== "string" || timestamp.trim() === "") {
    return "";
  }
  const ms = Date.parse(timestamp.trim());
  if (!Number.isFinite(ms)) {
    return "";
  }
  return formatUtcDateLabel(new Date(ms));
}

function formatUtcDateLabel(date) {
  const pad = (value) => String(value).padStart(2, "0");
  return `${date.getUTCFullYear()}-${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())} ${pad(
    date.getUTCHours(),
  )}:${pad(date.getUTCMinutes())}`;
}

function stripVariantSuffix(meetingId) {
  return meetingId.replace(/--stt-[A-Za-z0-9._-]+$/, "");
}

export function describeSpeechToTextVariant(manifest) {
  const step = manifest?.provenance?.speechToText;
  if (!step || typeof step !== "object") {
    return "";
  }

  const engine = safeToString(step.engine);
  const model = safeToString(step.model);
  const backend = safeToString(step.backend);

  if (backend === "local-whisper" || engine === "faster-whisper" || engine === "whisper") {
    if (model) {
      return `Whisper ${model}`;
    }
    return "Whisper";
  }

  if (engine.includes("parakeet") || model.includes("parakeet")) {
    return describeParakeetVariant(model);
  }

  if (engine) {
    if (model && model !== engine) {
      return `${toTitleCase(engine)} ${model}`;
    }
    return toTitleCase(engine);
  }

  if (model) {
    return model;
  }

  return "";
}

function describeParakeetVariant(model) {
  if (!model) {
    return "Parakeet";
  }

  const normalized = model.replace(/^nvidia\//, "").replace(/^parakeet-/, "");
  return `Parakeet ${normalized}`;
}

export function describeVariantSuffix(meetingId) {
  const match = /--stt-([A-Za-z0-9._-]+)$/.exec(meetingId);
  if (!match) {
    return "";
  }
  return toTitleCase(match[1].replace(/\./g, "-"));
}

function parseTimestampFromDoubledDashParts(meetingId, joiner = "--") {
  const parts = meetingId.split(joiner);
  if (parts.length < 2) {
    return null;
  }

  const timeParts = parts.at(-1);
  const timeMatch = /^([0-9]{2}):([0-9]{2})(?:\:([0-9]{2}))?$/.exec(timeParts);
  if (!timeMatch) {
    return null;
  }
  const hour = timeMatch[1];
  const minute = timeMatch[2];
  const dateCandidate = parts.at(-2);

  const dateMatch = /^(\d{4})-(\d{2})-(\d{2})$/.exec(dateCandidate);
  if (dateMatch) {
    const [, year, month, day] = dateMatch;
    const rawTitle = parts.slice(0, -2).join(joiner);
    if (!rawTitle) {
      return null;
    }
    return {
      title: toTitleCase(rawTitle),
      dateLabel: `${year}-${month}-${day} ${hour}:${minute}`,
    };
  }

  if (parts.length !== 2) {
    return null;
  }

  const legacyDateMatch = /^(.*)-(\d{4})-(\d{2})-(\d{2})$/.exec(parts[0]);
  if (!legacyDateMatch) {
    return null;
  }
  const [, rawTitle, year, month, day] = legacyDateMatch;
  return {
    title: toTitleCase(rawTitle),
    dateLabel: `${year}-${month}-${day} ${hour}:${minute}`,
  };
}

export function toTitleCase(text) {
  return text
    .split("-")
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function safeToInt(value, fallback) {
  if (typeof value === "number" && Number.isFinite(value)) {
    return Math.trunc(value);
  }
  if (typeof value === "string") {
    const parsed = Number.parseInt(value, 10);
    if (Number.isFinite(parsed)) {
      return parsed;
    }
  }
  return fallback;
}

function resolveDisplaySourceSegments({ block, sourceSegmentIds, segmentById, transcriptSegments }) {
  const resolved = sourceSegmentIds.map((segmentId) => segmentById.get(segmentId)).filter(Boolean);
  if (
    resolved.length > 0 &&
    resolvedSegmentsLookCompatible({
      block,
      resolvedSegments: resolved,
    })
  ) {
    return resolved;
  }
  const blockStartMs = safeToInt(block?.startMs, 0);
  const blockEndMs = safeToInt(block?.endMs, blockStartMs);
  return transcriptSegments.filter((segment) => {
    const startMs = safeToInt(segment?.startMs, 0);
    const endMs = safeToInt(segment?.endMs, startMs);
    if (endMs < blockStartMs || startMs > blockEndMs) {
      return false;
    }
    if (typeof block?.speaker === "string" && segment?.speaker && segment.speaker !== block.speaker) {
      return false;
    }
    return true;
  });
}

function resolvedSegmentsLookCompatible({ block, resolvedSegments }) {
  const targetWordCount = tokenizeDisplayText(typeof block?.text === "string" ? block.text : "").filter(
    (token) => token.kind === "word",
  ).length;
  const resolvedWordCount = resolvedSegments.flatMap((segment) =>
    Array.isArray(segment?.words) ? segment.words : [],
  ).length;
  if (targetWordCount > 0 && resolvedWordCount < targetWordCount) {
    return false;
  }

  const blockStartMs = safeToInt(block?.startMs, 0);
  const blockEndMs = safeToInt(block?.endMs, blockStartMs);
  const blockDurationMs = Math.max(0, blockEndMs - blockStartMs);
  if (blockDurationMs <= 0) {
    return true;
  }
  const resolvedStartMs = Math.min(...resolvedSegments.map((segment) => safeToInt(segment?.startMs, blockStartMs)));
  const resolvedEndMs = Math.max(...resolvedSegments.map((segment) => safeToInt(segment?.endMs, blockEndMs)));
  const resolvedDurationMs = Math.max(0, resolvedEndMs - resolvedStartMs);
  return resolvedDurationMs >= Math.max(1000, Math.floor(blockDurationMs / 2));
}

function tokenizeDisplayText(text) {
  const tokenPattern = /[A-Za-z0-9]+(?:[.'’/_-][A-Za-z0-9]+)*|[^\w\s]/gu;
  const tokens = [];
  let match;
  let cursor = 0;
  while ((match = tokenPattern.exec(text)) !== null) {
    const prefix = text.slice(cursor, match.index);
    const tokenText = match[0];
    tokens.push({
      text: tokenText,
      spaceBefore: /\s/u.test(prefix),
      kind: isWordToken(tokenText) ? "word" : "punctuation",
    });
    cursor = match.index + tokenText.length;
  }
  return tokens;
}

function normalizeAlignmentToken(token) {
  return String(token ?? "")
    .replaceAll("’", "'")
    .toLowerCase()
    .replace(/^[^\w']+|[^\w']+$/gu, "");
}

function isWordToken(token) {
  return normalizeAlignmentToken(token) !== "";
}

function alignReadableTokensToSourceWords(sourceWords, readableText) {
  const tokens = tokenizeDisplayText(readableText);
  const targetWordPositions = [];
  const targetNorms = [];
  for (let index = 0; index < tokens.length; index += 1) {
    if (tokens[index].kind !== "word") {
      continue;
    }
    targetWordPositions.push(index);
    targetNorms.push(normalizeAlignmentToken(tokens[index].text));
  }
  const sourceNorms = sourceWords.map((word) => normalizeAlignmentToken(word?.text ?? ""));
  const dp = buildLcsTable(sourceNorms, targetNorms);
  const targetIndexToSourceWordIds = reconstructLcsAlignment({
    sourceWords,
    sourceNorms,
    targetWordPositions,
    targetNorms,
    dp,
  });
  return {
    tokens: tokens.map((token, index) => ({
      ...token,
      sourceWordIds: targetIndexToSourceWordIds.get(index) ?? [],
    })),
  };
}

function buildLcsTable(sourceNorms, targetNorms) {
  const rows = sourceNorms.length + 1;
  const cols = targetNorms.length + 1;
  const dp = Array.from({ length: rows }, () => Array(cols).fill(0));
  for (let sourceIndex = sourceNorms.length - 1; sourceIndex >= 0; sourceIndex -= 1) {
    for (let targetIndex = targetNorms.length - 1; targetIndex >= 0; targetIndex -= 1) {
      let best = Math.max(dp[sourceIndex + 1][targetIndex], dp[sourceIndex][targetIndex + 1]);
      if (
        sourceNorms[sourceIndex] &&
        sourceNorms[sourceIndex] === targetNorms[targetIndex]
      ) {
        best = Math.max(best, 1 + dp[sourceIndex + 1][targetIndex + 1]);
      }
      dp[sourceIndex][targetIndex] = best;
    }
  }
  return dp;
}

function reconstructLcsAlignment({ sourceWords, sourceNorms, targetWordPositions, targetNorms, dp }) {
  const matched = new Map();
  let sourceIndex = 0;
  let targetIndex = 0;
  while (sourceIndex < sourceNorms.length && targetIndex < targetNorms.length) {
    const sourceNorm = sourceNorms[sourceIndex];
    const targetNorm = targetNorms[targetIndex];
    if (
      sourceNorm &&
      sourceNorm === targetNorm &&
      dp[sourceIndex][targetIndex] === 1 + dp[sourceIndex + 1][targetIndex + 1]
    ) {
      const wordId = typeof sourceWords[sourceIndex]?.id === "string" ? sourceWords[sourceIndex].id : "";
      if (wordId) {
        matched.set(targetWordPositions[targetIndex], [wordId]);
      }
      sourceIndex += 1;
      targetIndex += 1;
      continue;
    }
    if (dp[sourceIndex + 1][targetIndex] >= dp[sourceIndex][targetIndex + 1]) {
      sourceIndex += 1;
    } else {
      targetIndex += 1;
    }
  }
  return matched;
}

function safeToString(value) {
  if (typeof value === "string") {
    return value;
  }
  return "";
}

// CLI entry point. Kept at the very bottom so every module-level `const` (e.g.
// ULID_PATTERN) is initialized before main() runs. Invoking main() near the top
// of the module reaches those consts through describeMeeting() during module
// evaluation, before their initializers execute — a temporal-dead-zone crash
// that only surfaces on a real publish, never when tests import helpers (D-462).
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main();
}
