.PHONY: all build run-ai run-core docker-up docker-down docker-logs test test-unit test-sim test-mic benchmark-llm clean

# Цвета для терминала
CYAN  = \033[0;36m
GREEN = \033[0;32m
RESET = \033[0m

all: build

## build: Компиляция Go Core сервера и тестового CLI
build:
	@echo "$(CYAN)[BUILD] Сборка Go Core сервисов...$(RESET)"
	@cd go-core && go build -o bin/server cmd/server/main.go
	@cd go-core && go build -o bin/cli-test cmd/cli-test/main.go
	@echo "$(GREEN)[OK] Бинарные файлы собраны в go-core/bin/$(RESET)"

## docker-up: Запуск инфраструктуры (NATS + Asterisk)
docker-up:
	@echo "$(CYAN)[DOCKER] Запуск NATS и Asterisk...$(RESET)"
	docker compose up -d
	@echo "$(GREEN)[OK] Контейнеры запущены.$(RESET)"

## docker-down: Остановка Docker контейнеров
docker-down:
	@echo "$(CYAN)[DOCKER] Остановка контейнеров...$(RESET)"
	docker compose down

## docker-logs: Просмотр логов контейнеров
docker-logs:
	docker compose logs -f

## run-ai: Запуск Python AI Worker (FastAPI: Faster-Whisper ASR + Silero TTS)
run-ai:
	@echo "$(CYAN)[AI-WORKER] Запуск AI Worker на GPU/CPU...$(RESET)"
	PYTHONPATH=ai-worker python3 ai-worker/main.py

## run-core: Запуск Go Core диалогового менеджера
run-core: build
	@echo "$(CYAN)[GO-CORE] Запуск диалогового ядра поликлиники...$(RESET)"
	@cd go-core && ./bin/server

## test-unit: Запуск юнит-тестов Go Core (FSM, Fast-Gate, NLU, SlotManager)
test-unit:
	@echo "$(CYAN)[TEST] Запуск Go тестов...$(RESET)"
	@cd go-core && go test -v ./...

## test-sim: Автоматическая симуляция диалога Голос-в-Голос (сквозной E2E тест)
test-sim:
	@echo "$(CYAN)[TEST] Запуск автоматической голосовой симуляции...$(RESET)"
	python3 scripts/test_voice_simulation.py

## test-mic: Интерактивный диалог через микрофон и наушники (Voice-to-Voice)
test-mic:
	@echo "$(CYAN)[VOICE] Запуск интерактивного голосового клиента...$(RESET)"
	python3 scripts/voice_dialogue.py

## benchmark-llm: Тестирование скорости и TTFT моделей Gemini
benchmark-llm:
	python3 scripts/benchmark_gemini.py

## clean: Очистка артефактов сборки и кэша
clean:
	@echo "$(CYAN)[CLEAN] Очистка бинарников и кэша...$(RESET)"
	@rm -rf go-core/bin
	@find . -type d -name "__pycache__" -exec rm -rf {} + 2>/dev/null || true
	@echo "$(GREEN)[OK] Очищено.$(RESET)"
