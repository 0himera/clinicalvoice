#!/usr/bin/env python3
"""Check both local services without printing credentials or patient data."""

import json
import os
import sys
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


def main():
    settings = {}
    if (ROOT / ".env").exists():
        for line in (ROOT / ".env").read_text().splitlines():
            if "=" in line and not line.lstrip().startswith("#"):
                key, value = line.split("=", 1)
                settings[key.strip()] = value.strip().strip("\"'")
    settings.update(os.environ)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    failed = False
    checks = (
        ("core", settings.get("PORT", "8080"), "/readyz"),
        ("speech", settings.get("AI_WORKER_PORT", "8000"), "/health"),
    )
    for name, port, path in checks:
        try:
            port_number = int(port)
            if not 0 < port_number < 65536:
                raise ValueError("invalid port")
            with opener.open(f"http://127.0.0.1:{port_number}{path}", timeout=5) as response:
                data = json.load(response)
            if data.get("status") != "ok":
                raise ValueError("service is not ready")
            if name == "speech" and not (data.get("asr_ready") and data.get("tts_ready")):
                raise ValueError("speech models are not ready")
            mode = data.get("mode", data.get("device", ""))
            print(f"{name}: OK ({mode})")
        except (OSError, ValueError, urllib.error.URLError):
            print(f"{name}: FAILED; check service status and journal", file=sys.stderr)
            failed = True
    return int(failed)


if __name__ == "__main__":
    sys.exit(main())
