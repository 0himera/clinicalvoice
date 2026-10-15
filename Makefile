.PHONY: help setup install build run-core run-ai docker-up docker-down test test-unit test-python lint check voice smoke clean

help:
	@echo "setup install build docker-up run-core run-ai test lint check voice smoke"

setup:
	python3 scripts/setup.py

install:
	cd ai-worker && uv sync --locked --extra models --group dev

build:
	cd go-core && go build -trimpath -o bin/server ./cmd/server
	cd go-core && go build -trimpath -o bin/cli-test ./cmd/cli-test

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

clean:
	rm -rf go-core/bin
