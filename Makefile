.PHONY: help setup install build run-core run-ai docker-up docker-down health services-install gateway-install sip-check-vm test test-unit test-python lint check scenarios scenarios-voice zer0days zer0days-voice sim sim-live sim-live-1h voice smoke logs transcripts retention-timer clean

help:
	@echo "setup install build docker-up run-core run-ai health services-install gateway-install sip-check-vm test lint check scenarios scenarios-voice zer0days zer0days-voice sim sim-live voice smoke logs transcripts retention-timer"

setup:
	python3 scripts/setup.py

health:
	python3 scripts/check_health.py

services-install:
	sudo python3 scripts/install_services.py --user "$$(id -un)"

gateway-install:
	sudo python3 scripts/install_hackathon_asterisk.py

sip-check-vm:
	python3 scripts/check_vm_sip.py

install:
	cd ai-worker && uv sync --locked --extra models --group dev

build:
	cd go-core && go build -trimpath -o bin/server ./cmd/server
	cd go-core && go build -trimpath -o bin/cli-test ./cmd/cli-test
	cd go-core && go build -trimpath -o bin/transcripts ./cmd/transcripts

logs:
	tail -n 60 var/log/core-events.jsonl var/log/speech-events.jsonl

transcripts:
	./go-core/bin/transcripts -last 50

retention-timer: build
	python3 scripts/install_transcript_retention.py

run-core: build
	./go-core/bin/server

run-ai:
	cd ai-worker && uv run --inexact --locked --extra models python main.py

docker-up: setup
	docker compose up -d asterisk

docker-down:
	docker compose down

test: test-unit test-python

test-unit:
	cd go-core && go test -race ./...

test-python:
	cd ai-worker && uv run --inexact --locked --group dev python -m pytest -q

lint:
	cd go-core && test -z "$$(gofmt -l cmd pkg)"
	cd go-core && go vet ./...
	cd ai-worker && uv run --inexact --locked --group dev ruff check src tests main.py ../scripts
	cd ai-worker && uv run --inexact --locked --group dev ruff format --check src tests main.py ../scripts

check: lint test

voice:
	cd ai-worker && uv run --inexact --locked --extra models python ../scripts/voice_dialogue.py

smoke:
	cd ai-worker && uv run --inexact --locked --extra models python ../scripts/test_voice_simulation.py

scenarios: build
	cd ai-worker && uv run --inexact --locked --group dev python ../scripts/run_scenarios.py

scenarios-voice: build
	cd ai-worker && uv run --inexact --locked --extra models python ../scripts/run_scenarios.py --voice

zer0days: build
	cd ai-worker && uv run --inexact --locked --extra models python ../scripts/run_zer0days.py

zer0days-voice: build
	cd ai-worker && uv run --inexact --locked --extra models python ../scripts/run_zer0days.py --voice --only T-06,T-10,T-12,T-17,T-18,T-36

sim: build
	cd ai-worker && uv run --inexact --locked --group dev python ../scripts/user_sim.py --backend ollama --no-voice

sim-live: build
	cd ai-worker && uv run --inexact --locked --extra models python ../scripts/user_sim.py --backend gemini-live

sim-live-1h: build
	cd ai-worker && uv run --inexact --locked --extra models python ../scripts/user_sim.py --backend gemini-live --duration-min 60

clean:
	rm -rf go-core/bin


