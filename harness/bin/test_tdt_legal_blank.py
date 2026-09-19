#!/usr/bin/env python3
"""Synthetic diagnostic of sherpa-onnx v1.13.7 TDT blank-duration scoring.

A one-frame, two-token model suffices; no speech, neural runtime or weights.
This demonstrates an algorithmic scoring mismatch, not the cause of any given
recording failure. It models this version's top-token/argmax-duration expansion,
mixed-frame pruning, and final raw-score selection with beam width two.
"""
from dataclasses import dataclass
import math
import unittest


@dataclass
class Hypothesis:
    text: str = ''
    frame: int = 0
    score: float = 0.0


def probabilities(text):
    # Separate normalized token and duration distributions, as in TDT.
    if not text:
        return {'A': 0.6, 'blank': 0.4}, {0: 0.9, 1: 0.1}
    return {'A': 0.1, 'blank': 0.9}, {0: 0.4, 1: 0.6}


def beam_decode(legal_blank):
    active = [Hypothesis()]
    trace = []
    for _ in range(20):
        if all(h.frame >= 1 for h in active):
            return max(active, key=lambda h: h.score), trace
        current_frame = min(h.frame for h in active)
        candidates = []
        for h in active:
            if h.frame > current_frame:
                candidates.append(h)
                continue
            token_probs, duration_probs = probabilities(h.text)
            predicted_duration = max(duration_probs, key=duration_probs.get)
            for token, token_probability in token_probs.items():
                scored_duration = predicted_duration
                if token == 'blank' and legal_blank:
                    scored_duration = max((d for d in duration_probs if d > 0), key=duration_probs.get)
                # The original guard consumes one frame but scores duration zero.
                actual_duration = max(1, scored_duration) if token == 'blank' else scored_duration
                candidate = Hypothesis(h.text + (token if token != 'blank' else ''),
                                       h.frame + actual_duration,
                                       h.score + math.log(token_probability) + math.log(duration_probs[scored_duration]))
                candidates.append(candidate)
                trace.append({'prefix': h.text, 'token': token, 'scoredDuration': scored_duration,
                              'actualDuration': actual_duration, 'pathProbability': math.exp(candidate.score)})
        active = sorted(candidates, key=lambda h: h.score, reverse=True)[:2]
    raise AssertionError('Synthetic search failed to terminate')


def greedy_decode():
    h = Hypothesis()
    for _ in range(20):
        if h.frame >= 1:
            return h.text
        token_probs, duration_probs = probabilities(h.text)
        token = max(token_probs, key=token_probs.get)
        duration = max(duration_probs, key=duration_probs.get)
        h.frame += max(1, duration) if token == 'blank' else duration
        if token != 'blank':
            h.text += token
    raise AssertionError('Synthetic greedy failed to terminate')


class LegalBlankTests(unittest.TestCase):
    def test_blank_guard_scores_a_different_transition(self):
        original, trace = beam_decode(False)
        corrected, legal_trace = beam_decode(True)
        self.assertEqual(original.text, '')
        self.assertAlmostEqual(math.exp(original.score), 0.4 * 0.9)
        self.assertEqual(corrected.text, 'A')
        self.assertAlmostEqual(math.exp(corrected.score), 0.6 * 0.9 * 0.9 * 0.6)
        self.assertEqual(greedy_decode(), 'A')
        self.assertTrue(any(t['token'] == 'blank' and t['scoredDuration'] != t['actualDuration'] for t in trace))
        self.assertTrue(all(t['scoredDuration'] == t['actualDuration'] for t in legal_trace))
        # Legitimate empty-path probability is .4*.1=.04, not .36.
        self.assertGreater(math.exp(corrected.score), 0.4 * 0.1)


if __name__ == '__main__':
    unittest.main()
