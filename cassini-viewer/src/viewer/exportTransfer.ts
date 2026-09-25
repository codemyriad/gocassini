import type { MeetingCatalogEntry } from "./catalog";
import { safeMeetingStem } from "./meetingExport";

export function saveBlob(blob: Blob, name: string): void {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = name;
  // The host document owns downloads even when the viewer lives in a shadow root.
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  // Firefox and Safari can begin reading a large Blob after the click task.
  setTimeout(() => URL.revokeObjectURL(url), 40_000);
}

export function saveTranscript(text: string, name: string): void {
  saveBlob(new Blob([text], { type: "text/markdown;charset=utf-8" }), name);
}

export interface AudioFile {
  name: string;
  blob: Blob;
}

const AUDIO_EXTENSIONS: Record<string, string> = {
  opus: "audio/ogg",
  ogg: "audio/ogg",
  webm: "audio/webm",
  mp3: "audio/mpeg",
  mp4: "audio/mp4",
  m4a: "audio/mp4",
  wav: "audio/wav",
};

function audioExtension(path: string, mime: string): string {
  const pathname = new URL(path, "https://example.invalid").pathname;
  const extension = pathname.match(/\.([a-zA-Z0-9]+)$/)?.[1]?.toLowerCase();
  if (extension && extension in AUDIO_EXTENSIONS) return extension;
  const type = mime.split(";")[0]?.trim().toLowerCase();
  return Object.entries(AUDIO_EXTENSIONS).find(([, value]) => value === type)?.[0] ?? "opus";
}

export async function loadAudioFile(entry: Pick<MeetingCatalogEntry, "id" | "title" | "audioPath">): Promise<AudioFile> {
  if (!entry.audioPath) throw new Error(`${entry.title} has no meeting file to download.`);
  const response = await fetch(entry.audioPath, { cache: "no-store" });
  if (!response.ok) throw new Error(`Couldn't download the meeting file for ${entry.title} (${response.status}).`);
  const blob = await response.blob();
  const extension = audioExtension(entry.audioPath, blob.type);
  return {
    name: `${safeMeetingStem(entry)}.${extension}`,
    blob: blob.type ? blob : new Blob([blob], { type: AUDIO_EXTENSIONS[extension] }),
  };
}

// ZIP with stored entries: the meeting files are compressed already. This keeps
// the original portable file bytes intact and needs no codec or archive service.
const CRC_TABLE = Uint32Array.from({ length: 256 }, (_, index) => {
  let value = index;
  for (let bit = 0; bit < 8; bit++) value = (value >>> 1) ^ (value & 1 ? 0xedb88320 : 0);
  return value >>> 0;
});

async function crc32(blob: Blob): Promise<number> {
  let crc = 0xffffffff;
  const reader = blob.stream().getReader();
  while (true) {
    const { value, done } = await reader.read();
    if (done) break;
    for (const byte of value) {
      crc = CRC_TABLE[(crc ^ byte) & 0xff]! ^ (crc >>> 8);
    }
  }
  return (crc ^ 0xffffffff) >>> 0;
}

export async function zipAudioFiles(files: readonly AudioFile[]): Promise<Blob> {
  if (files.length === 0 || files.length > 0xffff) throw new Error("No meeting files to download.");
  const encoder = new TextEncoder();
  const parts: BlobPart[] = [];
  const directory: BlobPart[] = [];
  const usedNames = new Set<string>();
  let offset = 0;
  let directorySize = 0;
  for (const file of files) {
    let fileName = file.name;
    const dot = fileName.lastIndexOf(".");
    const stem = dot > 0 ? fileName.slice(0, dot) : fileName;
    const extension = dot > 0 ? fileName.slice(dot) : "";
    let suffix = 2;
    while (usedNames.has(fileName.toLowerCase())) {
      fileName = `${stem}-${suffix}${extension}`;
      suffix += 1;
    }
    usedNames.add(fileName.toLowerCase());
    const name = encoder.encode(fileName);
    const size = file.blob.size;
    if (name.length > 0xffff || size > 0xffffffff || offset + size + name.length + 30 > 0xffffffff) {
      throw new Error("The selected meeting files are too large for one download.");
    }
    const crc = await crc32(file.blob);
    const local = new ArrayBuffer(30);
    const l = new DataView(local);
    l.setUint32(0, 0x04034b50, true);
    l.setUint16(4, 20, true);
    l.setUint16(6, 0x0800, true); // UTF-8 names
    l.setUint16(12, 0x0021, true); // 1980-01-01, the earliest valid ZIP date
    l.setUint32(14, crc, true);
    l.setUint32(18, size, true);
    l.setUint32(22, size, true);
    l.setUint16(26, name.length, true);
    parts.push(local, name, file.blob);

    const central = new ArrayBuffer(46);
    const c = new DataView(central);
    c.setUint32(0, 0x02014b50, true);
    c.setUint16(4, 20, true);
    c.setUint16(6, 20, true);
    c.setUint16(8, 0x0800, true);
    c.setUint16(14, 0x0021, true);
    c.setUint32(16, crc, true);
    c.setUint32(20, size, true);
    c.setUint32(24, size, true);
    c.setUint16(28, name.length, true);
    c.setUint32(42, offset, true);
    directory.push(central, name);
    offset += 30 + name.length + size;
    directorySize += 46 + name.length;
  }
  if (offset + directorySize + 22 > 0xffffffff) {
    throw new Error("The selected meeting files are too large for one download.");
  }
  const end = new ArrayBuffer(22);
  const e = new DataView(end);
  e.setUint32(0, 0x06054b50, true);
  e.setUint16(8, files.length, true);
  e.setUint16(10, files.length, true);
  e.setUint32(12, directorySize, true);
  e.setUint32(16, offset, true);
  return new Blob([...parts, ...directory, end], { type: "application/zip" });
}
