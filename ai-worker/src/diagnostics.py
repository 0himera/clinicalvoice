"""Explicitly enabled, private local JSONL traces; never stores audio."""

import json
import logging
import math
import os
import re
import uuid
from datetime import UTC, datetime
from logging.handlers import RotatingFileHandler
from pathlib import Path


class PrivateHandler(RotatingFileHandler):
    def _open(self):
        fd = os.open(
            self.baseFilename, os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW, 0o600
        )
        os.fchmod(fd, 0o600)
        return os.fdopen(fd, "a", encoding="utf-8")


class DialogueTrace:
    def __init__(self, enabled: bool, directory: str, name: str = "speech"):
        self.handler = None
        if not enabled:
            return
        path = Path(directory)
        if not path.is_absolute():
            path = Path(__file__).resolve().parents[2] / path
        path.mkdir(mode=0o700, parents=True, exist_ok=True)
        if path.is_symlink():
            raise ValueError("debug directory must be a real directory")
        path.chmod(0o700)
        self.handler = PrivateHandler(
            path / (name + ".jsonl"), maxBytes=5 << 20, backupCount=1, encoding="utf-8"
        )

    def __call__(self, event: str, **fields):
        if self.handler is None:
            return
        message = json.dumps(
            {"time": datetime.now(UTC).isoformat(), "event": event, **fields}, ensure_ascii=False
        )
        self.handler.handle(
            logging.LogRecord("dialogue_trace", logging.INFO, "", 0, message, (), None)
        )

    def close(self):
        if self.handler:
            self.handler.close()


class CallDiagnostics:
    """Keep technical metadata separate from explicitly enabled mock transcripts."""

    def __init__(self, settings):
        self.transcript = DialogueTrace(settings.debug_dialogue, settings.debug_log_dir)
        self.technical = DialogueTrace(
            settings.technical_logs, settings.technical_log_dir, "speech-events"
        )

    def __call__(self, event: str, **fields):
        self.transcript(event, **fields)
        safe = {}
        if fields.get("call_id"):
            try:
                safe["call_id"] = str(uuid.UUID(fields["call_id"]))
            except (ValueError, TypeError, AttributeError):
                pass
        numeric = {
            "revision",
            "current_revision",
            "count",
            "audio_ms",
            "latency_ms",
            "played_ms",
            "idle_ms",
            "rms_dbfs",
            "peak_percent",
            "clipped_percent",
            "text_chars",
            "avg_logprob",
            "status",
        }
        flags = {"barge_in", "interrupted", "needs_repeat"}
        code_patterns = {
            "state": r"STATE_[A-Z_]{1,50}",
            "reason_code": r"[RC]-\d{2}",
            "transfer_target": r"10[1245]",
            "outcome": r"[A-Z_]{1,64}",
            "reason": r"peer_hangup|(?:IncompleteRead|Timeout|Connection|Value|OS|Runtime)Error",
            "error": r"(?:HTTPStatus|Connect|Read|Write|Pool|RemoteProtocol|LocalProtocol|Network|Proxy)Error|(?:Connect|Read|Write|Pool)Timeout",
            "operation": r"start|turn|interrupt|end|transfer",
            "mode": r"inbound|outbound",
        }
        for key, value in fields.items():
            if key in numeric and type(value) in (int, float) and math.isfinite(value):
                safe[key] = value
            elif key in flags and type(value) is bool:
                safe[key] = value
            elif (
                key in code_patterns
                and isinstance(value, str)
                and re.fullmatch(code_patterns[key], value)
            ):
                safe[key] = value
        events = {
            "call_started",
            "call_ended",
            "audio_connected",
            "tts_ready",
            "playback_cancelled",
            "playback_finished",
            "response_skipped",
            "bot_response",
            "transfer_requested",
            "asr_result",
            "dialogue_result",
            "speech_started",
            "silence_prompt",
            "core_request",
            "core_request_error",
        }
        if event in events:
            self.technical(event, **safe)

    def close(self):
        self.transcript.close()
        self.technical.close()
