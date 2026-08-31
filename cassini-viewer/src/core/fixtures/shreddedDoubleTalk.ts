/**
 * The shredded double-talk fixture: transcript.words.v1 as current `main`
 * really emits it for the `overlap-and-pause.v1` harness scenario.
 *
 * PUBLIC SYNTHETIC DATA. The audio is text-to-speech from a scripted scenario
 * committed in this repo (`harness/scenarios/overlap-and-pause.v1.json`), so
 * the ground truth is known exactly and nothing here is anybody's meeting.
 * The numbers and the text below are copied verbatim from a producer run; the
 * only edits are the speaker ids (shortened) and `speakerLabel`, which the
 * viewer takes from the recording manifest's display names.
 *
 * WHAT IT CONTAINS, and why each part is here (ground truth from the scenario
 * and the TTS manifest that rendered it):
 *
 *   - 1.0-8.2 s   Ana speaks ONE sentence; Ben backchannels "Right." at 4.6 s.
 *                 The producer emits it as ana / ben / ana.
 *   - 13.0-19.3 s clean sequential turns, seconds of silence between them.
 *   - 23.0-29.4 s Cara speaks ONE sentence; Ana backchannels "Perfect." at
 *                 26.5 s. Again emitted as cara / ana / cara.
 *   - 29.1-31.1 s Ben genuinely takes the floor off Cara - a real interruption,
 *                 not a backchannel, and not the same turn resuming.
 *   - 40.8-49.0 s SUSTAINED DOUBLE TALK. Cara speaks one sentence 41.0-49.0 and
 *                 Ben speaks a different, competing sentence 43.2-49.0. Because
 *                 `MergeAndSortSegments` interleaves every word by start time
 *                 and flushes a segment at each speaker change, the pipeline
 *                 shreds those two sentences into THIRTY-ONE alternating
 *                 fragments of one to three words ("f the", "final", "sign",
 *                 "off"). This is the shape the fix exists for.
 *   - 52.5-58.4 s clean sequential control turns after the collision.
 *
 * Measured on these numbers: every gap inside one speaker's own shredded run is
 * between -1 ms and 241 ms, while the smallest gap across a real floor change
 * is 4176 ms. There is no ambiguity in the middle for the re-join threshold to
 * land in.
 *
 * Word-level timing is kept because the audible-interval machinery reads it;
 * every block's extent is exactly its own word envelope, as the producer emits.
 */
import type { OverlapBlock } from "../overlap";

export const shreddedDoubleTalkSegments: OverlapBlock[] = [
  {
    id: "seg_000000",
    speaker: "spk_ana",
    speakerLabel: "Ana Duarte",
    startMs: 1020,
    endMs: 5195,
    words: [
      { id: "seg_000000:w_0", text: "Let", startMs: 1020, endMs: 1179 },
      { id: "seg_000000:w_1", text: "me", startMs: 1179, endMs: 1339 },
      { id: "seg_000000:w_2", text: "walk", startMs: 1339, endMs: 1579 },
      { id: "seg_000000:w_3", text: "through", startMs: 1580, endMs: 1899 },
      { id: "seg_000000:w_4", text: "where", startMs: 1899, endMs: 2059 },
      { id: "seg_000000:w_5", text: "the", startMs: 2059, endMs: 2219 },
      { id: "seg_000000:w_6", text: "release", startMs: 2219, endMs: 2699 },
      { id: "seg_000000:w_7", text: "actually", startMs: 2699, endMs: 3099 },
      { id: "seg_000000:w_8", text: "stands", startMs: 3099, endMs: 3500 },
      { id: "seg_000000:w_9", text: "today.", startMs: 3500, endMs: 3884 },
      { id: "seg_000000:w_10", text: "The", startMs: 4476, endMs: 4635 },
      { id: "seg_000000:w_11", text: "installer", startMs: 4635, endMs: 5195 },
    ],
  },
  {
    id: "seg_000001",
    speaker: "spk_ben",
    speakerLabel: "Ben Okafor",
    startMs: 4859,
    endMs: 5260,
    words: [
      { id: "seg_000001:w_0", text: "Right.", startMs: 4859, endMs: 5260 },
    ],
  },
  {
    id: "seg_000002",
    speaker: "spk_ana",
    speakerLabel: "Ana Duarte",
    startMs: 5195,
    endMs: 8236,
    words: [
      { id: "seg_000002:w_0", text: "is", startMs: 5195, endMs: 5355 },
      { id: "seg_000002:w_1", text: "finished,", startMs: 5355, endMs: 5756 },
      { id: "seg_000002:w_2", text: "and", startMs: 5836, endMs: 5996 },
      { id: "seg_000002:w_3", text: "the", startMs: 5995, endMs: 6235 },
      { id: "seg_000002:w_4", text: "documentation", startMs: 6235, endMs: 6955 },
      { id: "seg_000002:w_5", text: "has", startMs: 6956, endMs: 7116 },
      { id: "seg_000002:w_6", text: "already", startMs: 7115, endMs: 7515 },
      { id: "seg_000002:w_7", text: "been", startMs: 7515, endMs: 7755 },
      { id: "seg_000002:w_8", text: "merged.", startMs: 7755, endMs: 8236 },
    ],
  },
  {
    id: "seg_000003",
    speaker: "spk_ben",
    speakerLabel: "Ben Okafor",
    startMs: 13020,
    endMs: 15884,
    words: [
      { id: "seg_000003:w_0", text: "Okay,", startMs: 13020, endMs: 13499 },
      { id: "seg_000003:w_1", text: "so", startMs: 13499, endMs: 13739 },
      { id: "seg_000003:w_2", text: "what", startMs: 13739, endMs: 13899 },
      { id: "seg_000003:w_3", text: "is", startMs: 13899, endMs: 14059 },
      { id: "seg_000003:w_4", text: "actually", startMs: 14059, endMs: 14379 },
      { id: "seg_000003:w_5", text: "left", startMs: 14380, endMs: 14779 },
      { id: "seg_000003:w_6", text: "before", startMs: 14779, endMs: 15099 },
      { id: "seg_000003:w_7", text: "we", startMs: 15099, endMs: 15179 },
      { id: "seg_000003:w_8", text: "can", startMs: 15179, endMs: 15419 },
      { id: "seg_000003:w_9", text: "ship", startMs: 15419, endMs: 15819 },
      { id: "seg_000003:w_10", text: "it?", startMs: 15819, endMs: 15884 },
    ],
  },
  {
    id: "seg_000004",
    speaker: "spk_ana",
    speakerLabel: "Ana Duarte",
    startMs: 18044,
    endMs: 19243,
    words: [
      { id: "seg_000004:w_0", text: "Only", startMs: 18044, endMs: 18363 },
      { id: "seg_000004:w_1", text: "the", startMs: 18363, endMs: 18523 },
      { id: "seg_000004:w_2", text: "change", startMs: 18523, endMs: 18843 },
      { id: "seg_000004:w_3", text: "log.", startMs: 19003, endMs: 19243 },
    ],
  },
  {
    id: "seg_000005",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 23323,
    endMs: 26683,
    words: [
      { id: "seg_000005:w_0", text: "can", startMs: 23323, endMs: 23483 },
      { id: "seg_000005:w_1", text: "take", startMs: 23483, endMs: 23723 },
      { id: "seg_000005:w_2", text: "that", startMs: 23724, endMs: 23963 },
      { id: "seg_000005:w_3", text: "one.", startMs: 23963, endMs: 24203 },
      { id: "seg_000005:w_4", text: "I", startMs: 24363, endMs: 24603 },
      { id: "seg_000005:w_5", text: "will", startMs: 24603, endMs: 24843 },
      { id: "seg_000005:w_6", text: "write", startMs: 24843, endMs: 25083 },
      { id: "seg_000005:w_7", text: "it", startMs: 25083, endMs: 25243 },
      { id: "seg_000005:w_8", text: "this", startMs: 25243, endMs: 25403 },
      { id: "seg_000005:w_9", text: "afternoon", startMs: 25404, endMs: 26043 },
      { id: "seg_000005:w_10", text: "and", startMs: 26043, endMs: 26283 },
      { id: "seg_000005:w_11", text: "post", startMs: 26283, endMs: 26523 },
      { id: "seg_000005:w_12", text: "the", startMs: 26523, endMs: 26683 },
    ],
  },
  {
    id: "seg_000006",
    speaker: "spk_ana",
    speakerLabel: "Ana Duarte",
    startMs: 26635,
    endMs: 27244,
    words: [
      { id: "seg_000006:w_0", text: "Perfect.", startMs: 26635, endMs: 27244 },
    ],
  },
  {
    id: "seg_000007",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 26683,
    endMs: 29356,
    words: [
      { id: "seg_000007:w_0", text: "link", startMs: 26683, endMs: 26923 },
      { id: "seg_000007:w_1", text: "in", startMs: 26923, endMs: 27083 },
      { id: "seg_000007:w_2", text: "the", startMs: 27083, endMs: 27163 },
      { id: "seg_000007:w_3", text: "channel", startMs: 27164, endMs: 27643 },
      { id: "seg_000007:w_4", text: "well", startMs: 27644, endMs: 27884 },
      { id: "seg_000007:w_5", text: "before", startMs: 27883, endMs: 28204 },
      { id: "seg_000007:w_6", text: "the", startMs: 28203, endMs: 28363 },
      { id: "seg_000007:w_7", text: "stand-up", startMs: 28363, endMs: 28763 },
      { id: "seg_000007:w_8", text: "tomorrow.", startMs: 28923, endMs: 29356 },
    ],
  },
  {
    id: "seg_000008",
    speaker: "spk_ben",
    speakerLabel: "Ben Okafor",
    startMs: 29052,
    endMs: 31116,
    words: [
      { id: "seg_000008:w_0", text: "Wait,", startMs: 29052, endMs: 29451 },
      { id: "seg_000008:w_1", text: "does", startMs: 29451, endMs: 29692 },
      { id: "seg_000008:w_2", text: "that", startMs: 29691, endMs: 29851 },
      { id: "seg_000008:w_3", text: "block", startMs: 29851, endMs: 30171 },
      { id: "seg_000008:w_4", text: "the", startMs: 30172, endMs: 30332 },
      { id: "seg_000008:w_5", text: "announcement?", startMs: 30331, endMs: 31116 },
    ],
  },
  {
    id: "seg_000009",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 33532,
    endMs: 35931,
    words: [
      { id: "seg_000009:w_0", text: "No,", startMs: 33532, endMs: 33931 },
      { id: "seg_000009:w_1", text: "the", startMs: 33931, endMs: 34091 },
      { id: "seg_000009:w_2", text: "announcement", startMs: 34092, endMs: 34651 },
      { id: "seg_000009:w_3", text: "goes", startMs: 34652, endMs: 34891 },
      { id: "seg_000009:w_4", text: "out", startMs: 34892, endMs: 35132 },
      { id: "seg_000009:w_5", text: "separately.", startMs: 35131, endMs: 35931 },
    ],
  },
  {
    id: "seg_000010",
    speaker: "spk_ana",
    speakerLabel: "Ana Duarte",
    startMs: 37084,
    endMs: 38124,
    words: [
      { id: "seg_000010:w_0", text: "Then", startMs: 37084, endMs: 37323 },
      { id: "seg_000010:w_1", text: "we", startMs: 37323, endMs: 37483 },
      { id: "seg_000010:w_2", text: "are", startMs: 37483, endMs: 37563 },
      { id: "seg_000010:w_3", text: "done", startMs: 37563, endMs: 38043 },
      { id: "seg_000010:w_4", text: "here.", startMs: 38043, endMs: 38124 },
    ],
  },
  {
    id: "seg_000011",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 40811,
    endMs: 43131,
    words: [
      { id: "seg_000011:w_0", text: "So", startMs: 40811, endMs: 41051 },
      { id: "seg_000011:w_1", text: "the", startMs: 41131, endMs: 41371 },
      { id: "seg_000011:w_2", text: "only", startMs: 41371, endMs: 41531 },
      { id: "seg_000011:w_3", text: "thing", startMs: 41532, endMs: 41851 },
      { id: "seg_000011:w_4", text: "I", startMs: 41851, endMs: 42011 },
      { id: "seg_000011:w_5", text: "still", startMs: 42011, endMs: 42331 },
      { id: "seg_000011:w_6", text: "need", startMs: 42332, endMs: 42492 },
      { id: "seg_000011:w_7", text: "from", startMs: 42492, endMs: 42732 },
      { id: "seg_000011:w_8", text: "you", startMs: 42731, endMs: 42891 },
      { id: "seg_000011:w_9", text: "is", startMs: 42971, endMs: 43131 },
    ],
  },
  {
    id: "seg_000012",
    speaker: "spk_ben",
    speakerLabel: "Ben Okafor",
    startMs: 43260,
    endMs: 43579,
    words: [
      { id: "seg_000012:w_0", text: "Right,", startMs: 43260, endMs: 43579 },
    ],
  },
  {
    id: "seg_000013",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 43372,
    endMs: 43671,
    words: [
      { id: "seg_000013:w_0", text: "f", startMs: 43372, endMs: 43532 },
      { id: "seg_000013:w_1", text: "the", startMs: 43431, endMs: 43671 },
    ],
  },
  {
    id: "seg_000014",
    speaker: "spk_ben",
    speakerLabel: "Ben Okafor",
    startMs: 43579,
    endMs: 43739,
    words: [
      { id: "seg_000014:w_0", text: "but", startMs: 43579, endMs: 43739 },
    ],
  },
  {
    id: "seg_000015",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 43671,
    endMs: 43991,
    words: [
      { id: "seg_000015:w_0", text: "final", startMs: 43671, endMs: 43991 },
    ],
  },
  {
    id: "seg_000016",
    speaker: "spk_ben",
    speakerLabel: "Ben Okafor",
    startMs: 43739,
    endMs: 44059,
    words: [
      { id: "seg_000016:w_0", text: "hold", startMs: 43739, endMs: 44059 },
    ],
  },
  {
    id: "seg_000017",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 43991,
    endMs: 44311,
    words: [
      { id: "seg_000017:w_0", text: "sign", startMs: 43991, endMs: 44311 },
    ],
  },
  {
    id: "seg_000018",
    speaker: "spk_ben",
    speakerLabel: "Ben Okafor",
    startMs: 44059,
    endMs: 44379,
    words: [
      { id: "seg_000018:w_0", text: "on.", startMs: 44059, endMs: 44299 },
      { id: "seg_000018:w_1", text: "I", startMs: 44299, endMs: 44379 },
    ],
  },
  {
    id: "seg_000019",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 44311,
    endMs: 44551,
    words: [
      { id: "seg_000019:w_0", text: "off", startMs: 44311, endMs: 44551 },
    ],
  },
  {
    id: "seg_000020",
    speaker: "spk_ben",
    speakerLabel: "Ben Okafor",
    startMs: 44380,
    endMs: 44619,
    words: [
      { id: "seg_000020:w_0", text: "thought", startMs: 44380, endMs: 44619 },
    ],
  },
  {
    id: "seg_000021",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 44551,
    endMs: 44711,
    words: [
      { id: "seg_000021:w_0", text: "on", startMs: 44551, endMs: 44711 },
    ],
  },
  {
    id: "seg_000022",
    speaker: "spk_ben",
    speakerLabel: "Ben Okafor",
    startMs: 44620,
    endMs: 44780,
    words: [
      { id: "seg_000022:w_0", text: "we", startMs: 44620, endMs: 44780 },
    ],
  },
  {
    id: "seg_000023",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 44711,
    endMs: 44871,
    words: [
      { id: "seg_000023:w_0", text: "the", startMs: 44711, endMs: 44871 },
    ],
  },
  {
    id: "seg_000024",
    speaker: "spk_ben",
    speakerLabel: "Ben Okafor",
    startMs: 44779,
    endMs: 45179,
    words: [
      { id: "seg_000024:w_0", text: "agreed", startMs: 44779, endMs: 45179 },
    ],
  },
  {
    id: "seg_000025",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 44871,
    endMs: 45431,
    words: [
      { id: "seg_000025:w_0", text: "wording,", startMs: 44871, endMs: 45431 },
    ],
  },
  {
    id: "seg_000026",
    speaker: "spk_ben",
    speakerLabel: "Ben Okafor",
    startMs: 45179,
    endMs: 45739,
    words: [
      { id: "seg_000026:w_0", text: "the", startMs: 45179, endMs: 45419 },
      { id: "seg_000026:w_1", text: "wording", startMs: 45419, endMs: 45739 },
    ],
  },
  {
    id: "seg_000027",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 45591,
    endMs: 45911,
    words: [
      { id: "seg_000027:w_0", text: "because", startMs: 45591, endMs: 45911 },
    ],
  },
  {
    id: "seg_000028",
    speaker: "spk_ben",
    speakerLabel: "Ben Okafor",
    startMs: 45740,
    endMs: 46219,
    words: [
      { id: "seg_000028:w_0", text: "was", startMs: 45740, endMs: 45900 },
      { id: "seg_000028:w_1", text: "already", startMs: 45899, endMs: 46219 },
    ],
  },
  {
    id: "seg_000029",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 45911,
    endMs: 46311,
    words: [
      { id: "seg_000029:w_0", text: "once", startMs: 45911, endMs: 46151 },
      { id: "seg_000029:w_1", text: "it", startMs: 46151, endMs: 46311 },
    ],
  },
  {
    id: "seg_000030",
    speaker: "spk_ben",
    speakerLabel: "Ben Okafor",
    startMs: 46220,
    endMs: 46699,
    words: [
      { id: "seg_000030:w_0", text: "settled", startMs: 46220, endMs: 46699 },
    ],
  },
  {
    id: "seg_000031",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 46311,
    endMs: 46751,
    words: [
      { id: "seg_000031:w_0", text: "goes", startMs: 46311, endMs: 46551 },
      { id: "seg_000031:w_1", text: "out,", startMs: 46551, endMs: 46751 },
    ],
  },
  {
    id: "seg_000032",
    speaker: "spk_ben",
    speakerLabel: "Ben Okafor",
    startMs: 46699,
    endMs: 46939,
    words: [
      { id: "seg_000032:w_0", text: "last", startMs: 46699, endMs: 46939 },
    ],
  },
  {
    id: "seg_000033",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 46791,
    endMs: 46951,
    words: [
      { id: "seg_000033:w_0", text: "we", startMs: 46791, endMs: 46951 },
    ],
  },
  {
    id: "seg_000034",
    speaker: "spk_ben",
    speakerLabel: "Ben Okafor",
    startMs: 46939,
    endMs: 47259,
    words: [
      { id: "seg_000034:w_0", text: "week", startMs: 46939, endMs: 47259 },
    ],
  },
  {
    id: "seg_000035",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 46951,
    endMs: 47831,
    words: [
      { id: "seg_000035:w_0", text: "cannot", startMs: 46951, endMs: 47351 },
      { id: "seg_000035:w_1", text: "quietly", startMs: 47351, endMs: 47831 },
    ],
  },
  {
    id: "seg_000036",
    speaker: "spk_ben",
    speakerLabel: "Ben Okafor",
    startMs: 47419,
    endMs: 47979,
    words: [
      { id: "seg_000036:w_0", text: "when", startMs: 47419, endMs: 47579 },
      { id: "seg_000036:w_1", text: "we", startMs: 47579, endMs: 47739 },
      { id: "seg_000036:w_2", text: "went", startMs: 47740, endMs: 47979 },
    ],
  },
  {
    id: "seg_000037",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 47831,
    endMs: 48152,
    words: [
      { id: "seg_000037:w_0", text: "edit", startMs: 47831, endMs: 48152 },
    ],
  },
  {
    id: "seg_000038",
    speaker: "spk_ben",
    speakerLabel: "Ben Okafor",
    startMs: 47979,
    endMs: 48300,
    words: [
      { id: "seg_000038:w_0", text: "through", startMs: 47979, endMs: 48300 },
    ],
  },
  {
    id: "seg_000039",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 48151,
    endMs: 48391,
    words: [
      { id: "seg_000039:w_0", text: "it", startMs: 48151, endMs: 48391 },
    ],
  },
  {
    id: "seg_000040",
    speaker: "spk_ben",
    speakerLabel: "Ben Okafor",
    startMs: 48299,
    endMs: 48859,
    words: [
      { id: "seg_000040:w_0", text: "it.", startMs: 48299, endMs: 48859 },
    ],
  },
  {
    id: "seg_000041",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 48391,
    endMs: 49031,
    words: [
      { id: "seg_000041:w_0", text: "afterwards.", startMs: 48391, endMs: 49031 },
    ],
  },
  {
    id: "seg_000042",
    speaker: "spk_ana",
    speakerLabel: "Ana Duarte",
    startMs: 52572,
    endMs: 53836,
    words: [
      { id: "seg_000042:w_0", text: "One", startMs: 52572, endMs: 52811 },
      { id: "seg_000042:w_1", text: "at", startMs: 52811, endMs: 52971 },
      { id: "seg_000042:w_2", text: "a", startMs: 52971, endMs: 53131 },
      { id: "seg_000042:w_3", text: "time,", startMs: 53132, endMs: 53451 },
      { id: "seg_000042:w_4", text: "please.", startMs: 53451, endMs: 53836 },
    ],
  },
  {
    id: "seg_000043",
    speaker: "spk_cara",
    speakerLabel: "Cara Lindqvist",
    startMs: 55004,
    endMs: 58443,
    words: [
      { id: "seg_000043:w_0", text: "Sorry,", startMs: 55004, endMs: 55533 },
      { id: "seg_000043:w_1", text: "yes,", startMs: 55564, endMs: 55953 },
      { id: "seg_000043:w_2", text: "it", startMs: 55963, endMs: 56123 },
      { id: "seg_000043:w_3", text: "is", startMs: 56124, endMs: 56284 },
      { id: "seg_000043:w_4", text: "settled.", startMs: 56283, endMs: 56763 },
      { id: "seg_000043:w_5", text: "I", startMs: 56923, endMs: 57083 },
      { id: "seg_000043:w_6", text: "just", startMs: 57083, endMs: 57243 },
      { id: "seg_000043:w_7", text: "want", startMs: 57244, endMs: 57404 },
      { id: "seg_000043:w_8", text: "it", startMs: 57403, endMs: 57563 },
      { id: "seg_000043:w_9", text: "in", startMs: 57563, endMs: 57803 },
      { id: "seg_000043:w_10", text: "writing.", startMs: 57803, endMs: 58443 },
    ],
  },
];
