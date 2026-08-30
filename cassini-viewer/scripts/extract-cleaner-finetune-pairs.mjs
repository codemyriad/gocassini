import { existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const viewerDir = resolve(scriptDir, "..");
const defaultArtifactRoot = resolve(viewerDir, "exports", "from-george", "release-20260319T065408", "bundles");
const defaultOutputBasePath = resolve(viewerDir, "exports", "from-george", "release-20260319T065408", "cleaner-finetune-pairs-20260319");

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main();
}

export function main(argv = process.argv.slice(2)) {
  const { artifactRoot, outputBasePath, changedOnly } = parseArgs(argv);
  if (!existsSync(artifactRoot)) {
    throw new Error(`artifact root not found: ${artifactRoot}`);
  }

  const records = buildFinetuneRecords(artifactRoot, { changedOnly });
  if (records.length === 0) {
    throw new Error(`no finetune records extracted from ${artifactRoot}`);
  }

  const simpleRecords = records.map(({ rawText, cleanedText, ...rest }) => ({
    input: rawText,
    output: cleanedText,
    metadata: rest,
  }));
  const summary = buildSummary(records, { artifactRoot, changedOnly });

  mkdirSync(dirname(outputBasePath), { recursive: true });
  writeFileSync(`${outputBasePath}.jsonl`, renderJsonl(simpleRecords), "utf8");
  writeFileSync(`${outputBasePath}.rich.jsonl`, renderJsonl(records), "utf8");
  writeFileSync(`${outputBasePath}.summary.json`, `${JSON.stringify(summary, null, 2)}\n`, "utf8");

  console.log(`cleaner pairs -> ${outputBasePath}.jsonl`);
  console.log(`cleaner pairs (rich) -> ${outputBasePath}.rich.jsonl`);
  console.log(`cleaner summary -> ${outputBasePath}.summary.json`);
  console.log(`records -> ${records.length}`);
  console.log(`changed -> ${summary.changedPassageCount}`);
  console.log(`unchanged -> ${summary.unchangedPassageCount}`);
}

export function parseArgs(argv) {
  let artifactRoot = defaultArtifactRoot;
  let outputBasePath = defaultOutputBasePath;
  let changedOnly = false;

  for (let index = 0; index < argv.length; index += 1) {
    const value = argv[index];
    if (value === "--artifact-root") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --artifact-root");
      }
      artifactRoot = resolve(viewerDir, next);
      index += 1;
      continue;
    }
    if (value === "--output") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --output");
      }
      outputBasePath = resolve(viewerDir, next);
      index += 1;
      continue;
    }
    if (value === "--changed-only") {
      changedOnly = true;
      continue;
    }
    throw new Error(`unknown argument: ${value}`);
  }

  return {
    artifactRoot,
    outputBasePath,
    changedOnly,
  };
}

export function buildFinetuneRecords(artifactRoot, { changedOnly = false } = {}) {
  const artifactIds = readdirSync(artifactRoot, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && !entry.name.startsWith("."))
    .map((entry) => entry.name)
    .sort((left, right) => left.localeCompare(right));

  return artifactIds.flatMap((artifactId) => buildArtifactFinetuneRecords({
    artifactRoot,
    artifactId,
    changedOnly,
  }));
}

function buildArtifactFinetuneRecords({ artifactRoot, artifactId, changedOnly }) {
  const artifactDir = join(artifactRoot, artifactId);
  const manifestPath = join(artifactDir, "manifest.json");
  const transcriptPath = join(artifactDir, "transcript.words.v1.json");
  const readablePath = join(artifactDir, "transcript.readable.v1.json");

  if (!existsSync(manifestPath) || !existsSync(transcriptPath) || !existsSync(readablePath)) {
    return [];
  }

  const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
  const transcript = JSON.parse(readFileSync(transcriptPath, "utf8"));
  const readable = JSON.parse(readFileSync(readablePath, "utf8"));
  const transcriptSegments = Array.isArray(transcript.segments) ? transcript.segments : [];
  const segmentById = new Map(
    transcriptSegments
      .filter((segment) => typeof segment?.id === "string" && segment.id.trim() !== "")
      .map((segment) => [segment.id, segment]),
  );
  const speakerLabels = new Map(
    Array.isArray(transcript.speakers)
      ? transcript.speakers.map((speaker) => [safeString(speaker?.id), safeString(speaker?.label) || safeString(speaker?.id)])
      : [],
  );
  const meetingId = artifactId.replace(/\.meeting$/i, "");
  const readableSegments = Array.isArray(readable.segments) ? readable.segments : [];

  return readableSegments.flatMap((segment, index) => {
    const startMs = safeInt(segment?.startMs, 0);
    const endMs = safeInt(segment?.endMs, startMs);
    const sourceSegmentIds = Array.isArray(segment?.sourceSegmentIds)
      ? segment.sourceSegmentIds.filter((value) => typeof value === "string" && value.trim() !== "")
      : [];
    const resolvedSourceSegments = resolveSourceSegments({
      sourceSegmentIds,
      segmentById,
      transcriptSegments,
      speaker: safeString(segment?.speaker),
      startMs,
      endMs,
    });
    const sourceWords = resolveSourceWords({
      sourceSegments: resolvedSourceSegments,
      startMs,
      endMs,
    });
    const rawText = normalizeText(
      sourceWords.length > 0
        ? sourceWords.map((word) => word.text).join(" ")
        : resolvedSourceSegments
            .map((sourceSegment) => safeString(sourceSegment?.text))
            .filter(Boolean)
            .join(" "),
    );
    const cleanedText = normalizeText(segment?.text);
    if (!rawText || !cleanedText) {
      return [];
    }

    const changed = rawText !== cleanedText;
    if (changedOnly && !changed) {
      return [];
    }

    const contributingSourceSegments = collectSourceSegmentIds(resolvedSourceSegments, sourceWords);

    return [{
      artifactId,
      meetingId,
      segmentId: safeString(segment?.id) || `segment_${String(index).padStart(6, "0")}`,
      speaker: safeString(segment?.speaker),
      speakerLabel: speakerLabels.get(safeString(segment?.speaker)) || "Unknown speaker",
      startMs,
      endMs,
      durationMs: Math.max(0, endMs - startMs),
      sourceSegmentIds: contributingSourceSegments
        .map((sourceSegment) => safeString(sourceSegment?.id))
        .filter(Boolean),
      sourceSegmentCount: contributingSourceSegments.length,
      rawText,
      cleanedText,
      changed,
      sttBackend: safeString(manifest?.provenance?.speechToText?.backend),
      sttEngine: safeString(manifest?.provenance?.speechToText?.engine),
      sttModel: safeString(manifest?.provenance?.speechToText?.model),
      cleanupBackend: safeString(manifest?.provenance?.readableCleanup?.backend),
      cleanupModel: safeString(manifest?.provenance?.readableCleanup?.model),
    }];
  });
}

export function resolveSourceSegments({ sourceSegmentIds, segmentById, transcriptSegments, speaker, startMs, endMs }) {
  const explicitMatches = dedupeSegments(
    sourceSegmentIds
      .map((segmentId) => segmentById.get(segmentId))
      .filter(Boolean),
  );
  if (explicitMatches.length > 0) {
    return explicitMatches;
  }

  const speakerMatches = transcriptSegments.filter((segment) => overlapsTimeRange(segment, startMs, endMs)
    && (!speaker || safeString(segment?.speaker) === speaker));
  if (speakerMatches.length > 0) {
    return dedupeSegments(speakerMatches);
  }

  return dedupeSegments(transcriptSegments.filter((segment) => overlapsTimeRange(segment, startMs, endMs)));
}

function overlapsTimeRange(segment, startMs, endMs) {
  const segmentStart = safeInt(segment?.startMs, 0);
  const segmentEnd = safeInt(segment?.endMs, segmentStart);
  return segmentEnd >= startMs && segmentStart <= endMs;
}

function dedupeSegments(segments) {
  const seen = new Set();
  return segments
    .filter((segment) => segment && typeof segment === "object")
    .filter((segment) => {
      const key = safeString(segment?.id) || `${safeInt(segment?.startMs, 0)}:${safeInt(segment?.endMs, 0)}:${safeString(segment?.text)}`;
      if (seen.has(key)) {
        return false;
      }
      seen.add(key);
      return true;
    })
    .sort((left, right) => safeInt(left?.startMs, 0) - safeInt(right?.startMs, 0) || safeInt(left?.endMs, 0) - safeInt(right?.endMs, 0));
}

function resolveSourceWords({ sourceSegments, startMs, endMs }) {
  const candidateWords = sourceSegments.flatMap((segment) => {
    if (!Array.isArray(segment?.words)) {
      return [];
    }
    return segment.words
      .filter((word) => word && typeof word === "object")
      .map((word, index) => ({
        id: safeString(word?.id) || `${safeString(segment?.id) || "segment"}:w_${index}`,
        segmentId: safeString(segment?.id),
        text: safeString(word?.text),
        startMs: safeInt(word?.startMs, Number.NaN),
        endMs: safeInt(word?.endMs, safeInt(word?.startMs, Number.NaN)),
      }))
      .filter((word) => word.text && Number.isFinite(word.startMs) && Number.isFinite(word.endMs));
  });

  if (candidateWords.length === 0) {
    return [];
  }

  const midpointMatches = candidateWords.filter((word) => {
    const midpoint = word.startMs + ((word.endMs - word.startMs) / 2);
    return midpoint >= startMs && midpoint <= endMs;
  });
  if (midpointMatches.length > 0) {
    return midpointMatches;
  }

  const overlapMatches = candidateWords.filter((word) => word.endMs >= startMs && word.startMs <= endMs);
  return overlapMatches.length > 0 ? overlapMatches : candidateWords;
}

function collectSourceSegmentIds(sourceSegments, sourceWords) {
  const wordSegmentIds = new Set(sourceWords.map((word) => word.segmentId).filter(Boolean));
  if (wordSegmentIds.size === 0) {
    return sourceSegments;
  }
  return sourceSegments.filter((segment) => wordSegmentIds.has(safeString(segment?.id)));
}

export function buildSummary(records, { artifactRoot, changedOnly }) {
  const artifactIds = new Set(records.map((record) => record.artifactId));
  const meetingIds = new Set(records.map((record) => record.meetingId));
  const changedPassageCount = records.filter((record) => record.changed).length;
  const unchangedPassageCount = records.length - changedPassageCount;
  const byMeeting = Array.from(meetingIds)
    .sort((left, right) => left.localeCompare(right))
    .map((meetingId) => {
      const meetingRecords = records.filter((record) => record.meetingId === meetingId);
      const changedCount = meetingRecords.filter((record) => record.changed).length;
      return {
        meetingId,
        passageCount: meetingRecords.length,
        changedPassageCount: changedCount,
        unchangedPassageCount: meetingRecords.length - changedCount,
      };
    });

  return {
    generatedAt: new Date().toISOString(),
    artifactRoot,
    changedOnly,
    artifactCount: artifactIds.size,
    meetingCount: meetingIds.size,
    passageCount: records.length,
    changedPassageCount,
    unchangedPassageCount,
    changedPassageRate: records.length > 0 ? changedPassageCount / records.length : 0,
    totalRawWordCount: records.reduce((sum, record) => sum + countWords(record.rawText), 0),
    totalCleanedWordCount: records.reduce((sum, record) => sum + countWords(record.cleanedText), 0),
    byMeeting,
  };
}

function renderJsonl(records) {
  return `${records.map((record) => JSON.stringify(record)).join("\n")}\n`;
}

function normalizeText(value) {
  return safeString(value)
    .replace(/\s+/g, " ")
    .replace(/\s+([,.;!?])/g, "$1")
    .trim();
}

function countWords(value) {
  return normalizeText(value).split(/\s+/).filter(Boolean).length;
}

function safeString(value) {
  return typeof value === "string" ? value.trim() : "";
}

function safeInt(value, fallback) {
  return Number.isFinite(Number(value)) ? Number(value) : fallback;
}
