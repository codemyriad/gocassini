import { createHash } from "node:crypto";
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, readdirSync, statSync } from "node:fs";
import { basename, extname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { buildDisplayTranscriptFromArtifacts, groupTranscriptSegmentsAsReadable } from "./export-static-meetings.mjs";
import { buildOpusTags, encodeManifest, normalizeManifest, rewritePortableTags } from "./repack-portable-display-transcripts.mjs";

const DEFAULT_CHUNK_SIZE = 4096;

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}

export async function main(argv = process.argv.slice(2)) {
  const { outputDir, sourceDir } = parseArgs(argv);
  const artifactDirs = listArtifactDirectories(sourceDir);
  if (artifactDirs.length === 0) {
    throw new Error(`No artifact directories found in ${sourceDir}`);
  }

  mkdirSync(outputDir, { recursive: true });
  for (const artifactDir of artifactDirs) {
    const outputPath = join(outputDir, `${basename(artifactDir)}.opus`);
    const result = await packArtifactDirectory(artifactDir, outputPath);
    console.log(`${result.status} ${outputPath}`);
  }
}

export function parseArgs(argv) {
  let outputDir = "";
  let sourceDir = "";
  for (let index = 0; index < argv.length; index += 1) {
    if (argv[index] === "--source-dir") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --source-dir");
      }
      sourceDir = resolve(next);
      index += 1;
      continue;
    }
    if (argv[index] === "--output-dir") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --output-dir");
      }
      outputDir = resolve(next);
      index += 1;
      continue;
    }
    throw new Error(`unknown argument: ${argv[index]}`);
  }

  if (!sourceDir) {
    throw new Error("--source-dir is required");
  }
  if (!outputDir) {
    throw new Error("--output-dir is required");
  }
  return { outputDir, sourceDir };
}

export function listArtifactDirectories(sourceDir) {
  if (!existsSync(sourceDir)) {
    throw new Error(`Missing source directory: ${sourceDir}`);
  }
  if (!statSync(sourceDir).isDirectory()) {
    throw new Error(`Source path must be a directory: ${sourceDir}`);
  }
  return readdirSync(sourceDir, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && !entry.name.startsWith("."))
    .map((entry) => join(sourceDir, entry.name))
    .filter((entry) => existsSync(join(entry, "transcript.words.v1.json")) && existsSync(join(entry, "meeting.opus")))
    .sort();
}

export async function packArtifactDirectory(artifactDir, outputPath) {
  const artifact = loadArtifactMeeting(artifactDir);
  const audioIdentity = await computeAudioIdentity(artifact.audioPath);
  const manifest = buildPortableManifestFromArtifact(artifact, outputPath, audioIdentity);
  const payload = encodeManifest(manifest, DEFAULT_CHUNK_SIZE);
  const tags = buildOpusTags(manifest, payload);
  rewritePortableTags(artifact.audioPath, outputPath, tags);
  return { status: "write" };
}

export function buildPortableManifestFromArtifact(artifact, outputPath, audioIdentity) {
  const title = basename(outputPath, extname(outputPath)) || basename(artifact.rootDir);
  const createdAtUtc = safeToString(artifact.manifest?.generatedAt) || new Date().toISOString();
  const transcript = artifact.transcript;
  const readableTranscript = artifact.readableTranscript ?? {
    version: "transcript.readable.v1",
    media: { ...(transcript.media ?? {}) },
    speakers: Array.isArray(transcript?.speakers) ? transcript.speakers : [],
    sourceTranscriptVersion: transcript.version ?? "transcript.words.v1",
    segments: groupTranscriptSegmentsAsReadable(Array.isArray(transcript?.segments) ? transcript.segments : []),
  };
  const displayTranscript = artifact.displayTranscript ?? buildDisplayTranscriptFromArtifacts(transcript, readableTranscript);
  const items = flattenPortableTranscriptItems(transcript);

  return normalizeManifest({
    meeting: {
      id: audioIdentity.pcmSha256 ? `mtg_${audioIdentity.pcmSha256}` : "",
      title,
      createdAtUtc,
      durationMs: audioIdentity.durationMs,
    },
    audio: {
      container: "ogg",
      codec: "opus",
      sampleRate: audioIdentity.sampleRate,
      channels: audioIdentity.channels,
      sampleCount: audioIdentity.sampleCount,
      durationMs: audioIdentity.durationMs,
    },
    integrity: {
      matchPolicy: "exact-pcm",
      pcmFormat: "s16le",
      pcmSha256: audioIdentity.pcmSha256,
      sampleRate: audioIdentity.sampleRate,
      channels: audioIdentity.channels,
      sampleCount: audioIdentity.sampleCount,
      durationMs: audioIdentity.durationMs,
    },
    speakers: Array.isArray(transcript?.speakers) ? transcript.speakers : [],
    transcript: {
      format: "cassini.words.v1",
      language: safeToString(transcript?.language),
      wordCount: items.length,
      items,
    },
    provenance: artifact.manifest?.provenance,
    readableTranscript,
    displayTranscript,
  });
}

export function flattenPortableTranscriptItems(transcript) {
  const segments = Array.isArray(transcript?.segments) ? transcript.segments : [];
  const items = [];
  for (const segment of segments) {
    const words = Array.isArray(segment?.words) ? segment.words : [];
    if (words.length === 0) {
      const text = safeToString(segment?.text).trim();
      if (!text) {
        continue;
      }
      items.push({
        speaker: safeToString(segment?.speaker),
        startMs: safeToInt(segment?.startMs, 0),
        endMs: safeToInt(segment?.endMs, safeToInt(segment?.startMs, 0)),
        text,
      });
      continue;
    }
    for (const word of words) {
      const text = safeToString(word?.text).trim();
      if (!text) {
        continue;
      }
      items.push({
        speaker: safeToString(segment?.speaker),
        startMs: safeToInt(word?.startMs, 0),
        endMs: safeToInt(word?.endMs, safeToInt(word?.startMs, 0)),
        text,
      });
    }
  }
  return items;
}

function loadArtifactMeeting(rootDir) {
  const manifestPath = join(rootDir, "manifest.json");
  const transcriptPath = join(rootDir, "transcript.words.v1.json");
  const readablePath = join(rootDir, "transcript.readable.v1.json");
  const displayPath = join(rootDir, "transcript.display.v1.json");
  return {
    rootDir,
    audioPath: join(rootDir, "meeting.opus"),
    manifest: existsSync(manifestPath) ? JSON.parse(readFileSync(manifestPath, "utf8")) : {},
    transcript: JSON.parse(readFileSync(transcriptPath, "utf8")),
    readableTranscript: existsSync(readablePath) ? JSON.parse(readFileSync(readablePath, "utf8")) : null,
    displayTranscript: existsSync(displayPath) ? JSON.parse(readFileSync(displayPath, "utf8")) : null,
  };
}

async function computeAudioIdentity(audioPath) {
  const stream = await probeFirstAudioStream(audioPath);
  const sampleRate = safeToInt(stream?.sample_rate, 0);
  const channels = safeToInt(stream?.channels, 0);
  const pcmSha256 = await hashDecodedPcm(audioPath);
  const durationSeconds = Number.parseFloat(String(stream?.duration ?? "0"));
  const durationMs = Number.isFinite(durationSeconds) ? Math.round(durationSeconds * 1000) : 0;
  const sampleCount = sampleRate > 0 ? Math.round((durationMs * sampleRate) / 1000) : 0;
  return { pcmSha256, sampleRate, channels, sampleCount, durationMs };
}

function probeFirstAudioStream(audioPath) {
  return new Promise((resolvePromise, rejectPromise) => {
    const child = spawn("ffprobe", [
      "-v",
      "error",
      "-show_entries",
      "stream=sample_rate,channels,duration",
      "-of",
      "json",
      audioPath,
    ]);
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => {
      stdout += chunk.toString("utf8");
    });
    child.stderr.on("data", (chunk) => {
      stderr += chunk.toString("utf8");
    });
    child.on("error", rejectPromise);
    child.on("close", (code) => {
      if (code !== 0) {
        rejectPromise(new Error(`ffprobe failed for ${audioPath}: ${stderr.trim()}`));
        return;
      }
      resolvePromise(JSON.parse(stdout)?.streams?.[0] ?? {});
    });
  });
}

function hashDecodedPcm(audioPath) {
  return new Promise((resolvePromise, rejectPromise) => {
    const hash = createHash("sha256");
    const child = spawn("ffmpeg", [
      "-v",
      "error",
      "-i",
      audioPath,
      "-f",
      "s16le",
      "-acodec",
      "pcm_s16le",
      "-",
    ]);
    let stderr = "";
    child.stdout.on("data", (chunk) => {
      hash.update(chunk);
    });
    child.stderr.on("data", (chunk) => {
      stderr += chunk.toString("utf8");
    });
    child.on("error", rejectPromise);
    child.on("close", (code) => {
      if (code !== 0) {
        rejectPromise(new Error(`ffmpeg failed for ${audioPath}: ${stderr.trim()}`));
        return;
      }
      resolvePromise(hash.digest("hex"));
    });
  });
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

function safeToString(value) {
  return typeof value === "string" ? value : "";
}
