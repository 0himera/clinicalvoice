#!/usr/bin/env python3
"""Repeat local speech/HTTP measurements using isolated mock data; no audio is saved."""

import argparse
import io
import json
import math
import os
import platform
import statistics
import time
import wave
from datetime import UTC, datetime
from pathlib import Path

from client import ROOT, VoiceClient
from run_scenarios import isolated_server


def percentiles(values):
    ordered = sorted(values)
    return {
        "p50_ms": round(statistics.median(ordered), 2),
        "p95_ms": round(ordered[math.ceil(0.95 * len(ordered)) - 1], 2),
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--samples", type=int, default=5)
    parser.add_argument("--port", type=int, default=18086)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    if not 1 <= args.samples <= 50:
        parser.error("--samples must be between 1 and 50")
    os.umask(0o077)
    # This measures speech and deterministic dialogue, without extra NLU or MIS traffic.
    os.environ.update(LLM_PROVIDER="none", DECISION_MODEL_ENABLED="false")
    samples = []
    with isolated_server(args.port):
        client = VoiceClient()
        try:
            client.wait_ready()
            client.require_mock()
            health = client.checked(client.ai.get("/health")).json()
            before = client.visits()
            for index in range(args.samples + 1):
                client.start()
                start = time.perf_counter()
                audio = client.speak("Подскажите часы работы поликлиники", "baya")
                tts = time.perf_counter()
                recognition = client.checked(
                    client.ai.post("/asr", files={"file": ("input.wav", audio, "audio/wav")})
                ).json()
                asr = time.perf_counter()
                result = client.checked(
                    client.core.post(
                        "/api/dialogue/turn",
                        json={
                            "call_id": client.call_id,
                            "text": recognition["text"],
                            "asr_uncertain": recognition.get("needs_repeat", False),
                        },
                    )
                ).json()
                end = time.perf_counter()
                with wave.open(io.BytesIO(audio)) as wav:
                    duration = wav.getnframes() / wav.getframerate()
                row = {
                    "tts_ms": round((tts - start) * 1000, 2),
                    "asr_ms": round((asr - tts) * 1000, 2),
                    "core_ms": round((end - asr) * 1000, 2),
                    "serial_ms": round((end - start) * 1000, 2),
                    "input_audio_seconds": round(duration, 3),
                    "needs_repeat": recognition.get("needs_repeat", False),
                    "faq_correct": "семи тридцати" in result.get("bot_speech", ""),
                }
                if index:
                    samples.append(row)
                client.end_call()
            assert client.visits() == before, "benchmark unexpectedly changed MIS mock visits"
        finally:
            client.close()
    report = {
        "time_utc": datetime.now(UTC).isoformat(),
        "hardware": {"platform": platform.platform(), "cpu_count": os.cpu_count()},
        "speech_health": health,
        "scope": "local TTS + ASR + HTTP, one warmup excluded, serial synthetic FAQ, isolated mock, no audio retention; no SIP/playback or human pauses",
        "samples": samples,
        "summary": {
            key: percentiles([row[key] for row in samples])
            for key in ("tts_ms", "asr_ms", "core_ms", "serial_ms")
        },
    }
    output = args.output or ROOT / "var" / (
        "benchmark-" + datetime.now(UTC).strftime("%Y%m%dT%H%M%SZ") + ".json"
    )
    output.parent.mkdir(parents=True, exist_ok=True)
    with output.open("x") as stream:
        json.dump(report, stream, ensure_ascii=False, indent=2)
        stream.write("\n")
    print(json.dumps(report["summary"], ensure_ascii=False))
    print("Correct FAQ:", sum(row["faq_correct"] for row in samples), "/", len(samples))
    print("Report:", output)
    return int(not all(row["faq_correct"] for row in samples))


if __name__ == "__main__":
    raise SystemExit(main())
