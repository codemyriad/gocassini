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
