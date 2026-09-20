PYTHON ?= python3
MANIFEST ?= fixtures/manifest.v1.json
AUDIO_DIR ?= fixtures/audio
RESULTS_DIR ?= results
SOURCE_AUDIO_DIR ?= /tmp/cassini-vast-bench-20260828/audio
VOXTRAL_OFFLINE_ARGS ?=
VOXTRAL_REALTIME_ARGS ?=
PARAKEET_ARGS ?=
GROUND_TRUTH ?=

.PHONY: help fixtures verify test syntax bootstrap-offline bootstrap-realtime \
	run-voxtral-offline run-voxtral-realtime run-parakeet score

help:
	@sed -n '/^## Targets:/,/^$$/p' README.md

fixtures:
	$(PYTHON) scripts/stage_fixtures.py --manifest $(MANIFEST) \
		--source-dir $(SOURCE_AUDIO_DIR) --audio-dir $(AUDIO_DIR)

verify:
	$(PYTHON) scripts/verify_fixtures.py --manifest $(MANIFEST) --audio-dir $(AUDIO_DIR)

test:
	PYTHONPATH=scripts $(PYTHON) -m unittest discover -s scripts -p 'test_*.py' -v
	PYTHONPATH=runners:scripts $(PYTHON) -m unittest discover -s runners -p 'test_*.py' -v
	PYTHONPATH=vast:scripts $(PYTHON) -m unittest discover -s vast -p 'test_*.py' -v

syntax:
	$(PYTHON) -m py_compile scripts/*.py runners/*.py vast/*.py
	bash -n scripts/*.sh runners/*.sh

bootstrap-offline:
	./scripts/bootstrap_voxtral.sh offline

bootstrap-realtime:
	./scripts/bootstrap_voxtral.sh realtime

run-voxtral-offline: verify
	CUDA_VISIBLE_DEVICES=$${CUDA_VISIBLE_DEVICES:-0} .venv-voxtral-offline/bin/python \
		runners/voxtral_offline.py --manifest $(MANIFEST) --audio-dir $(AUDIO_DIR) \
		--output $(RESULTS_DIR)/voxtral-offline.json $(VOXTRAL_OFFLINE_ARGS)

run-voxtral-realtime: verify
	CUDA_VISIBLE_DEVICES=$${CUDA_VISIBLE_DEVICES:-0} .venv-voxtral-realtime/bin/python \
		runners/voxtral_realtime.py --manifest $(MANIFEST) --audio-dir $(AUDIO_DIR) \
		--output $(RESULTS_DIR)/voxtral-realtime.json $(VOXTRAL_REALTIME_ARGS)

run-parakeet: verify
	./runners/parakeet_hotword.sh --manifest $(MANIFEST) --audio-dir $(AUDIO_DIR) \
		--output-dir $(RESULTS_DIR)/parakeet-hotword $(PARAKEET_ARGS)

score:
	@args=""; \
		if [ -n "$(GROUND_TRUTH)" ]; then args="--ground-truth $(GROUND_TRUTH)"; fi; \
		$(PYTHON) scripts/score_results.py --manifest $(MANIFEST) \
			--result-dir "$(RESULTS_DIR)" $$args --output "$(RESULTS_DIR)/score.json" \
			--fail-on-forbidden --fail-on-false-speech
