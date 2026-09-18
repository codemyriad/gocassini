#!/usr/bin/env python3
"""Compare ASR boundary experiments with supplied references (stdlib only).

Model-generated references measure model-reference disagreement, not verified
accuracy. Punctuation and case are normalized; disfluencies are retained.
Held-out rows are reported separately and excluded from selection aggregates.
"""
import argparse
from collections import Counter, defaultdict
import json
from pathlib import Path
import re


def tokens(text):
    return re.findall(r"[^\W_]+(?:'[^\W_]+)*", text.casefold().replace('’', "'"), re.UNICODE)


def align(reference, hypothesis):
    """Unit-cost Levenshtein; ties prefer diagonal, deletion, insertion."""
    d = [[0] * (len(hypothesis) + 1) for _ in range(len(reference) + 1)]
    for i in range(len(reference) + 1):
        d[i][0] = i
    for j in range(len(hypothesis) + 1):
        d[0][j] = j
    for i, a in enumerate(reference, 1):
        for j, b in enumerate(hypothesis, 1):
            d[i][j] = min(d[i-1][j-1] + (a != b), d[i-1][j] + 1, d[i][j-1] + 1)
    i, j = len(reference), len(hypothesis)
    operations = []
    while i or j:
        if i and j and d[i][j] == d[i-1][j-1] + (reference[i-1] != hypothesis[j-1]):
            i -= 1
            j -= 1
            operations.append(('match' if reference[i] == hypothesis[j] else 'substitution', reference[i], hypothesis[j]))
        elif i and d[i][j] == d[i-1][j] + 1:
            i -= 1
            operations.append(('deletion', reference[i], None))
        else:
            j -= 1
            operations.append(('insertion', None, hypothesis[j]))
    return list(reversed(operations))


def repetitions(sequence):
    """Adjacent repeated 1–4-grams: diagnostics, not necessarily ASR errors."""
    return Counter(' '.join(sequence[i:i+n]) for n in range(1, 5)
                   for i in range(len(sequence)-2*n+1)
                   if sequence[i:i+n] == sequence[i+n:i+2*n])


def score_row(row, fixture):
    condition = row['condition']
    condition = condition['id'] if isinstance(condition, dict) else condition
    words = row.get('words') or []
    start, end = fixture.get('scoreStartMs', float('-inf')), fixture.get('scoreEndMs', float('inf'))
    words = [w for w in words if start <= w['StartMS'] < end]
    text = ' '.join(w['Text'] for w in words)
    hypothesis = tokens(text)
    out = {'fixture': row['fixture'], 'condition': condition, 'hypothesis': text,
           'hypothesisTokens': len(hypothesis), 'referenceKind': fixture.get('referenceKind', fixture.get('refKind')),
           'hasReference': 'referenceText' in fixture, 'directSpeech': fixture.get('directSpeech'),
           'seconds': row.get('seconds')}
    if fixture.get('directSpeech') is False:
        out['falseSpeechTokens'] = len(hypothesis)
    if 'referenceText' not in fixture:
        return out
    reference = tokens(fixture['referenceText'])
    operations = align(reference, hypothesis)
    counts = Counter(op for op, _, _ in operations)
    errors = sum(counts[k] for k in ['substitution', 'deletion', 'insertion'])
    out.update(referenceTokens=len(reference), substitutions=counts['substitution'], deletions=counts['deletion'],
               insertions=counts['insertion'], matches=counts['match'], errors=errors,
               wer=errors / len(reference) if reference else None,
               exactRecall=counts['match'] / len(reference) if reference else None,
               lexicalRecall=sum((Counter(reference) & Counter(hypothesis)).values()) / len(reference) if reference else None,
               insertedTokens=[h for op, _, h in operations if op == 'insertion'],
               deletedTokens=[r for op, r, _ in operations if op == 'deletion'],
               excessAdjacentRepetitions=dict(repetitions(hypothesis) - repetitions(reference)))
    return out


def aggregate(rows):
    # Zero-reference insertions cannot have a WER denominator. Report separately.
    speech = [r for r in rows if r.get('referenceTokens', 0) > 0 and r.get('directSpeech') is not False]
    totals = {key: sum(r[key] for r in speech) for key in
              ['referenceTokens', 'substitutions', 'deletions', 'insertions', 'matches', 'errors']}
    n = totals['referenceTokens']
    totals.update(wer=totals['errors'] / n if n else None, exactRecall=totals['matches'] / n if n else None,
                  fixtures=len(rows), scoredSpeechFixtures=len(speech), fixtureIds=sorted(r['fixture'] for r in rows),
                  falseSpeechTokens=sum(r.get('falseSpeechTokens', 0) for r in rows),
                  emptyReferenceInsertions=sum(r.get('insertions', 0) for r in rows if r.get('referenceTokens') == 0),
                  unreferencedFixtures=sum(not r['hasReference'] for r in rows))
    return totals


def score(corpus, results, holdout=()):
    fixtures = {f['id']: f for f in corpus}
    if len(fixtures) != len(corpus):
        raise ValueError('Duplicate corpus fixture IDs')
    holdout = set(holdout)
    if holdout - fixtures.keys():
        raise ValueError('Unknown holdout IDs: ' + ', '.join(sorted(holdout - fixtures.keys())))
    selection, heldout, seen = [], [], set()
    for row in results:
        if 'error' in row:
            raise ValueError(f"Failed inference row: {row['fixture']}: {row['error']}")
        out = score_row(row, fixtures[row['fixture']])
        key = out['fixture'], out['condition']
        if key in seen:
            raise ValueError(f'Duplicate result: {key}')
        seen.add(key)
        (heldout if row['fixture'] in holdout else selection).append(out)
    groups = defaultdict(list)
    for row in selection:
        groups[row['condition']].append(row)
    aggregates = {condition: aggregate(rows) for condition, rows in sorted(groups.items())}
    expected = set(fixtures) - holdout
    for result in aggregates.values():
        result['missingFixtures'] = sorted(expected - set(result['fixtureIds']))
        result['complete'] = not result['missingFixtures']
    return {'metric': 'Normalized model-reference disagreement (WER formula); not verified human accuracy unless references were human-verified.',
            'normalization': 'Unicode casefold; punctuation split; apostrophes within words retained; repetitions and fillers retained.',
            'recall': 'exactRecall is alignment matches/reference tokens; lexicalRecall is unordered multiset token coverage.',
            'duplicates': 'excessAdjacentRepetitions counts adjacent repeated 1–4-grams beyond the reference; legitimate speech repetitions may differ.',
            'holdoutIds': sorted(holdout), 'selectionAggregates': aggregates,
            'selectionRows': selection, 'heldoutRows': heldout}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--corpus', required=True, type=Path)
    parser.add_argument('--results', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--holdout', nargs='+', action='extend', default=[], metavar='FIXTURE_ID')
    args = parser.parse_args()
    corpus = json.loads(args.corpus.read_text())
    results = [json.loads(line) for line in args.results.read_text().splitlines() if line.strip()]
    report = score(corpus, results, args.holdout)
    args.output.write_text(json.dumps(report, indent=2, ensure_ascii=False) + '\n')


if __name__ == '__main__':
    main()
