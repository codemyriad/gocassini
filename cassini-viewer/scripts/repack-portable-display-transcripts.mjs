import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { existsSync, readdirSync, renameSync, rmSync, statSync } from "node:fs";
import { basename, dirname, extname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";

import {
  buildDisplayTranscriptFromArtifacts,
  buildReadableTranscriptFromPortable,
  buildTranscriptWordsFromPortable,
  extractPortableManifest,
} from "./export-static-meetings.mjs";

const FORMAT = "org.cassini.portable-meeting/1";
const PROFILE = "ogg-opus";
const PAYLOAD_MIME = "application/vnd.cassini.portable-meeting+json";
const PAYLOAD_ENCODING = "base64url+gzip+utf8json";
const PAYLOAD_SCHEMA = "https://cassini.local/spec/cassini-portable-meeting-manifest-v1.schema.json";
const AUDIO_PCM_FORMAT = "s16le";
const AUDIO_MATCH_POLICY = "exact-pcm";
const DEFAULT_PAYLOAD_CHUNK_SIZE = 4096;
const DESCRIPTION =
  "Cassini portable meeting file. Decode CASSINI_PAYLOAD_*: base64url -> gzip -> UTF-8 JSON.";
const DECODE_HINT = "Concatenate CASSINI_PAYLOAD_000..N, base64url decode, gzip decompress, parse UTF-8 JSON.";

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main();
}

export function main(argv = process.argv.slice(2)) {
  const { sourceDir } = parseArgs(argv);
  const files = listPortableMeetingFiles(sourceDir);
  if (files.length === 0) {
    throw new Error(`No portable .opus files found in ${sourceDir}`);
  }

  for (const file of files) {
    const result = repackPortableMeeting(file);
    console.log(`${result.status} ${file}`);
  }
}

export function parseArgs(argv) {
  let sourceDir = "";
  for (let index = 0; index < argv.length; index += 1) {
    if (argv[index] === "--source-dir") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --source-dir");
      }
      sourceDir = resolve(next);
      index += 1;
    }
  }
  if (!sourceDir) {
    throw new Error("--source-dir is required");
  }
  return { sourceDir };
}

export function listPortableMeetingFiles(sourceDir) {
  if (!existsSync(sourceDir)) {
    throw new Error(`Missing source directory: ${sourceDir}`);
  }
  const stats = statSync(sourceDir);
  if (!stats.isDirectory()) {
    throw new Error(`Source path must be a directory: ${sourceDir}`);
  }

  return readdirSync(sourceDir)
    .filter((entry) => extname(entry).toLowerCase() === ".opus")
    .map((entry) => join(sourceDir, entry))
    .sort();
}

export function repackPortableMeeting(path) {
  const portable = extractPortableManifest(path);
  const normalizedPortable = normalizePortableReadableTranscript(portable);
  const transcript = buildTranscriptWordsFromPortable(normalizedPortable);
  const readable = buildReadableTranscriptFromPortable(normalizedPortable, transcript);
  const display = buildDisplayTranscriptFromArtifacts(transcript, readable);

  if (
    normalizedPortable.displayTranscript &&
    JSON.stringify(normalizedPortable.displayTranscript) === JSON.stringify(display) &&
    normalizedPortable.readableTranscript?.version === portable.readableTranscript?.version
  ) {
    return { status: "skip" };
  }

  const updatedManifest = normalizeManifest({
    ...normalizedPortable,
    displayTranscript: display,
  });
  const payload = encodeManifest(updatedManifest, DEFAULT_PAYLOAD_CHUNK_SIZE);
  const tags = buildOpusTags(updatedManifest, payload);

  const stagePath = join(dirname(path), `.${basename(path, ".opus")}.display-repack.tmp.opus`);
  rmSync(stagePath, { force: true });
  try {
    rewritePortableTags(path, stagePath, tags);
    const rewritten = extractPortableManifest(stagePath);
    if (!rewritten.displayTranscript) {
      throw new Error(`displayTranscript missing after rewrite for ${path}`);
    }
    renameSync(stagePath, path);
  } finally {
    rmSync(stagePath, { force: true });
  }
  return { status: "rewrite" };
}

export function normalizePortableReadableTranscript(portable) {
  if (
    portable?.readableTranscript &&
    typeof portable.readableTranscript === "object" &&
    portable.readableTranscript.version === "transcript.words.v1" &&
    Array.isArray(portable.readableTranscript.segments)
  ) {
    // Old portable files already contain cleaned readable text here; the bug is
    // the version tag, not the payload. Normalize it so display alignment uses
    // the cleaned transcript instead of silently falling back to raw ASR text.
    return {
      ...portable,
      readableTranscript: {
        ...portable.readableTranscript,
        version: "transcript.readable.v1",
        sourceTranscriptVersion: portable.readableTranscript.sourceTranscriptVersion ?? "transcript.words.v1",
      },
    };
  }
  return portable;
}

export function rewritePortableTags(inputPath, outputPath, tags) {
  const args = [
    "-v",
    "error",
    "-y",
    "-i",
    inputPath,
    "-map",
    "0:a",
    "-c",
    "copy",
    "-map_metadata",
    "-1",
  ];
  for (const [key, value] of Object.entries(tags).sort(([left], [right]) => left.localeCompare(right))) {
    args.push("-metadata", `${key}=${value}`);
  }
  args.push(outputPath);
  execFileSync("ffmpeg", args, { stdio: "inherit" });
}

export function normalizeManifest(manifest) {
  const next = structuredClone(manifest);
  next.kind = "cassini-portable-meeting";
  next.version = 1;
  next.profile = PROFILE;
  next.audio = next.audio || {};
  next.audio.container = "ogg";
  next.audio.codec = "opus";
  next.integrity = next.integrity || {};
  next.integrity.matchPolicy = AUDIO_MATCH_POLICY;
  next.integrity.pcmFormat = AUDIO_PCM_FORMAT;
  next.transcript = next.transcript || {};
  if (!next.transcript.format) {
    next.transcript.format = "cassini.words.v1";
  }
  return next;
}

export function encodeManifest(manifest, chunkSize) {
  const normalized = normalizeManifest(manifest);
  const json = Buffer.from(JSON.stringify(normalized), "utf8");
  const gzip = gzipSync(json);
  const base64url = gzip.toString("base64url");
  const size = chunkSize > 0 ? chunkSize : DEFAULT_PAYLOAD_CHUNK_SIZE;
  const chunks = chunkString(base64url, size);
  return {
    json,
    gzip,
    base64url,
    sha256: createHash("sha256").update(json).digest("hex"),
    rawBytes: json.length,
    compressedBytes: gzip.length,
    chunks,
  };
}

export function buildOpusTags(manifest, payload) {
  const normalized = normalizeManifest(manifest);
  const tags = {
    TITLE: normalized.meeting?.title ?? "",
    DATE: normalized.meeting?.createdAtUtc ?? "",
    DESCRIPTION,
    ENCODER: "Cassini",
    CASSINI_FORMAT: FORMAT,
    CASSINI_PROFILE: PROFILE,
    CASSINI_PAYLOAD_MIME: PAYLOAD_MIME,
    CASSINI_PAYLOAD_ENCODING: PAYLOAD_ENCODING,
    CASSINI_PAYLOAD_SCHEMA: PAYLOAD_SCHEMA,
    CASSINI_PAYLOAD_CHUNK_COUNT: String(payload.chunks.length),
    CASSINI_PAYLOAD_SHA256: payload.sha256,
    CASSINI_PAYLOAD_RAW_BYTES: String(payload.rawBytes),
    CASSINI_PAYLOAD_GZIP_BYTES: String(payload.compressedBytes),
    CASSINI_AUDIO_PCM_FORMAT: AUDIO_PCM_FORMAT,
    CASSINI_AUDIO_SAMPLE_RATE: String(normalized.audio?.sampleRate ?? ""),
    CASSINI_AUDIO_CHANNELS: String(normalized.audio?.channels ?? ""),
    CASSINI_AUDIO_SAMPLE_COUNT: String(normalized.audio?.sampleCount ?? ""),
    CASSINI_AUDIO_DURATION_MS: String(normalized.audio?.durationMs ?? ""),
    CASSINI_AUDIO_PCM_SHA256: normalized.integrity?.pcmSha256 ?? "",
    CASSINI_AUDIO_MATCH_POLICY: AUDIO_MATCH_POLICY,
    CASSINI_DECODE_HINT: DECODE_HINT,
    CASSINI_MEETING_ID: normalized.meeting?.id ?? "",
    CASSINI_CREATED_AT: normalized.meeting?.createdAtUtc ?? "",
    CASSINI_SPEAKER_COUNT: String(Array.isArray(normalized.speakers) ? normalized.speakers.length : 0),
    CASSINI_WORD_COUNT: String(normalized.transcript?.wordCount ?? 0),
  };

  // The room, mirroring Go's applyRoomTags (internal/portable/manifest.go).
  // rewritePortableTags runs ffmpeg with -map_metadata -1, so a tag this
  // function does not re-emit is DELETED from every repacked file — the room
  // would survive in the gzipped payload and vanish from the plain tags, which
  // are the ones a shell reader (the catalog backfill) can actually get at.
  // Absent rather than empty when unknown, as on the Go side.
  const roomId = typeof normalized.meeting?.roomId === "string" ? normalized.meeting.roomId.trim() : "";
  const roomName = typeof normalized.meeting?.roomName === "string" ? normalized.meeting.roomName.trim() : "";
  if (roomId) {
    tags.CASSINI_ROOM_ID = roomId;
  }
  if (roomName) {
    tags.CASSINI_ROOM_NAME = roomName;
  }

  // The operator lineage, mirroring Go's applyProvenanceTags. Same deletion
  // hazard as the room above: a tag not re-emitted here is gone from every
  // repacked file. attemptNumber is 1-based, so a non-positive value is
  // "unknown" and is omitted rather than written as an attempt that cannot
  // exist.
  const jobId = typeof normalized.meeting?.jobId === "string" ? normalized.meeting.jobId.trim() : "";
  const attemptNumber = Number(normalized.meeting?.attemptNumber);
  if (jobId) {
    tags.CASSINI_JOB_ID = jobId;
  }
  if (Number.isInteger(attemptNumber) && attemptNumber > 0) {
    tags.CASSINI_ATTEMPT_NUMBER = String(attemptNumber);
  }

  const language = firstNonEmpty(normalized.transcript?.language, normalized.meeting?.language);
  if (language) {
    tags.LANGUAGE = language;
    tags.CASSINI_TRANSCRIPT_LANGUAGE = language;
  }

  applyProcessingStepTags(tags, "CASSINI_STT", normalized.provenance?.speechToText);
  applyProcessingStepTags(tags, "CASSINI_READABLE", normalized.provenance?.readableCleanup);

  payload.chunks.forEach((chunk, index) => {
    tags[`CASSINI_PAYLOAD_${String(index).padStart(3, "0")}`] = chunk;
  });
  return tags;
}

function applyProcessingStepTags(tags, prefix, step) {
  if (!step || typeof step !== "object") {
    return;
  }
  setStepTag(tags, `${prefix}_BACKEND`, step.backend);
  setStepTag(tags, `${prefix}_ENGINE`, step.engine);
  setStepTag(tags, `${prefix}_MODEL`, step.model);
  setStepTag(tags, `${prefix}_DEVICE`, step.device);
  setStepTag(tags, `${prefix}_LANGUAGE`, step.language);
  setStepTag(tags, `${prefix}_BASE_URL`, step.baseUrl);
  setStepTag(tags, `${prefix}_HOST`, step.host);
  setStepTag(tags, `${prefix}_SOURCE`, step.source);
  setStepTag(tags, `${prefix}_VERSION`, step.version);
}

function setStepTag(tags, key, value) {
  const normalized = firstNonEmpty(value);
  if (normalized) {
    tags[key] = normalized;
  }
}

function chunkString(value, size) {
  if (!value) {
    return [];
  }
  const chunks = [];
  for (let index = 0; index < value.length; index += size) {
    chunks.push(value.slice(index, index + size));
  }
  return chunks;
}

function firstNonEmpty(...values) {
  for (const value of values) {
    if (typeof value === "string" && value.trim() !== "") {
      return value;
    }
  }
  return "";
}
