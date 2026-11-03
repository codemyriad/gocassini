"""Regenerate chunk_cases.json: synthetic transcripts and the passages the
natural-passage rules give them (docs/proposals/automatic-tagging, "Chunking").

passages() below is written from those rules, in Python, as an independent
oracle: the Go chunker must give exactly these passages. Python's str.strip
and re's `$` are the reference semantics the Go port reproduces. Words are
synthetic; never add meeting text: the repository is public.

  python -I chunks.py > ../chunk_cases.json
"""
import json
import random
import re

SENTENCE_END = re.compile(r'[.!?]["\'’”]*$')


def closing_rule(run_length, word, following):
    """The first rule that closes a passage after `word`, or None."""
    if following is None:
        return 'end'
    if run_length >= 25 and following['startMs'] - word['endMs'] >= 2000:
        return 'pause'
    if run_length >= 25 and following['speaker'] != word['speaker']:
        return 'speaker change'
    if run_length >= 40 and SENTENCE_END.search(word['text']):
        return 'sentence end'
    if run_length >= 120:
        return 'word safety cap'
    return None


def passages(words):
    order = [i for i, w in enumerate(words) if w['text'].strip()]
    order.sort(key=lambda i: (words[i]['startMs'], words[i]['endMs'], i))
    out, start = [], 0
    for k, i in enumerate(order):
        following = words[order[k + 1]] if k + 1 < len(order) else None
        rule = closing_rule(k + 1 - start, words[i], following)
        if rule is None:
            continue
        run = [words[j] for j in order[start:k + 1]]
        out.append(dict(startMs=min(w['startMs'] for w in run), endMs=max(w['endMs'] for w in run),
                        text=' '.join(w['text'].strip() for w in run), wordCount=len(run), boundary=rule))
        start = k + 1
    return out


VOCAB = ['plan', 'release', 'tickets', 'schedule', 'review', 'piano', 'riunione', 'Woche',
         'café', 'naïve', '週次', '計画', 'the', 'and', 'ok', 'yes', '42', '3.5', 'e-mail']
ENDINGS = ['', '', '', '', '.', '?', '!', '."', ".'", '.’', '?”', '...', '!!', ',', ';']


def synthetic(seed, n):
    rng = random.Random(seed)
    words, t = [], 0
    speakers = ['spk_a', 'spk_b', 'spk_c']
    speaker = speakers[0]
    for _ in range(n):
        if rng.random() < 0.03:
            speaker = rng.choice(speakers)
        gap = rng.choice([0, 40, 120, 300, 900, 1999, 2000, 2600]) if rng.random() < 0.2 else rng.randint(0, 80)
        length = rng.randint(80, 600)
        if rng.random() < 0.02:
            gap = -length  # overlapping speech: starts before the previous word ends
        start = max(0, t + gap)
        text = rng.choice(VOCAB) + rng.choice(ENDINGS)
        r = rng.random()
        if r < 0.02:
            text = ''
        elif r < 0.04:
            text = '  '
        elif r < 0.06:
            text = ' ' + text + '\t'
        words.append(dict(text=text, startMs=start, endMs=start + length, speaker=speaker))
        t = start + length
    if n > 5:
        # Two words with the same times keep their transcript order.
        words[3] = {**words[3], 'startMs': words[2]['startMs'], 'endMs': words[2]['endMs']}
    return words


def fixed():
    def run(n, text='word', speaker='a', start=0):
        return [dict(text=text, startMs=start + i * 100, endMs=start + i * 100 + 90, speaker=speaker) for i in range(n)]
    sentence = run(80)
    sentence[44] = {**sentence[44], 'text': 'end.'}
    pause = run(30) + run(30, start=3000 + 30 * 100)
    speakers = run(30) + run(30, speaker='b', start=30 * 100)
    return [('prototype: sentence end preferred', sentence),
            ('prototype: long unpunctuated speech', run(250)),
            ('pause after 25 words', pause),
            ('speaker change after 25 words', speakers),
            ('short meeting', run(7)),
            ('empty', [])]


def case(name, words):
    # Words as [text, startMs, endMs, speaker], to keep the file small.
    return dict(name=name, words=[[w['text'], w['startMs'], w['endMs'], w['speaker']] for w in words],
                passages=passages(words))


cases = [case(name, words) for name, words in fixed()]
cases += [case(f'synthetic seed {seed}', synthetic(seed, 300 + seed * 41)) for seed in range(8)]
print(json.dumps(cases, ensure_ascii=False, separators=(',', ':')))
