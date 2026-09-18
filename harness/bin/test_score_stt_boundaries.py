#!/usr/bin/env python3
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('boundary_score', Path(__file__).with_name('score-stt-boundaries.py'))
scorer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(scorer)


def row(fixture, text, condition='candidate'):
    return {'fixture': fixture, 'condition': {'id': condition},
            'words': [{'Text': word, 'StartMS': i * 100, 'EndMS': (i + 1) * 100} for i, word in enumerate(text.split())]}


class ScoringTests(unittest.TestCase):
    def test_substitution_deletion_insertion(self):
        for reference, hypothesis, expected in [('a b', 'a c', (1, 0, 0)), ('a b', 'a', (0, 1, 0)), ('a', 'a b', (0, 0, 1))]:
            with self.subTest(reference=reference, hypothesis=hypothesis):
                result = scorer.score_row(row('f', hypothesis), {'referenceText': reference})
                self.assertEqual(tuple(result[k] for k in ['substitutions', 'deletions', 'insertions']), expected)
                self.assertEqual(result['wer'], 1 / len(reference.split()))

    def test_empty_reference_and_missing_reference_are_distinct(self):
        corpus = [{'id': 'control', 'referenceText': '', 'directSpeech': False}, {'id': 'unknown'}]
        report = scorer.score(corpus, [row('control', 'thank you'), row('unknown', 'hello')])
        control, unknown = report['selectionRows']
        self.assertEqual(control['insertions'], 2)
        self.assertIsNone(control['wer'])
        self.assertFalse(unknown['hasReference'])
        total = report['selectionAggregates']['candidate']
        self.assertEqual(total['falseSpeechTokens'], 2)
        self.assertEqual(total['emptyReferenceInsertions'], 2)
        self.assertIsNone(total['wer'])

    def test_holdout_cannot_change_selection(self):
        corpus = [{'id': 'train', 'referenceText': 'correct'}, {'id': 'hold', 'referenceText': 'a b c'}]
        first = scorer.score(corpus, [row('train', 'correct'), row('hold', 'a b c')], ['hold'])
        second = scorer.score(corpus, [row('train', 'correct'), row('hold', 'bad')], ['hold'])
        self.assertEqual(first['selectionAggregates'], second['selectionAggregates'])
        self.assertEqual(first['selectionRows'], second['selectionRows'])
        self.assertEqual(first['selectionAggregates']['candidate']['referenceTokens'], 1)
        self.assertNotEqual(first['heldoutRows'], second['heldoutRows'])

    def test_reference_repetitions_preserved_and_crop_applied(self):
        result = scorer.score_row(row('f', 'outside my my my plan'),
                                  {'referenceText': 'my my plan', 'scoreStartMs': 100, 'scoreEndMs': 500})
        self.assertEqual(result['insertedTokens'], ['my'])
        self.assertEqual(result['excessAdjacentRepetitions'], {'my': 1})
        self.assertEqual(result['exactRecall'], 1)
        self.assertEqual(scorer.tokens('Café, CAN’T!'), ['café', "can't"])


if __name__ == '__main__':
    unittest.main()
