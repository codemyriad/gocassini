#!/usr/bin/env python3
"""Audit replay coverage and timestamp/repetition diagnostics, not ASR accuracy."""
import argparse
from collections import Counter, defaultdict
import json
import os
from pathlib import Path
import re


def normalized(word):
    return re.sub(r'[^\w\']', '', word.casefold())


def word_stats(words, duration):
    tokens = [normalized(w.get('Text', w.get('text', ''))) for w in words]
    adjacent = sum(bool(a) and a == b for a, b in zip(tokens, tokens[1:]))
    run = longest = 0
    previous = None
    errors = Counter()
    last_start = -1
    for word, token in zip(words, tokens):
        run = run + 1 if token and token == previous else 1
        longest = max(longest, run)
        previous = token
        start, end = word.get('StartMS', word.get('startMs')), word.get('EndMS', word.get('endMs'))
        if not isinstance(start, (int, float)) or not isinstance(end, (int, float)):
            errors['missingTimestamp'] += 1
            continue
        if start < 0 or end < 0: errors['negativeTimestamp'] += 1
        if end < start: errors['reversedExtent'] += 1
        if start < last_start: errors['nonmonotonicStart'] += 1
        if start > duration or end > duration + 1: errors['pastDecodedAudio'] += 1
        last_start = start
    return {'words': len(words), 'adjacentRepeatPairs': adjacent, 'longestSameWordRun': longest,
            'timestampErrors': dict(errors)}


def audit(root, published=None):
    meetings = []
    states = Counter()
    for status_path in sorted(root.glob('*/status.json')):
        status = json.loads(status_path.read_text())
        states[status['state']] += 1
        if status['state'] != 'complete': continue
        folder = status_path.parent
        rows = [json.loads(line) for line in (folder / 'results.jsonl').read_text().splitlines()]
        probe = json.loads((folder / 'probe.json').read_text())
        streams = {s['index']: s for s in probe['streams'] if s['codec_type'] == 'audio'}
        meeting = {'id': status['id'], 'expectedDurationMs': status['expectedDurationMs'],
                   'expectedTracks': status['expectedTracks'], 'completedTracks': len(rows), 'tracks': []}
        new_by_label = defaultdict(list)
        for row in rows:
            index = int(row['fixture'].rsplit('/stream-', 1)[1])
            tags = streams.get(index, {}).get('tags', {})
            label = tags.get('PARTICIPANT_NAME', tags.get('title', str(index)))
            duration = row['samples'] / 16
            words = row.get('words') or []
            new_by_label[label.casefold()].extend(words)
            track = {'streamIndex': index, 'label': label, 'decodedDurationMs': duration,
                     'pcmSha256': row['pcmSha256'], **word_stats(words, duration)}
            meeting['tracks'].append(track)
        maximum = max((r['samples'] / 16 for r in rows), default=0)
        meeting['maximumDecodedDurationMs'] = maximum
        meeting['sourceCoverageRatio'] = maximum / status['expectedDurationMs']
        meeting['possibleSourceTruncation'] = maximum + 1000 < status['expectedDurationMs'] * .98
        meeting['emptyTracks'] = sum(t['words'] == 0 for t in meeting['tracks'])
        meeting['words'] = sum(t['words'] for t in meeting['tracks'])
        if published:
            path = published / (status['id'] + '.meeting') / 'transcript.words.v1.json'
            if path.exists():
                old = json.loads(path.read_text())
                labels = {s['id']: s['label'].casefold() for s in old['speakers']}
                old_by_label = defaultdict(list)
                for segment in old['segments']:
                    old_by_label[labels.get(segment['speaker'], segment['speaker'])].extend(segment.get('words', []))
                meeting['publishedWords'] = sum(map(len, old_by_label.values()))
                meeting['wordCountDelta'] = meeting['words'] - meeting['publishedWords']
                comparisons = []
                published_labels = set(labels.values())
                replay_labels = set(new_by_label)
                for label in sorted(set(old_by_label) | replay_labels):
                    old_words, new_words = old_by_label[label], new_by_label[label]
                    bins = defaultdict(lambda: [0, 0])
                    for side, words in enumerate((old_words, new_words)):
                        for w in words:
                            start = w.get('StartMS', w.get('startMs', 0))
                            bins[int(start // 30000) * 30000][side] += 1
                    windows = [{'startMs': start, 'endMs': start + 30000, 'publishedWords': counts[0],
                                'replayWords': counts[1], 'delta': counts[1] - counts[0]} for start, counts in bins.items()]
                    comparisons.append({'label': label, 'labelMatched': label in published_labels and label in replay_labels, 'published': word_stats(old_words, status['expectedDurationMs']),
                                        'replay': word_stats(sorted(new_words, key=lambda w: w['StartMS']), status['expectedDurationMs']),
                                        'largestCountChanges': sorted(windows, key=lambda w: abs(w['delta']), reverse=True)[:8]})
                meeting['publishedComparison'] = comparisons
        meetings.append(meeting)
    return {'caution': 'Coverage, repetition and word-count diagnostics are not accuracy. Published output includes attribution/merging stages absent from isolated-track replay; audit differences against audio.',
            'states': dict(states), 'completedMeetings': len(meetings),
            'completedTracks': sum(m['completedTracks'] for m in meetings),
            'possibleSourceTruncations': sum(m['possibleSourceTruncation'] for m in meetings),
            'timestampErrors': dict(sum((Counter(t['timestampErrors']) for m in meetings for t in m['tracks']), Counter())),
            'meetings': meetings}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output-dir', type=Path, required=True, help='replay output directory')
    parser.add_argument('--published-root', type=Path)
    parser.add_argument('--output', type=Path, required=True, help='private audit JSON')
    args = parser.parse_args()
    os.umask(0o077)
    result = audit(args.output_dir, args.published_root)
    with open(args.output, 'w') as f: json.dump(result, f, indent=2)
    os.chmod(args.output, 0o600)
    print(json.dumps({k: v for k, v in result.items() if k != 'meetings'}))


if __name__ == '__main__':
    main()
