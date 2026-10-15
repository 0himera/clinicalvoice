"""Explicitly enabled, private local JSONL traces; never stores audio."""

import json
import logging
import os
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
    def __init__(self, enabled: bool, directory: str):
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
            path / "speech.jsonl", maxBytes=5 << 20, backupCount=1, encoding="utf-8"
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
