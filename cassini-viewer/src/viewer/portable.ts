import type {
  DisplayTranscriptV1,
  ReadableTranscriptV1,
  TranscriptSpeaker,
  TranscriptWordsV1,
} from "../core/types";

export interface PortablePayloadRef {
  prefix: string;
  chunkCount: number;
  sha256: string;
  rawBytes?: number;
  gzipBytes?: number;
  mime?: string;
  encoding?: string;
}

export interface PortableTranscriptEntry {
  id: string;
  role: string;
  default?: boolean;
  format: string;
  language?: string;
  wordCount?: number;
  sourceTranscriptId?: string;
  createdAtUtc?: string;
  payloadRef: PortablePayloadRef;
}

export interface PortableMeetingManifest {
	kind?: string;
  version?: number;
	profile?: string;
  meeting?: {
    durationMs?: number;
    createdAtUtc?: string;
    recordedAtLocal?: string;
    processedAtUtc?: string;
    title?: string;
    id?: string;
  };
  audio?: {
    sha256?: string;
  };
  integrity?: {
    matchPolicy?: string;
    opusAudioSha256?: string;
  };
  speakers?: unknown[];
  transcript?: {
    items?: unknown[];
  };
  readableTranscript?: {
    version?: string;
    speakers?: unknown[];
    segments?: unknown[];
    sourceTranscriptVersion?: string;
  };
  displayTranscript?: {
    version?: string;
    media?: unknown;
    speakers?: unknown[];
    blocks?: unknown[];
    sourceTranscriptVersion?: string;
    sourceReadableTranscriptVersion?: string;
  };
  provenance?: unknown;
  transcripts?: PortableTranscriptEntry[];
  readableTranscripts?: PortableTranscriptEntry[];
}

export interface PortableTranscriptDescriptor {
  id: string;
  role: string;
  // Short, always-unique button label. Derived from the transcript id, which the
  // producer chose for human consumption — guaranteed distinguishable even when
  // two transcripts share an engine (e.g. both sherpa-onnx for parakeet+canary).
  label: string;
  // Long-form tooltip text: engine/backend/model from provenance.speechToText,
  // intended for the title attribute. Empty when provenance is absent.
  description: string;
  language?: string;
  isDefault: boolean;
}

export interface ExtractedPortableManifest {
  manifest: PortableMeetingManifest;
  tags: Record<string, string>;
}

export async function extractPortableManifestFromArrayBuffer(
  value: ArrayBuffer | Uint8Array,
): Promise<ExtractedPortableManifest> {
  const bytes = value instanceof Uint8Array ? value : new Uint8Array(value);
  const tags = parseOpusCommentTags(bytes);
  const format = String(tags.CASSINI_FORMAT ?? "").trim();
  if (!format) {
    throw new Error("portable opus file is missing CASSINI_FORMAT");
  }
  if (format !== "org.cassini.portable-meeting/1") {
    throw new Error(`portable opus file uses unsupported CASSINI_FORMAT=${format}`);
  }
  for (const [name, expected] of Object.entries({
    CASSINI_PROFILE: "ogg-opus",
    CASSINI_PAYLOAD_MIME: "application/vnd.cassini.portable-meeting+json",
    CASSINI_PAYLOAD_ENCODING: "base64url+gzip+utf8json",
    CASSINI_PAYLOAD_SCHEMA:
      "https://cassini-format.codemyriad.io/schema/cassini-portable-meeting-manifest-v1.schema.json",
    CASSINI_AUDIO_MATCH_POLICY: "exact-opus-audio-v1",
  })) {
    if (String(tags[name] ?? "").trim() !== expected) {
      throw new Error(`portable opus file uses unsupported ${name}=${String(tags[name] ?? "")}`);
    }
  }
  if (!/^[0-9a-f]{64}$/.test(String(tags.CASSINI_AUDIO_OPUS_SHA256 ?? "").trim())) {
    throw new Error("portable opus file has a missing or invalid CASSINI_AUDIO_OPUS_SHA256");
  }
  const indexManifest = await readMainPortablePayload(tags);
  validatePortableIndexManifest(indexManifest);
  const tagAudioDigest = String(tags.CASSINI_AUDIO_OPUS_SHA256).trim();
  const manifestAudioDigest = String(indexManifest.integrity?.opusAudioSha256 ?? "").trim();
  if (tagAudioDigest !== manifestAudioDigest) {
    throw new Error("portable opus audio digest disagrees between tags and manifest");
  }
  const manifest = await resolvePortableDefaultBodies(indexManifest, tags);
  return { manifest, tags };
}

function validatePortableIndexManifest(manifest: PortableMeetingManifest): void {
  if (manifest.kind !== "cassini-portable-meeting") {
    throw new Error(`portable manifest has unsupported kind ${String(manifest.kind)}`);
  }
  if (manifest.version !== 1) {
    throw new Error(`portable opus file uses unsupported manifest version ${String(manifest.version)}`);
  }
  if (manifest.profile !== "ogg-opus") {
    throw new Error(`portable manifest has unsupported profile ${String(manifest.profile)}`);
  }
  if (manifest.integrity?.matchPolicy !== "exact-opus-audio-v1") {
    throw new Error(
      `portable manifest has unsupported audio integrity policy ${String(manifest.integrity?.matchPolicy)}`,
    );
  }
  if (!/^[0-9a-f]{64}$/.test(String(manifest.integrity?.opusAudioSha256 ?? ""))) {
    throw new Error("portable manifest has an invalid Opus digest");
  }
  if (!Array.isArray(manifest.transcripts) || manifest.transcripts.length === 0) {
    throw new Error("portable manifest has no transcripts[]");
  }
  const readable = Array.isArray(manifest.readableTranscripts)
    ? manifest.readableTranscripts
    : [];
  const rawIds = new Set<string>();
  const allIds = new Set<string>();
  for (const entry of manifest.transcripts) {
    validatePortableTranscriptEntry(entry, new Set(["raw-asr", "human-corrected", "translation"]));
    if (allIds.has(entry.id)) {
      throw new Error(`portable manifest has duplicate transcript id ${entry.id}`);
    }
    allIds.add(entry.id);
    rawIds.add(entry.id);
  }
  for (const entry of readable) {
    validatePortableTranscriptEntry(entry, new Set(["readable-cleanup", "display"]));
    if (allIds.has(entry.id)) {
      throw new Error(`portable manifest has duplicate transcript id ${entry.id}`);
    }
    if (!rawIds.has(String(entry.sourceTranscriptId ?? ""))) {
      throw new Error(`portable transcript ${entry.id} has an unknown sourceTranscriptId`);
    }
    allIds.add(entry.id);
  }
}

function validatePortableTranscriptEntry(
  entry: PortableTranscriptEntry,
  roles: ReadonlySet<string>,
): void {
  const id = String(entry?.id ?? "");
  if (!/^[a-z0-9][a-z0-9_-]{0,31}$/.test(id)) {
    throw new Error(`portable manifest has an invalid transcript id ${JSON.stringify(id)}`);
  }
  if (!roles.has(entry?.role)) {
    throw new Error(`portable transcript ${id} has unsupported role ${String(entry?.role)}`);
  }
  if (typeof entry?.format !== "string" || entry.format.trim() === "") {
    throw new Error(`portable transcript ${id} has an invalid format`);
  }
  const ref = entry?.payloadRef;
  const expectedPrefix = `CASSINI_TX_${id.toUpperCase().replace(/-/g, "_")}_PAYLOAD_`;
  if (!ref || ref.prefix !== expectedPrefix || !Number.isInteger(ref.chunkCount) || ref.chunkCount < 1) {
    throw new Error(`portable transcript ${id} has an invalid payloadRef`);
  }
  if (ref.encoding !== "base64url+gzip+utf8json") {
    throw new Error(`portable transcript ${id} uses an unsupported payload encoding`);
  }
  if (!/^[0-9a-f]{64}$/.test(String(ref.sha256 ?? ""))) {
    throw new Error(`portable transcript ${id} has an invalid payload digest`);
  }
}

async function readMainPortablePayload(
  tags: Record<string, string>,
): Promise<PortableMeetingManifest> {
  const chunkCount = safeToInt(tags.CASSINI_PAYLOAD_CHUNK_COUNT, 0);
  if (chunkCount <= 0) {
    throw new Error("missing or invalid CASSINI_PAYLOAD_CHUNK_COUNT");
  }

  let encoded = "";
  for (let index = 0; index < chunkCount; index += 1) {
    const key = `CASSINI_PAYLOAD_${String(index).padStart(3, "0")}`;
    const chunk = tags[key];
    if (!chunk) {
      throw new Error(`missing payload chunk ${key}`);
    }
    encoded += chunk;
  }

  const compressed = decodeBase64Url(encoded);
  const declaredGzipBytes = safeToInt(tags.CASSINI_PAYLOAD_GZIP_BYTES, 0);
  if (declaredGzipBytes <= 0 || declaredGzipBytes !== compressed.byteLength) {
    throw new Error(
      `portable manifest gzip byte count mismatch (expected ${declaredGzipBytes}, got ${compressed.byteLength})`,
    );
  }
  const rawManifest = await gunzipBytes(compressed);
  const declaredRawBytes = safeToInt(tags.CASSINI_PAYLOAD_RAW_BYTES, 0);
  if (declaredRawBytes <= 0 || declaredRawBytes !== rawManifest.byteLength) {
    throw new Error(
      `portable manifest raw byte count mismatch (expected ${declaredRawBytes}, got ${rawManifest.byteLength})`,
    );
  }
  const expectedSHA = String(tags.CASSINI_PAYLOAD_SHA256 ?? "").trim().toLowerCase();
  if (!/^[0-9a-f]{64}$/.test(expectedSHA)) {
    throw new Error("portable manifest has a missing or invalid CASSINI_PAYLOAD_SHA256");
  }
  const actualSHA = await sha256Hex(rawManifest);
  if (actualSHA !== expectedSHA) {
    throw new Error(
      `portable manifest sha256 mismatch (expected ${expectedSHA}, got ${actualSHA})`,
    );
  }
  return JSON.parse(new TextDecoder().decode(rawManifest)) as PortableMeetingManifest;
}

async function resolvePortableDefaultBodies(
  indexManifest: PortableMeetingManifest,
  tags: Record<string, string>,
): Promise<PortableMeetingManifest> {
  const transcripts = Array.isArray(indexManifest.transcripts) ? indexManifest.transcripts : [];
  if (transcripts.length === 0) {
    throw new Error("portable manifest has no transcripts[]");
  }
  const defaultTranscript = pickDefaultTranscript(transcripts);
  const transcriptBody = await loadPortableTranscriptBody(tags, defaultTranscript.payloadRef);
  indexManifest.transcript = transcriptBody as PortableMeetingManifest["transcript"];

  const readableTranscripts = Array.isArray(indexManifest.readableTranscripts)
    ? indexManifest.readableTranscripts
    : [];
  const defaultReadable = pickDerivedTranscript(
    readableTranscripts,
    "readable-cleanup",
    defaultTranscript.id,
  );
  if (defaultReadable) {
    const readableBody = await loadPortableTranscriptBody(tags, defaultReadable.payloadRef);
    indexManifest.readableTranscript = readableBody as PortableMeetingManifest["readableTranscript"];
  }
  const defaultDisplay = pickDerivedTranscript(
    readableTranscripts,
    "display",
    defaultTranscript.id,
  );
  if (defaultDisplay) {
    const displayBody = await loadPortableTranscriptBody(tags, defaultDisplay.payloadRef);
    indexManifest.displayTranscript = displayBody as PortableMeetingManifest["displayTranscript"];
  }

  return indexManifest;
}

function pickDefaultTranscript(
  transcripts: PortableTranscriptEntry[],
): PortableTranscriptEntry {
  const flagged = transcripts.find((entry) => entry.default);
  return flagged ?? transcripts[0]!;
}

function pickDerivedTranscript(
  entries: PortableTranscriptEntry[],
  role: string,
  sourceTranscriptId: string,
): PortableTranscriptEntry | null {
  const candidates = entries.filter((entry) => entry.role === role);
  return candidates.find((entry) => entry.sourceTranscriptId === sourceTranscriptId) ?? null;
}

/**
 * Returns the readableTranscripts[] entry whose sourceTranscriptId matches the
 * given raw transcript id. Returns null when no matching readable transcript
 * exists; a body derived from a different ASR result must never be substituted.
 */
export function pickReadableForTranscript(
  manifest: PortableMeetingManifest,
  transcriptId: string,
): PortableTranscriptEntry | null {
  const readables = Array.isArray(manifest.readableTranscripts)
    ? manifest.readableTranscripts
    : [];
  if (readables.length === 0) {
    return null;
  }
  return pickDerivedTranscript(readables, "readable-cleanup", transcriptId);
}

/**
 * Builds a descriptor for a transcript entry suitable for the viewer switcher UI.
 * The label always uses a humanized form of the transcript id — engine/backend/
 * model are demoted to the description field for tooltips, since producers can
 * reuse the same engine for multiple transcripts (e.g. sherpa-onnx running both
 * parakeet and canary models) which would otherwise collide.
 */
export function describeTranscript(
  entry: PortableTranscriptEntry,
  manifest: PortableMeetingManifest,
  isDefault: boolean,
): PortableTranscriptDescriptor {
  const provenance = asRecord((manifest.provenance as unknown) ?? {});
  const speechToText = asRecord(provenance.speechToText ?? {});
  const step = asRecord(speechToText[entry.id] ?? {});
  const descriptionParts = [
    safeToString(step.engine),
    safeToString(step.model),
    safeToString(step.backend),
    safeToString(step.device),
  ].filter(Boolean);
  return {
    id: entry.id,
    role: entry.role,
    label: humanizeTranscriptId(entry.id),
    description: descriptionParts.join(" · "),
    language: entry.language,
    isDefault,
  };
}

/**
 * Lists the transcript descriptors in a published portable manifest.
 */
export function listAvailableTranscripts(
  manifest: PortableMeetingManifest,
): PortableTranscriptDescriptor[] {
  const transcripts = Array.isArray(manifest.transcripts) ? manifest.transcripts : [];
  if (transcripts.length === 0) {
    throw new Error("portable manifest has no transcripts[]");
  }
  const defaultEntry = pickDefaultTranscript(transcripts);
  return transcripts.map((entry) => describeTranscript(entry, manifest, entry === defaultEntry));
}

/**
 * Returns the id of the transcript that the producer marked as default.
 */
export function getDefaultTranscriptId(manifest: PortableMeetingManifest): string {
  const transcripts = Array.isArray(manifest.transcripts) ? manifest.transcripts : [];
  if (transcripts.length === 0) {
    throw new Error("portable manifest has no transcripts[]");
  }
  return pickDefaultTranscript(transcripts).id;
}

function humanizeTranscriptId(value: string): string {
  return value
    .replace(/[_-]+/g, " ")
    .trim()
    .replace(/\b\w/g, (match) => match.toUpperCase());
}

/**
 * Resolves a per-transcript chunk set from the OpusTags map, base64url-decodes,
 * gzip-decompresses, verifies the SHA-256 against payloadRef.sha256, and parses
 * the resulting UTF-8 JSON. Throws on any integrity failure.
 */
export async function loadPortableTranscriptBody(
  tags: Record<string, string>,
  payloadRef: PortablePayloadRef,
): Promise<unknown> {
  if (!payloadRef || typeof payloadRef.prefix !== "string" || payloadRef.prefix === "") {
    throw new Error("portable transcript payloadRef is missing a prefix");
  }
  const chunkCount = typeof payloadRef.chunkCount === "number" ? payloadRef.chunkCount : 0;
  if (chunkCount <= 0) {
    throw new Error(`portable transcript ${payloadRef.prefix} has invalid chunkCount`);
  }
  if (!/^CASSINI_TX_[A-Z0-9_]+_PAYLOAD_$/.test(payloadRef.prefix)) {
    throw new Error(`portable transcript payloadRef has invalid prefix ${payloadRef.prefix}`);
  }
  if (payloadRef.encoding !== undefined && payloadRef.encoding !== "base64url+gzip+utf8json") {
    throw new Error(`portable transcript ${payloadRef.prefix} uses unsupported encoding`);
  }

  let encoded = "";
  for (let index = 0; index < chunkCount; index += 1) {
    const key = `${payloadRef.prefix}${String(index).padStart(3, "0")}`;
    const chunk = tags[key];
    if (!chunk) {
      throw new Error(`missing transcript payload chunk ${key}`);
    }
    encoded += chunk;
  }

  const compressed = decodeBase64Url(encoded);
  if (typeof payloadRef.gzipBytes === "number" && compressed.byteLength !== payloadRef.gzipBytes) {
    throw new Error(`portable transcript ${payloadRef.prefix} gzip byte count mismatch`);
  }
  const raw = await gunzipBytes(compressed);
  if (typeof payloadRef.rawBytes === "number" && raw.byteLength !== payloadRef.rawBytes) {
    throw new Error(`portable transcript ${payloadRef.prefix} raw byte count mismatch`);
  }
  const expected = typeof payloadRef.sha256 === "string" ? payloadRef.sha256.toLowerCase() : "";
  if (expected) {
    const actual = await sha256Hex(raw);
    if (actual !== expected) {
      throw new Error(
        `portable transcript ${payloadRef.prefix} sha256 mismatch (expected ${expected}, got ${actual})`,
      );
    }
  }
  return JSON.parse(new TextDecoder().decode(raw)) as unknown;
}

async function sha256Hex(bytes: Uint8Array): Promise<string> {
  const subtle = globalThis.crypto?.subtle;
  if (!subtle) {
    return sha256HexFallback(bytes);
  }
  // Copy to a fresh ArrayBuffer so WebCrypto never sees unrelated bytes from a
  // wider typed-array backing buffer.
  const digest = await subtle.digest("SHA-256", bytes.slice().buffer as ArrayBuffer);
  const view = new Uint8Array(digest);
  let hex = "";
  for (let index = 0; index < view.length; index += 1) {
    hex += view[index]!.toString(16).padStart(2, "0");
  }
  return hex;
}

const SHA256_K = [
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5,
  0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3,
  0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc,
  0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7,
  0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13,
  0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3,
  0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5,
  0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208,
  0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
];

export function sha256HexFallback(bytes: Uint8Array): string {
  const bitLength = bytes.byteLength * 8;
  const paddedLength =
    bytes.byteLength + 1 + ((64 - ((bytes.byteLength + 1 + 8) % 64)) % 64) + 8;
  const message = new Uint8Array(paddedLength);
  message.set(bytes);
  message[bytes.byteLength] = 0x80;
  const highBits = Math.floor(bitLength / 0x100000000);
  const lowBits = bitLength >>> 0;
  message[paddedLength - 8] = (highBits >>> 24) & 0xff;
  message[paddedLength - 7] = (highBits >>> 16) & 0xff;
  message[paddedLength - 6] = (highBits >>> 8) & 0xff;
  message[paddedLength - 5] = highBits & 0xff;
  message[paddedLength - 4] = (lowBits >>> 24) & 0xff;
  message[paddedLength - 3] = (lowBits >>> 16) & 0xff;
  message[paddedLength - 2] = (lowBits >>> 8) & 0xff;
  message[paddedLength - 1] = lowBits & 0xff;

  let h0 = 0x6a09e667;
  let h1 = 0xbb67ae85;
  let h2 = 0x3c6ef372;
  let h3 = 0xa54ff53a;
  let h4 = 0x510e527f;
  let h5 = 0x9b05688c;
  let h6 = 0x1f83d9ab;
  let h7 = 0x5be0cd19;
  const words = new Uint32Array(64);

  for (let offset = 0; offset < message.length; offset += 64) {
    for (let index = 0; index < 16; index += 1) {
      const base = offset + index * 4;
      words[index] =
        (((message[base] ?? 0) << 24) |
          ((message[base + 1] ?? 0) << 16) |
          ((message[base + 2] ?? 0) << 8) |
          (message[base + 3] ?? 0)) >>>
        0;
    }
    for (let index = 16; index < 64; index += 1) {
      const s0 =
        rotateRight(words[index - 15]!, 7) ^
        rotateRight(words[index - 15]!, 18) ^
        (words[index - 15]! >>> 3);
      const s1 =
        rotateRight(words[index - 2]!, 17) ^
        rotateRight(words[index - 2]!, 19) ^
        (words[index - 2]! >>> 10);
      words[index] = (words[index - 16]! + s0 + words[index - 7]! + s1) >>> 0;
    }

    let a = h0;
    let b = h1;
    let c = h2;
    let d = h3;
    let e = h4;
    let f = h5;
    let g = h6;
    let h = h7;

    for (let index = 0; index < 64; index += 1) {
      const s1 = rotateRight(e, 6) ^ rotateRight(e, 11) ^ rotateRight(e, 25);
      const ch = (e & f) ^ (~e & g);
      const temp1 = (h + s1 + ch + SHA256_K[index]! + words[index]!) >>> 0;
      const s0 = rotateRight(a, 2) ^ rotateRight(a, 13) ^ rotateRight(a, 22);
      const maj = (a & b) ^ (a & c) ^ (b & c);
      const temp2 = (s0 + maj) >>> 0;

      h = g;
      g = f;
      f = e;
      e = (d + temp1) >>> 0;
      d = c;
      c = b;
      b = a;
      a = (temp1 + temp2) >>> 0;
    }

    h0 = (h0 + a) >>> 0;
    h1 = (h1 + b) >>> 0;
    h2 = (h2 + c) >>> 0;
    h3 = (h3 + d) >>> 0;
    h4 = (h4 + e) >>> 0;
    h5 = (h5 + f) >>> 0;
    h6 = (h6 + g) >>> 0;
    h7 = (h7 + h) >>> 0;
  }

  return [h0, h1, h2, h3, h4, h5, h6, h7]
    .map((word) => word.toString(16).padStart(8, "0"))
    .join("");
}

function rotateRight(value: number, bits: number): number {
  return ((value >>> bits) | (value << (32 - bits))) >>> 0;
}

export function buildTranscriptWordsFromPortable(
  portable: PortableMeetingManifest,
  mediaSrc = "meeting.opus",
): TranscriptWordsV1 {
  const speakers = normalizeSpeakers(portable.speakers || []);
  const items = Array.isArray(portable.transcript?.items) ? portable.transcript.items : [];
  const segments = items.map((item, index) => {
    const segment = asRecord(item);
    const segmentId =
      typeof segment.id === "string" && segment.id.trim() !== ""
        ? segment.id
        : `seg_${String(index).padStart(6, "0")}`;
    const startMs = safeToInt(segment.startMs, 0);
    const endMs = safeToInt(segment.endMs, startMs);
    const text = typeof segment.text === "string" ? segment.text : "";
    // Portable meetings may contain either true word-level transcript items or
    // older segment-level text spans. Only the single-word case is safe to turn
    // back into a timed transcript word. Multi-word spans would fabricate
    // uniform word timings that were never produced by ASR.
    const words = isSinglePortableWord(text)
      ? splitTextIntoWords(text, startMs, endMs)
      : [];
    const speaker =
      typeof segment.speaker === "string" && segment.speaker.trim() !== "" ? segment.speaker : undefined;
    // Attribution provenance rides on the raw-asr items themselves (optional
    // keys; null and non-finite values mean "not measured"). Copy it onto
    // every word the item yields, or the canonical index — the thing the
    // crosstalk badge is judged on — silently loses the evidence for meetings
    // opened from a .opus.
    const attributionGapDb =
      typeof segment.attributionGapDb === "number" && Number.isFinite(segment.attributionGapDb)
        ? segment.attributionGapDb
        : undefined;
    const lowConfidenceSpeaker = segment.lowConfidenceSpeaker === true ? true : undefined;

    return {
      id: segmentId,
      speaker,
      startMs,
      endMs,
      text,
      words: words.map((word) => ({
        ...word,
        id: `${segmentId}:${word.id}`,
        ...(attributionGapDb === undefined ? {} : { attributionGapDb }),
        ...(lowConfidenceSpeaker === undefined ? {} : { lowConfidenceSpeaker }),
      })),
    };
  });

  return {
    version: "transcript.words.v1",
    media: {
      src: mediaSrc,
      durationMs: safeToInt(portable.meeting?.durationMs, 0),
      sha256: safeToString(portable.audio?.sha256) || undefined,
    },
    speakers,
    segments,
  };
}

function isSinglePortableWord(text: string): boolean {
  return typeof text === "string" && text.trim().split(/\s+/).filter(Boolean).length <= 1;
}

function extractPortableReadableWords(
  value: Record<string, unknown>,
  segmentId: string,
): TranscriptWordsV1["segments"][number]["words"] {
  if (!Array.isArray(value.words)) {
    return [];
  }
  return value.words.flatMap((wordValue, index) => {
    const word = asRecord(wordValue);
    const text = safeToString(word.text).trim();
    if (!text) {
      return [];
    }
    const startMs = safeToInt(word.startMs, NaN);
    const endMs = safeToInt(word.endMs, startMs);
    if (!Number.isFinite(startMs) || !Number.isFinite(endMs)) {
      return [];
    }
    // Deliberately no attribution carry here: readable-segment words only ever
    // feed display TIMING (token start/end via sourceWords) — no judgement or
    // rendering path reads attribution off them. The producer does write them:
    // every readable segment of every packed meeting in this repo's export tree
    // carries words. It just never writes ATTRIBUTION on them. That travels on
    // the raw-asr items instead (see buildTranscriptWordsFromPortable), which is
    // what the canonical index — and therefore the crosstalk badge — is built
    // from.
    return [{
      id: typeof word.id === "string" && word.id.trim() !== "" ? word.id : `${segmentId}:w_${index}`,
      text,
      startMs,
      endMs,
    }];
  });
}

function extractTranscriptArtifactWords(
  value: Record<string, unknown>,
  segmentId: string,
): TranscriptWordsV1["segments"][number]["words"] {
  // Runtime portable files need the same synthetic source-word IDs as export
  // so cleaned-word seek still lands on exact transcript timings in the UI.
  if (!Array.isArray(value.words)) {
    return [];
  }
  return value.words.flatMap((wordValue, index) => {
    const word = asRecord(wordValue);
    const text = safeToString(word.text).trim();
    if (!text) {
      return [];
    }
    const startMs = safeToInt(word.startMs, Number.NaN);
    const endMs = safeToInt(word.endMs, startMs);
    if (!Number.isFinite(startMs) || !Number.isFinite(endMs)) {
      return [];
    }
    return [{
      id: typeof word.id === "string" && word.id.trim() !== "" ? word.id : `${segmentId}:w_${index}`,
      text,
      startMs,
      endMs,
    }];
  });
}

export function buildReadableTranscriptFromPortable(
  portable: PortableMeetingManifest,
  transcript: TranscriptWordsV1,
): ReadableTranscriptV1 {
  const provided = asRecord(portable.readableTranscript);
  const speakers = normalizeSpeakers(portable.speakers || transcript.speakers || []);
  const validSpeakerIds = new Set(speakers.map((speaker) => speaker.id));

  if (
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
      segments: provided.segments.map((segmentValue, index) => {
        const segment = asRecord(segmentValue);
        const segmentId =
          typeof segment.id === "string" && segment.id.trim() !== ""
            ? segment.id
            : `readable_${String(index).padStart(6, "0")}`;
        const sourceSegmentIds = Array.isArray(segment.sourceSegmentIds)
          ? segment.sourceSegmentIds.filter((entry): entry is string => typeof entry === "string" && entry.trim() !== "")
          : [];
        const speaker =
          typeof segment.speaker === "string" &&
          segment.speaker.trim() !== "" &&
          validSpeakerIds.has(segment.speaker)
            ? segment.speaker
            : undefined;
        const words = extractPortableReadableWords(segment, segmentId);
        return {
          id: segmentId,
          speaker,
          startMs: safeToInt(segment.startMs, 0),
          endMs: safeToInt(segment.endMs, safeToInt(segment.startMs, 0)),
          text: typeof segment.text === "string" ? segment.text : "",
          sourceSegmentIds,
          ...(words.length > 0 ? ({ words } as object) : {}),
        };
      }),
      sourceTranscriptVersion:
        typeof provided.sourceTranscriptVersion === "string"
          ? provided.sourceTranscriptVersion
          : "transcript.words.v1",
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

interface DisplaySourceBlock {
  id: string;
  speaker?: string;
  speakerLabel: string;
  startMs: number;
  endMs: number;
  text: string;
  sourceSegmentIds: string[];
  words: TranscriptWordsV1["segments"][number]["words"];
}

function buildReadableDisplaySourceBlocks(
  readable: ReadableTranscriptV1,
  speakerLabels: Map<string, string>,
): DisplaySourceBlock[] {
  return readable.segments.map((segment, index) => {
    const segmentRecord = asRecord(segment as unknown);
    const blockId =
      typeof segment.id === "string" && segment.id.trim() !== ""
        ? segment.id
        : `rseg_${String(index).padStart(6, "0")}`;
    const sourceSegmentIds = Array.isArray(segment.sourceSegmentIds) ? [...segment.sourceSegmentIds] : [];
    return {
      id: blockId,
      speaker: segment.speaker,
      speakerLabel: segment.speaker
        ? speakerLabels.get(segment.speaker) || segment.speaker
        : "Unknown speaker",
      startMs: safeToInt(segment.startMs, 0),
      endMs: safeToInt(segment.endMs, safeToInt(segment.startMs, 0)),
      text: typeof segment.text === "string" ? segment.text : "",
      sourceSegmentIds,
      words: extractPortableReadableWords(segmentRecord, blockId),
    } satisfies DisplaySourceBlock;
  });
}

export function buildDisplayTranscriptFromArtifacts(
  transcript: TranscriptWordsV1,
  readable: ReadableTranscriptV1 | null,
): DisplayTranscriptV1 {
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
        ? block.sourceSegmentIds.filter((value): value is string => typeof value === "string" && value.trim() !== "")
        : [];
      const sourceSegments = resolveDisplaySourceSegments({
        block,
        sourceSegmentIds,
        segmentById,
        transcriptSegments,
      });
      const sourceWordsFromTranscript = sourceSegments.flatMap((segment, index) =>
        extractTranscriptArtifactWords(
          asRecord(segment as unknown),
          typeof segment.id === "string" && segment.id.trim() !== ""
            ? segment.id
            : `seg_${String(index).padStart(6, "0")}`,
        ),
      );
      const sourceWordsFromReadable = extractPortableReadableWords(
        asRecord(block),
        `block_${String(blockIndex).padStart(6, "0")}`,
      );
      const sourceWords = sourceWordsFromTranscript.length > 0 ? sourceWordsFromTranscript : sourceWordsFromReadable;
      const sourceWordById = new Map(
        sourceWords
          .filter((word): word is NonNullable<typeof word> & { id: string } => typeof word.id === "string" && word.id.trim() !== "")
          .map((word) => [word.id, word]),
      );
      const sourceWordIndexById = new Map(
        sourceWords
          .filter((word): word is NonNullable<typeof word> & { id: string } => typeof word.id === "string" && word.id.trim() !== "")
          .map((word, index) => [word.id, index]),
      );
      const alignment = alignReadableTokensToSourceWords(
        sourceWords,
        typeof block.text === "string" ? block.text : "",
      );
      const fallbackStartMs =
        sourceWords.length > 0
          ? safeToInt(sourceWords[0]?.startMs, safeToInt(block.startMs, 0))
          : sourceSegments.length > 0
            ? safeToInt(sourceSegments[0]?.startMs, safeToInt(block.startMs, 0))
            : safeToInt(block.startMs, 0);
      const fallbackEndMs =
        sourceWords.length > 0
          ? safeToInt(sourceWords[sourceWords.length - 1]?.endMs, safeToInt(block.endMs, fallbackStartMs))
          : sourceSegments.length > 0
            ? safeToInt(
                sourceSegments[sourceSegments.length - 1]?.endMs,
                safeToInt(block.endMs, fallbackStartMs),
              )
            : safeToInt(block.endMs, fallbackStartMs);
      const exactTokens = alignment.tokens.map((token) => {
        const matchedWords = token.sourceWordIds
          .map((wordId) => sourceWordById.get(wordId))
          .filter((word): word is NonNullable<typeof word> => Boolean(word));
        if (matchedWords.length > 0) {
          return {
            text: token.text,
            spaceBefore: token.spaceBefore,
            kind: token.kind,
            sourceWordIds: [...token.sourceWordIds],
            startMs: Math.min(...matchedWords.map((word) => safeToInt(word.startMs, fallbackStartMs))),
            endMs: Math.max(...matchedWords.map((word) => safeToInt(word.endMs, fallbackEndMs))),
            alignment: "source" as const,
          };
        }
        return {
          text: token.text,
          spaceBefore: token.spaceBefore,
          kind: token.kind,
          sourceWordIds: [...token.sourceWordIds],
          alignment: "none" as const,
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
        startMs: timedTokens.length > 0 ? safeToInt(timedTokens[0]?.startMs, fallbackStartMs) : fallbackStartMs,
        endMs:
          timedTokens.length > 0
            ? safeToInt(timedTokens[timedTokens.length - 1]?.endMs, fallbackEndMs)
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

function interpolateUntimedWordRuns(
  tokens: DisplayTranscriptV1["blocks"][number]["tokens"],
  fallbackStartMs: number,
  fallbackEndMs: number,
  sourceWords: Array<{ text?: string }>,
  sourceWordIndexById: Map<string, number>,
): DisplayTranscriptV1["blocks"][number]["tokens"] {
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
    if (
      !shouldInterpolateUntimedRun({
        tokens: next,
        runTokenIndexes,
        prevTimedToken,
        nextTimedToken,
        sourceWords,
        sourceWordIndexById,
      })
    ) {
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
      const tokenStart =
        runTokenIndexes.length <= 1
          ? startMs
          : startMs + Math.floor((span * index) / runTokenIndexes.length);
      const tokenEnd =
        runTokenIndexes.length <= 1
          ? endMs
          : startMs + Math.floor((span * (index + 1)) / runTokenIndexes.length);
      next[runTokenIndex] = {
        ...next[runTokenIndex],
        startMs: tokenStart,
        endMs: Math.max(tokenEnd, tokenStart),
        alignment: "interpolated" as const,
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
}: {
  tokens: DisplayTranscriptV1["blocks"][number]["tokens"];
  runTokenIndexes: number[];
  prevTimedToken: DisplayTranscriptV1["blocks"][number]["tokens"][number] | null;
  nextTimedToken: DisplayTranscriptV1["blocks"][number]["tokens"][number] | null;
  sourceWords: Array<{ text?: string }>;
  sourceWordIndexById: Map<string, number>;
}): boolean {
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

function resolveTokenSourceIndexes(
  token: DisplayTranscriptV1["blocks"][number]["tokens"][number] | null | undefined,
  sourceWordIndexById: Map<string, number>,
): number[] {
  if (!token || !Array.isArray(token.sourceWordIds)) {
    return [];
  }
  return token.sourceWordIds
    .map((wordId) => sourceWordIndexById.get(wordId))
    .filter((index): index is number => Number.isInteger(index));
}

function resolveInterpolatedSpan({
  prevTimedToken,
  nextTimedToken,
  fallbackStartMs,
  fallbackEndMs,
}: {
  prevTimedToken: DisplayTranscriptV1["blocks"][number]["tokens"][number] | null;
  nextTimedToken: DisplayTranscriptV1["blocks"][number]["tokens"][number] | null;
  fallbackStartMs: number;
  fallbackEndMs: number;
}): { startMs: number; endMs: number } {
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

function tokenHasTiming(token: DisplayTranscriptV1["blocks"][number]["tokens"][number] | null | undefined): boolean {
  return Boolean(token) && Number.isInteger(token.startMs) && Number.isInteger(token.endMs);
}

export function describeMeeting(meetingId: string): { title: string; dateLabel: string } {
  const normalizedMeetingId = stripVariantSuffix(meetingId);
  const colonTimeStamp = parseTimestampFromDoubledDashParts(normalizedMeetingId, "--");
  if (colonTimeStamp) {
    return colonTimeStamp;
  }

  const modernStamp = /^(.*)--(\d{8})T(\d{2})(\d{2})(\d{2})$/.exec(normalizedMeetingId);
  if (modernStamp) {
    const [, rawTitle, yyyymmdd, hour, minute] = modernStamp;
    return {
      title: toTitleCase(rawTitle),
      dateLabel: `${yyyymmdd.slice(0, 4)}-${yyyymmdd.slice(4, 6)}-${yyyymmdd.slice(6, 8)} ${hour}:${minute}`,
    };
  }

  const legacyStamp = /^(.*)--(\d{4})-(\d{2})-(\d{2})--(\d{2})-(\d{2})-(\d{2})$/.exec(normalizedMeetingId);
  if (legacyStamp) {
    const [, rawTitle, year, month, day, hour, minute] = legacyStamp;
    return {
      title: toTitleCase(rawTitle),
      dateLabel: `${year}-${month}-${day} ${hour}:${minute}`,
    };
  }

  // Talk recordings are named by the operator's ULID job id, which carries no
  // human-readable name but does encode the recording start time. Surface that
  // timestamp instead of showing the raw id as both title and date (D-462).
  const ulidDateLabel = dateLabelFromUlid(normalizedMeetingId);
  if (ulidDateLabel) {
    return {
      title: "Untitled meeting",
      dateLabel: ulidDateLabel,
    };
  }

  return {
    title: toTitleCase(normalizedMeetingId),
    dateLabel: normalizedMeetingId,
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
function dateLabelFromUlid(meetingId: string): string {
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
  const date = new Date(ms);
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${date.getUTCFullYear()}-${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())} ${pad(
    date.getUTCHours(),
  )}:${pad(date.getUTCMinutes())}`;
}

export function describeVariantSuffix(meetingId: string): string {
  const match = /--stt-([A-Za-z0-9._-]+)$/.exec(meetingId);
  if (!match) {
    return "";
  }
  return toTitleCase(match[1].replace(/\./g, "-"));
}

function parseOpusCommentTags(bytes: Uint8Array): Record<string, string> {
  const packets = extractOggPackets(bytes, 2);
  if (packets.length < 2) {
    throw new Error("portable opus file is missing OpusTags");
  }
  const tagsPacket = packets[1];
  const signature = new TextDecoder().decode(tagsPacket.subarray(0, 8));
  if (signature !== "OpusTags") {
    throw new Error("portable opus file does not contain a valid OpusTags packet");
  }

  const view = new DataView(tagsPacket.buffer, tagsPacket.byteOffset, tagsPacket.byteLength);
  let offset = 8;
  const vendorLength = view.getUint32(offset, true);
  offset += 4 + vendorLength;
  if (offset + 4 > tagsPacket.byteLength) {
    throw new Error("portable opus file has a truncated OpusTags vendor string");
  }

  const commentCount = view.getUint32(offset, true);
  offset += 4;
  const tags: Record<string, string> = {};
  const decoder = new TextDecoder();

  for (let index = 0; index < commentCount; index += 1) {
    if (offset + 4 > tagsPacket.byteLength) {
      throw new Error("portable opus file has a truncated OpusTags comment header");
    }
    const length = view.getUint32(offset, true);
    offset += 4;
    if (offset + length > tagsPacket.byteLength) {
      throw new Error("portable opus file has a truncated OpusTags comment value");
    }
    const comment = decoder.decode(tagsPacket.subarray(offset, offset + length));
    offset += length;
    const separator = comment.indexOf("=");
    if (separator <= 0) {
      continue;
    }
    tags[comment.slice(0, separator).toUpperCase()] = comment.slice(separator + 1);
  }

  return tags;
}

function extractOggPackets(bytes: Uint8Array, targetPacketCount: number): Uint8Array[] {
  const packets: Uint8Array[] = [];
  let offset = 0;
  let currentPacketParts: Uint8Array[] = [];
  let currentPacketLength = 0;

  while (offset + 27 <= bytes.byteLength && packets.length < targetPacketCount) {
    if (readAscii(bytes, offset, 4) !== "OggS") {
      throw new Error("portable opus file is not a valid Ogg stream");
    }
    const pageSegments = bytes[offset + 26] ?? 0;
    const headerLength = 27 + pageSegments;
    if (offset + headerLength > bytes.byteLength) {
      throw new Error("portable opus file has a truncated Ogg page header");
    }
    const segmentTable = bytes.subarray(offset + 27, offset + 27 + pageSegments);
    const pageDataLength = segmentTable.reduce((sum, value) => sum + value, 0);
    const pageDataStart = offset + headerLength;
    const pageDataEnd = pageDataStart + pageDataLength;
    if (pageDataEnd > bytes.byteLength) {
      throw new Error("portable opus file has a truncated Ogg page payload");
    }
    const pageData = bytes.subarray(pageDataStart, pageDataEnd);

    let cursor = 0;
    for (const segmentLength of segmentTable) {
      const nextCursor = cursor + segmentLength;
      currentPacketParts.push(pageData.subarray(cursor, nextCursor));
      currentPacketLength += segmentLength;
      cursor = nextCursor;
      if (segmentLength < 255) {
        packets.push(concatenateChunks(currentPacketParts, currentPacketLength));
        currentPacketParts = [];
        currentPacketLength = 0;
        if (packets.length >= targetPacketCount) {
          break;
        }
      }
    }

    offset = pageDataEnd;
  }

  return packets;
}

function readAscii(bytes: Uint8Array, offset: number, length: number): string {
  return new TextDecoder().decode(bytes.subarray(offset, offset + length));
}

function concatenateChunks(chunks: Uint8Array[], totalLength: number): Uint8Array {
  const out = new Uint8Array(totalLength);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return out;
}

function decodeBase64Url(value: string): Uint8Array {
  const normalized = value.replace(/-/g, "+").replace(/_/g, "/");
  const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, "=");
  const binary = atob(padded);
  const out = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) {
    out[index] = binary.charCodeAt(index);
  }
  return out;
}

async function gunzipBytes(bytes: Uint8Array): Promise<Uint8Array> {
  const stream = new Blob([bytes]).stream().pipeThrough(new DecompressionStream("gzip"));
  return new Uint8Array(await new Response(stream).arrayBuffer());
}

function groupTranscriptSegmentsAsReadable(
  segments: TranscriptWordsV1["segments"],
): ReadableTranscriptV1["segments"] {
  const grouped: ReadableTranscriptV1["segments"] = [];
  let current:
    | {
        speaker?: string;
        startMs: number;
        endMs: number;
        wordCount: number;
        text: string;
        sourceSegments: TranscriptWordsV1["segments"];
      }
    | null = null;
  const hardGapMs = 4200;
  const softGapMs = 2200;
  const targetParagraphWords = 96;
  const targetParagraphDurationMs = 45_000;
  const maxParagraphWords = 140;
  const maxParagraphDurationMs = 90_000;
  const minStandaloneWords = 18;
  const minStandaloneDurationMs = 8000;

  const flush = () => {
    if (!current || current.sourceSegments.length === 0) {
      current = null;
      return;
    }
    grouped.push({
      id: `r_${current.sourceSegments[0]?.id ?? "segment"}`,
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
    const speaker = typeof segment.speaker === "string" && segment.speaker.trim() !== "" ? segment.speaker : undefined;
    const startMs = safeToInt(segment.startMs, 0);
    const endMs = safeToInt(segment.endMs, startMs);
    const segmentWordCount =
      Array.isArray(segment.words) && segment.words.length > 0
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
    const paragraphTargetReached =
      current.wordCount >= targetParagraphWords ||
      current.endMs - current.startMs >= targetParagraphDurationMs;
    const naturalBoundary = endsSentence(current.text) || gapMs >= softGapMs;
    const shouldFlush =
      speakerChanged ||
      gapMs > hardGapMs ||
      durationMs > maxParagraphDurationMs ||
      nextWordCount > maxParagraphWords ||
      (paragraphTargetReached && naturalBoundary);

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

function mergeSmallReadableSegments(
  grouped: ReadableTranscriptV1["segments"],
  options: {
    maxParagraphWords: number;
    maxParagraphDurationMs: number;
    hardGapMs: number;
    minStandaloneWords: number;
    minStandaloneDurationMs: number;
  },
): ReadableTranscriptV1["segments"] {
  const merged: ReadableTranscriptV1["segments"] = [];
  for (const segment of grouped) {
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
    const oneSideTooSmall =
      previousWordCount < options.minStandaloneWords ||
      currentWordCount < options.minStandaloneWords ||
      previousDurationMs < options.minStandaloneDurationMs ||
      currentDurationMs < options.minStandaloneDurationMs;

    if (
      sameSpeaker &&
      gapMs <= options.hardGapMs &&
      combinedWordCount <= options.maxParagraphWords &&
      combinedDurationMs <= options.maxParagraphDurationMs &&
      oneSideTooSmall
    ) {
      previous.endMs = segment.endMs;
      previous.text = joinSegmentTexts([{ text: previous.text }, { text: segment.text }]);
      previous.sourceSegmentIds = [...previous.sourceSegmentIds, ...segment.sourceSegmentIds];
      continue;
    }

    merged.push(segment);
  }
  return merged;
}

export function splitTextIntoWords(
  text: string,
  startMs: number,
  endMs: number,
): Array<{ id: string; text: string; startMs: number; endMs: number }> {
  const parts = typeof text === "string" ? text.trim().split(/\s+/).filter(Boolean) : [];
  if (parts.length === 0) {
    return [];
  }

  const span = Math.max(0, endMs - startMs);
  return parts.map((part, index) => {
    const from = parts.length <= 1 ? startMs : startMs + Math.floor((span * index) / parts.length);
    const to = parts.length <= 1 ? endMs : startMs + Math.floor((span * (index + 1)) / parts.length);
    return {
      id: `w_${index}`,
      text: part,
      startMs: Math.min(Math.max(from, startMs), endMs),
      endMs: Math.max(Math.min(to, endMs), startMs),
    };
  });
}

function normalizeSpeakers(value: unknown): TranscriptSpeaker[] {
  if (!Array.isArray(value)) {
    return [];
  }

  const speakers: TranscriptSpeaker[] = [];
  const seen = new Set<string>();
  for (const item of value) {
    const speaker = asRecord(item);
    const id = safeToString(speaker.id);
    const label = safeToString(speaker.label) || id;
    if (id.trim() === "" || seen.has(id)) {
      continue;
    }
    seen.add(id);
    speakers.push({ id, label });
  }
  return speakers;
}

function stripVariantSuffix(meetingId: string): string {
  return meetingId.replace(/--stt-[A-Za-z0-9._-]+$/, "");
}

function parseTimestampFromDoubledDashParts(
  meetingId: string,
  joiner = "--",
): { title: string; dateLabel: string } | null {
  const parts = meetingId.split(joiner);
  if (parts.length < 2) {
    return null;
  }

  const timeParts = parts.at(-1);
  const timeMatch = /^([0-9]{2}):([0-9]{2})(?:\:([0-9]{2}))?$/.exec(timeParts ?? "");
  if (!timeMatch) {
    return null;
  }
  const hour = timeMatch[1];
  const minute = timeMatch[2];
  const dateCandidate = parts.at(-2);

  const dateMatch = /^(\d{4})-(\d{2})-(\d{2})$/.exec(dateCandidate ?? "");
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

  const legacyDateMatch = /^(.*)-(\d{4})-(\d{2})-(\d{2})$/.exec(parts[0] ?? "");
  if (!legacyDateMatch) {
    return null;
  }
  const [, rawTitle, year, month, day] = legacyDateMatch;
  return {
    title: toTitleCase(rawTitle),
    dateLabel: `${year}-${month}-${day} ${hour}:${minute}`,
  };
}

function toTitleCase(text: string): string {
  return text
    .split("-")
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function resolveDisplaySourceSegments({
  block,
  sourceSegmentIds,
  segmentById,
  transcriptSegments,
}: {
  block: {
    startMs?: unknown;
    endMs?: unknown;
    speaker?: unknown;
    text?: unknown;
  };
  sourceSegmentIds: string[];
  segmentById: Map<string, TranscriptWordsV1["segments"][number]>;
  transcriptSegments: TranscriptWordsV1["segments"];
}): TranscriptWordsV1["segments"] {
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
  const blockStartMs = safeToInt(block.startMs, 0);
  const blockEndMs = safeToInt(block.endMs, blockStartMs);
  return transcriptSegments.filter((segment) => {
    const startMs = safeToInt(segment.startMs, 0);
    const endMs = safeToInt(segment.endMs, startMs);
    if (endMs < blockStartMs || startMs > blockEndMs) {
      return false;
    }
    if (typeof block.speaker === "string" && segment.speaker && segment.speaker !== block.speaker) {
      return false;
    }
    return true;
  });
}

function resolvedSegmentsLookCompatible({
  block,
  resolvedSegments,
}: {
  block: {
    startMs?: unknown;
    endMs?: unknown;
    text?: unknown;
  };
  resolvedSegments: TranscriptWordsV1["segments"];
}): boolean {
  const targetWordCount = tokenizeDisplayText(typeof block.text === "string" ? block.text : "").filter(
    (token) => token.kind === "word",
  ).length;
  const resolvedWordCount = resolvedSegments.flatMap((segment) =>
    Array.isArray(segment.words) ? segment.words : [],
  ).length;
  if (targetWordCount > 0 && resolvedWordCount < targetWordCount) {
    return false;
  }

  const blockStartMs = safeToInt(block.startMs, 0);
  const blockEndMs = safeToInt(block.endMs, blockStartMs);
  const blockDurationMs = Math.max(0, blockEndMs - blockStartMs);
  if (blockDurationMs <= 0) {
    return true;
  }
  const resolvedStartMs = Math.min(...resolvedSegments.map((segment) => safeToInt(segment.startMs, blockStartMs)));
  const resolvedEndMs = Math.max(...resolvedSegments.map((segment) => safeToInt(segment.endMs, blockEndMs)));
  const resolvedDurationMs = Math.max(0, resolvedEndMs - resolvedStartMs);
  return resolvedDurationMs >= Math.max(1000, Math.floor(blockDurationMs / 2));
}

function tokenizeDisplayText(text: string): Array<{
  text: string;
  spaceBefore: boolean;
  kind: "word" | "punctuation";
}> {
  const tokenPattern = /[A-Za-z0-9]+(?:[.'’/_-][A-Za-z0-9]+)*|[^\w\s]/gu;
  const tokens: Array<{ text: string; spaceBefore: boolean; kind: "word" | "punctuation" }> = [];
  let match: RegExpExecArray | null;
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

function normalizeAlignmentToken(token: string): string {
  return String(token ?? "")
    .replaceAll("’", "'")
    .toLowerCase()
    .replace(/^[^\w']+|[^\w']+$/gu, "");
}

function isWordToken(token: string): boolean {
  return normalizeAlignmentToken(token) !== "";
}

function alignReadableTokensToSourceWords(
  sourceWords: Array<{ id?: string; text?: string; startMs?: number; endMs?: number }>,
  readableText: string,
): {
  tokens: Array<{
    text: string;
    spaceBefore: boolean;
    kind: "word" | "punctuation";
    sourceWordIds: string[];
  }>;
} {
  const tokens = tokenizeDisplayText(readableText);
  const targetWordPositions: number[] = [];
  const targetNorms: string[] = [];
  for (let index = 0; index < tokens.length; index += 1) {
    if (tokens[index]?.kind !== "word") {
      continue;
    }
    targetWordPositions.push(index);
    targetNorms.push(normalizeAlignmentToken(tokens[index]?.text ?? ""));
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
      text: token.text,
      spaceBefore: token.spaceBefore,
      kind: token.kind,
      sourceWordIds: targetIndexToSourceWordIds.get(index) ?? [],
    })),
  };
}

function buildLcsTable(sourceNorms: string[], targetNorms: string[]): number[][] {
  const dp = Array.from({ length: sourceNorms.length + 1 }, () =>
    Array.from({ length: targetNorms.length + 1 }, () => 0),
  );
  for (let sourceIndex = sourceNorms.length - 1; sourceIndex >= 0; sourceIndex -= 1) {
    for (let targetIndex = targetNorms.length - 1; targetIndex >= 0; targetIndex -= 1) {
      if (sourceNorms[sourceIndex] === targetNorms[targetIndex]) {
        dp[sourceIndex]![targetIndex] = dp[sourceIndex + 1]![targetIndex + 1]! + 1;
        continue;
      }
      dp[sourceIndex]![targetIndex] = Math.max(
        dp[sourceIndex + 1]![targetIndex]!,
        dp[sourceIndex]![targetIndex + 1]!,
      );
    }
  }
  return dp;
}

function reconstructLcsAlignment({
  sourceWords,
  sourceNorms,
  targetWordPositions,
  targetNorms,
  dp,
}: {
  sourceWords: Array<{ id?: string }>;
  sourceNorms: string[];
  targetWordPositions: number[];
  targetNorms: string[];
  dp: number[][];
}): Map<number, string[]> {
  const targetIndexToSourceWordIds = new Map<number, string[]>();
  let sourceIndex = 0;
  let targetIndex = 0;
  while (sourceIndex < sourceNorms.length && targetIndex < targetNorms.length) {
    if (sourceNorms[sourceIndex] === targetNorms[targetIndex]) {
      const targetWordIndex = targetWordPositions[targetIndex];
      const sourceWordId = sourceWords[sourceIndex]?.id;
      if (targetWordIndex !== undefined && typeof sourceWordId === "string" && sourceWordId.trim() !== "") {
        targetIndexToSourceWordIds.set(targetWordIndex, [sourceWordId]);
      }
      sourceIndex += 1;
      targetIndex += 1;
      continue;
    }
    if (dp[sourceIndex + 1]![targetIndex]! >= dp[sourceIndex]![targetIndex + 1]!) {
      sourceIndex += 1;
    } else {
      targetIndex += 1;
    }
  }
  return targetIndexToSourceWordIds;
}

function joinSegmentTexts(segments: Array<{ text?: string }>): string {
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

function countWordsInText(text: string): number {
  return safeToString(text)
    .trim()
    .split(/\s+/)
    .filter(Boolean)
    .length;
}

function endsSentence(text: string): boolean {
  return /[.!?]["')\]]*$/.test(safeToString(text).trim());
}

function safeToInt(value: unknown, fallback: number): number {
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

function safeToString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function asRecord(value: unknown): Record<string, unknown> {
  return typeof value === "object" && value !== null ? value as Record<string, unknown> : {};
}
