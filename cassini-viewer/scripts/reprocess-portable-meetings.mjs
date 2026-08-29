import { createHash } from "node:crypto";
import { spawn } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  renameSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { basename, dirname, extname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import {
  buildDisplayTranscriptFromArtifacts,
  extractPortableManifest,
  groupTranscriptSegmentsAsReadable,
} from "./export-static-meetings.mjs";
import {
  buildOpusTags,
  encodeManifest,
  normalizeManifest,
  normalizePortableReadableTranscript,
  repackPortableMeeting,
  requireLegacyV1ForJSRewrite,
  rewritePortableTags,
} from "./repack-portable-display-transcripts.mjs";

const DEFAULT_REPORT_NAME = "portable-reprocess-report.json";
const DEFAULT_CHUNK_SIZE = 4096;

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}

export async function main(argv = process.argv.slice(2)) {
  const { artifactRoots, portableDir, reportPath } = parseArgs(argv);
  const portableFiles = listPortableMeetingFiles(portableDir);
  if (portableFiles.length === 0) {
    throw new Error(`No portable .opus files found in ${portableDir}`);
  }

  const artifactIndex = indexArtifactDirectories(artifactRoots);
  const results = [];
  for (const portablePath of portableFiles) {
    const result = await reprocessPortableMeeting(portablePath, artifactIndex);
    results.push(result);
    console.log(`${result.status} ${basename(portablePath)} (${result.timingPrecision})`);
  }

  if (reportPath) {
    mkdirSync(dirname(reportPath), { recursive: true });
    writeFileSync(
      reportPath,
      `${JSON.stringify({ generatedAt: new Date().toISOString(), portableDir, artifactRoots, results }, null, 2)}\n`,
      "utf8",
    );
    console.log(`report ${reportPath}`);
  }
}

export function parseArgs(argv) {
  let portableDir = "";
  const artifactRoots = [];
  let reportPath = "";
  for (let index = 0; index < argv.length; index += 1) {
    const value = argv[index];
    if (value === "--portable-dir") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --portable-dir");
      }
      portableDir = resolve(next);
      index += 1;
      continue;
    }
    if (value === "--artifact-root") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --artifact-root");
      }
      artifactRoots.push(resolve(next));
      index += 1;
      continue;
    }
    if (value === "--report") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --report");
      }
      reportPath = resolve(next);
      index += 1;
      continue;
    }
    throw new Error(`unknown argument: ${value}`);
  }

  if (!portableDir) {
    throw new Error("--portable-dir is required");
  }
  if (!reportPath) {
    reportPath = join(portableDir, DEFAULT_REPORT_NAME);
  }
  return { artifactRoots, portableDir, reportPath };
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

export function indexArtifactDirectories(roots) {
  const index = new Map();
  for (const root of roots) {
    if (!existsSync(root) || !statSync(root).isDirectory()) {
      continue;
    }
    const entries = readdirSync(root, { withFileTypes: true })
      .filter((entry) => entry.isDirectory() && !entry.name.startsWith("."))
      .sort((left, right) => left.name.localeCompare(right.name));
    for (const entry of entries) {
      const meetingKey = canonicalMeetingKey(entry.name);
      const list = index.get(meetingKey) ?? [];
      list.push(resolve(root, entry.name));
      index.set(meetingKey, list);
    }
  }
  return index;
}

export async function reprocessPortableMeeting(portablePath, artifactIndex) {
  const extracted = extractPortableManifest(portablePath);
  requireLegacyV1ForJSRewrite(extracted, portablePath);
  const portable = normalizePortableReadableTranscript(extracted);
  const portableAudio = portableAudioIdentityFromManifest(portable);
  const meetingKey = canonicalMeetingKey(basename(portablePath, ".opus"));
  const candidatePaths = artifactIndex.get(meetingKey) ?? [];
  const candidates = [];

  for (const candidatePath of candidatePaths) {
    const candidate = await inspectArtifactCandidate(candidatePath, portableAudio, portable);
    if (candidate) {
      candidates.push(candidate);
    }
  }

  const exactCandidate = choosePreferredArtifactCandidate(candidates, portable);
  if (exactCandidate) {
    const result = rewritePortableFromArtifact(portablePath, portable, exactCandidate);
    return {
      ...result,
      meetingId: basename(portablePath, ".opus"),
      source: {
        kind: "artifact",
        path: exactCandidate.path,
      },
      timingPrecision: "exact-word",
      matchedArtifactCount: candidates.filter((candidate) => candidate.audioMatch).length,
      candidateCount: candidatePaths.length,
      candidates: summarizeCandidates(candidates, portableAudio),
    };
  }

  const fallback = repackPortableMeeting(portablePath);
  const updatedPortable = normalizePortableReadableTranscript(extractPortableManifest(portablePath));
  return {
    meetingId: basename(portablePath, ".opus"),
    status: fallback.status === "rewrite" ? "rewrite-fallback" : "skip-fallback",
    source: { kind: "portable" },
    timingPrecision: classifyPortableTimingPrecision(updatedPortable),
    matchedArtifactCount: 0,
    candidateCount: candidatePaths.length,
    candidates: summarizeCandidates(candidates, portableAudio),
  };
}

export function choosePreferredArtifactCandidate(candidates, portable) {
  const exactMatches = candidates.filter((candidate) => candidate.audioMatch);
  if (exactMatches.length === 0) {
    return null;
  }
  const scored = exactMatches.map((candidate) => ({
    candidate,
    score: scoreArtifactCandidate(candidate, portable),
  }));
  scored.sort((left, right) => right.score - left.score || left.candidate.path.localeCompare(right.candidate.path));
  return scored[0]?.candidate ?? null;
}

export function scoreArtifactCandidate(candidate, portable) {
  let score = 0;
  const portableSignature = normalizeSttSignature(portable?.provenance?.speechToText);
  const candidateSignature = normalizeSttSignature(candidate.artifact?.manifest?.provenance?.speechToText, candidate.path);

  if (portableSignature.family && portableSignature.family === candidateSignature.family) {
    score += 100;
  }
  if (portableSignature.model && portableSignature.model === candidateSignature.model) {
    score += 100;
  }
  if (portableSignature.engine && portableSignature.engine === candidateSignature.engine) {
    score += 30;
  }
  if (candidate.artifact?.displayTranscript) {
    score += 10;
  }
  if (candidate.artifact?.readableTranscript) {
    score += 10;
  }
  return score;
}

export function classifyPortableTimingPrecision(portable) {
  const transcriptItems = Array.isArray(portable?.transcript?.items) ? portable.transcript.items : [];
  if (transcriptItems.length > 0 && transcriptItems.every((item) => isSingleWordText(item?.text))) {
    return "exact-word";
  }

  const readableSegments = Array.isArray(portable?.readableTranscript?.segments)
    ? portable.readableTranscript.segments
    : [];
  const segmentsWithWords = readableSegments.filter((segment) => Array.isArray(segment?.words) && segment.words.length > 0);
  if (segmentsWithWords.length === 0) {
    return transcriptItems.length > 0 ? "segment-only" : "missing";
  }

  const syntheticSegments = segmentsWithWords.filter((segment) =>
    wordsLookUniformlyInterpolated(segment.words, safeToInt(segment?.startMs, 0), safeToInt(segment?.endMs, 0)),
  );
  if (syntheticSegments.length === segmentsWithWords.length) {
    return "interpolated-word";
  }
  if (syntheticSegments.length === 0) {
    return "exact-word-readable";
  }
  return "mixed-word";
}

export function canonicalMeetingKey(value) {
  return String(value ?? "")
    .replace(/\.opus$/i, "")
    .replace(/--stt-[A-Za-z0-9._-]+$/, "");
}

function portableAudioIdentityFromManifest(portable) {
  return {
    pcmSha256: safeToString(portable?.integrity?.pcmSha256).toLowerCase(),
    sampleRate: safeToInt(portable?.audio?.sampleRate, safeToInt(portable?.integrity?.sampleRate, 0)),
    channels: safeToInt(portable?.audio?.channels, safeToInt(portable?.integrity?.channels, 0)),
    sampleCount: safeToInt(portable?.audio?.sampleCount, safeToInt(portable?.integrity?.sampleCount, 0)),
    durationMs: safeToInt(portable?.meeting?.durationMs, safeToInt(portable?.audio?.durationMs, 0)),
  };
}

async function inspectArtifactCandidate(candidatePath, portableAudio, portable) {
  const artifact = loadArtifactMeeting(candidatePath);
  if (!artifact) {
    return null;
  }

  const audioIdentity = await computeAudioIdentity(artifact.audioPath);
  const audioMatch = audioIdentity.pcmSha256 !== "" && audioIdentity.pcmSha256 === portableAudio.pcmSha256;
  return {
    path: candidatePath,
    artifact,
    audioIdentity,
    audioMatch,
    portableCreatedAtUtc: safeToString(portable?.meeting?.createdAtUtc),
  };
}

function summarizeCandidates(candidates, portableAudio) {
  return candidates.map((candidate) => ({
    path: candidate.path,
    audioMatch: candidate.audioMatch,
    stt: normalizeSttSignature(candidate.artifact?.manifest?.provenance?.speechToText, candidate.path),
    audioIdentity: {
      durationMs: candidate.audioIdentity.durationMs,
      sampleRate: candidate.audioIdentity.sampleRate,
      channels: candidate.audioIdentity.channels,
      sampleCount: candidate.audioIdentity.sampleCount,
      pcmSha256Prefix: candidate.audioIdentity.pcmSha256.slice(0, 12),
    },
    portableAudio: {
      durationMs: portableAudio.durationMs,
      sampleRate: portableAudio.sampleRate,
      channels: portableAudio.channels,
      sampleCount: portableAudio.sampleCount,
      pcmSha256Prefix: portableAudio.pcmSha256.slice(0, 12),
    },
  }));
}

function loadArtifactMeeting(candidatePath) {
  const transcriptPath = join(candidatePath, "transcript.words.v1.json");
  const audioPath = join(candidatePath, "meeting.opus");
  if (!existsSync(transcriptPath) || !existsSync(audioPath)) {
    return null;
  }

  const manifestPath = join(candidatePath, "manifest.json");
  const readablePath = join(candidatePath, "transcript.readable.v1.json");
  const displayPath = join(candidatePath, "transcript.display.v1.json");
  const manifest = existsSync(manifestPath) ? JSON.parse(readFileSync(manifestPath, "utf8")) : {};
  const transcript = JSON.parse(readFileSync(transcriptPath, "utf8"));
  const readableTranscript = existsSync(readablePath) ? JSON.parse(readFileSync(readablePath, "utf8")) : null;
  const displayTranscript = existsSync(displayPath) ? JSON.parse(readFileSync(displayPath, "utf8")) : null;

  return {
    audioPath,
    manifest,
    transcript,
    readableTranscript,
    displayTranscript,
  };
}

async function computeAudioIdentity(audioPath) {
  const stream = await probeFirstAudioStream(audioPath);
  const sampleRate = safeToInt(stream?.sample_rate, 0);
  const channels = safeToInt(stream?.channels, 0);
  const pcmSha256 = await hashDecodedPcm(audioPath);
  const sampleCount = sampleRate > 0 && channels > 0 ? Math.floor((stream?.duration_ts ?? 0) / Math.max(channels, 1)) : 0;
  const durationMs = sampleRate > 0
    ? Math.floor((sampleCount * 1000) / sampleRate)
    : Math.round(Number.parseFloat(String(stream?.duration ?? "0")) * 1000) || 0;

  return {
    pcmSha256,
    sampleRate,
    channels,
    sampleCount,
    durationMs,
  };
}

function probeFirstAudioStream(audioPath) {
  return new Promise((resolvePromise, rejectPromise) => {
    const child = spawn("ffprobe", [
      "-v",
      "error",
      "-show_entries",
      "stream=sample_rate,channels,duration,duration_ts",
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
      const parsed = JSON.parse(stdout);
      resolvePromise(parsed?.streams?.[0] ?? {});
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

function rewritePortableFromArtifact(portablePath, portable, candidate) {
  const transcript = candidate.artifact.transcript;
  const readable = candidate.artifact.readableTranscript ?? {
    version: "transcript.readable.v1",
    media: { ...(transcript.media ?? {}) },
    speakers: Array.isArray(transcript?.speakers) ? transcript.speakers : portable.speakers,
    sourceTranscriptVersion: transcript.version ?? "transcript.words.v1",
    segments: groupTranscriptSegmentsAsReadable(Array.isArray(transcript?.segments) ? transcript.segments : []),
  };
  const display = candidate.artifact.displayTranscript ?? buildDisplayTranscriptFromArtifacts(transcript, readable);
  const updatedManifest = normalizeManifest({
    ...portable,
    speakers: Array.isArray(transcript?.speakers) ? transcript.speakers : portable.speakers,
    transcript: {
      format: "cassini.words.v1",
      language: safeToString(transcript?.language) || safeToString(portable?.transcript?.language),
      wordCount: flattenPortableTranscriptItems(transcript).length,
      items: flattenPortableTranscriptItems(transcript),
    },
    readableTranscript: readable,
    displayTranscript: display,
    provenance: candidate.artifact.manifest?.provenance ?? portable.provenance,
  });
  const payload = encodeManifest(updatedManifest, DEFAULT_CHUNK_SIZE);
  const tags = buildOpusTags(updatedManifest, payload);

  const stagePath = join(dirname(portablePath), `.${basename(portablePath, ".opus")}.reprocess.tmp.opus`);
  rmSync(stagePath, { force: true });
  try {
    rewritePortableTags(portablePath, stagePath, tags);
    renameSync(stagePath, portablePath);
  } finally {
    rmSync(stagePath, { force: true });
  }

  return { status: "rewrite-exact" };
}

function flattenPortableTranscriptItems(transcript) {
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

function normalizeSttSignature(step, fallbackPath = "") {
  const backend = safeToString(step?.backend).toLowerCase();
  const engine = safeToString(step?.engine).toLowerCase();
  const model = safeToString(step?.model).toLowerCase();
  const path = fallbackPath.toLowerCase();
  const family = [backend, engine, model, path].find((value) => value.includes("whisper"))
    ? "whisper"
    : [backend, engine, model, path].find((value) => value.includes("parakeet"))
      ? "parakeet"
      : "";
  return { backend, engine, model, family };
}

function wordsLookUniformlyInterpolated(words, startMs, endMs) {
  if (!Array.isArray(words) || words.length === 0) {
    return false;
  }
  const span = Math.max(0, endMs - startMs);
  const toleranceMs = Math.max(25, Math.floor(span / Math.max(words.length, 1) / 5));
  return words.every((word, index) => {
    const expectedStart = words.length <= 1
      ? startMs
      : startMs + Math.floor((span * index) / words.length);
    const expectedEnd = words.length <= 1
      ? endMs
      : startMs + Math.floor((span * (index + 1)) / words.length);
    return (
      Math.abs(safeToInt(word?.startMs, expectedStart) - expectedStart) <= toleranceMs &&
      Math.abs(safeToInt(word?.endMs, expectedEnd) - expectedEnd) <= toleranceMs
    );
  });
}

function isSingleWordText(text) {
  return safeToString(text).trim().split(/\s+/).filter(Boolean).length <= 1;
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
