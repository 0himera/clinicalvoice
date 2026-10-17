.PHONY: help setup install build run-core run-ai docker-up docker-down test test-unit test-python lint check scenarios scenarios-voice sim sim-live voice smoke logs transcripts retention-timer clean

help:
	@echo "setup install build docker-up run-core run-ai test lint check scenarios scenarios-voice sim sim-live voice smoke logs transcripts retention-timer"

setup:
	python3 scripts/setup.py

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
	cd go-core && go vet ./...
	cd ai-worker && uv run --inexact --locked --group dev ruff check src tests main.py ../scripts
	cd ai-worker && uv run --inexact --locked --group dev ruff format --check src tests main.py ../scripts

check: lint test

voice:
	cd ai-worker && uv run --inexact --locked --extra models python ../scripts/voice_dialogue.py

smoke:
	cd ai-worker && uv run --inexact --locked --extra models python ../scripts/test_voice_simulation.py

scenarios:
	cd ai-worker && uv run --inexact --locked --group dev python ../scripts/run_scenarios.py

scenarios-voice:
	cd ai-worker && uv run --inexact --locked --extra models python ../scripts/run_scenarios.py --voice

sim:
	cd ai-worker && uv run --inexact --locked --group dev python ../scripts/user_sim.py --backend ollama --no-voice

sim-live:
	cd ai-worker && uv run --inexact --locked --extra models python ../scripts/user_sim.py --backend gemini-live

sim-live-1h:
	cd ai-worker && uv run --inexact --locked --extra models python ../scripts/user_sim.py --backend gemini-live --duration-min 60

clean:
	rm -rf go-core/bin


