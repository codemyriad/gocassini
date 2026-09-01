export interface TranscriptMedia {
  src: string;
  durationMs: number;
  sha256?: string;
}

export interface TranscriptSpeaker {
  id: string;
  label: string;
}

export interface TranscriptWord {
  id?: string;
  text: string;
  startMs: number;
  endMs: number;
  confidence?: number;
  /**
   * How far the loudest OTHER participant's microphone sat above its own noise
   * floor compared with this speaker's, in dB, at this word. Near zero means the
   * attributed speaker was the loudest voice; a large positive value means
   * somebody else was, and the word is probably crosstalk bleeding into this
   * track rather than this person speaking. Absent when the producer did not
   * measure attribution.
   */
  attributionGapDb?: number;
  /**
   * Set when the gap cleared the threshold estimated from that meeting's own
   * level distribution. The word is still canonical transcript content — it is
   * shown, and can be read and overruled — but it is de-emphasised, and
   * generated summaries exclude it.
   */
  lowConfidenceSpeaker?: boolean;
}

export interface TranscriptSegment {
  id: string;
  speaker?: string;
  startMs: number;
  endMs: number;
  text: string;
  words: TranscriptWord[];
}

export interface TranscriptWordsV1 {
  version: "transcript.words.v1";
  media: TranscriptMedia;
  speakers: TranscriptSpeaker[];
  segments: TranscriptSegment[];
}

export interface ReadableTranscriptSegment {
  id: string;
  speaker?: string;
  startMs: number;
  endMs: number;
  text: string;
  sourceSegmentIds: string[];
}

export interface ReadableTranscriptV1 {
  version: "transcript.readable.v1";
  media: TranscriptMedia;
  speakers: TranscriptSpeaker[];
  segments: ReadableTranscriptSegment[];
  sourceTranscriptVersion?: string;
}

export type DisplayTranscriptTokenAlignment = "source" | "interpolated" | "none";
export type DisplayTranscriptTokenKind = "word" | "punctuation";

export interface DisplayTranscriptToken {
  text: string;
  spaceBefore: boolean;
  kind: DisplayTranscriptTokenKind;
  sourceWordIds: string[];
  startMs?: number;
  endMs?: number;
  alignment?: DisplayTranscriptTokenAlignment;
}

export interface DisplayTranscriptBlock {
  id: string;
  speaker?: string;
  speakerLabel: string;
  startMs: number;
  endMs: number;
  text: string;
  sourceSegmentIds: string[];
  wordCount: number;
  timedWordCount: number;
  timingCoverage: number;
  tokens: DisplayTranscriptToken[];
}

export interface DisplayTranscriptV1 {
  version: "transcript.display.v1";
  media: TranscriptMedia;
  speakers: TranscriptSpeaker[];
  blocks: DisplayTranscriptBlock[];
  sourceTranscriptVersion?: string;
  sourceReadableTranscriptVersion?: string;
}

export interface IndexedWord extends TranscriptWord {
  id: string;
  segmentId: string;
  segmentIndex: number;
  wordIndex: number;
  speakerLabel: string;
}

export interface IndexedSegment extends TranscriptSegment {
  index: number;
  speakerLabel: string;
  searchText: string;
  words: IndexedWord[];
}

export interface TranscriptIndex {
  transcript: TranscriptWordsV1;
  speakersById: Map<string, TranscriptSpeaker>;
  segments: IndexedSegment[];
  segmentStartTimes: number[];
}
