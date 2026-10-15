#!/usr/bin/env bash
set -euo pipefail
TASK_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
exec "$TASK_ROOT/ai-worker/.venv/bin/python" "$TASK_ROOT/scripts/voice_dialogue.py" "$@"
