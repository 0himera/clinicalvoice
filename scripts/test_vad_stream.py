#!/usr/bin/env python3
"""Check short replies after long pauses with real VAD and G.711 audio.

Requires the running local TTS service and installed model dependencies. Audio
stays in memory. This catches recurrent-state and frame-alignment regressions
that a full-file ASR request cannot exercise.
"""

import subprocess
import sys

from client import ROOT, VoiceClient


def g711_round_trip(wav: bytes) -> bytes:
    encoded = subprocess.run(
        [
            "ffmpeg",
            "-v",
            "error",
            "-i",
            "pipe:0",
            "-f",
            "mulaw",
            "-ar",
            "8000",
            "-ac",
            "1",
            "pipe:1",
        ],
        input=wav,
        capture_output=True,
        check=True,
    ).stdout
    return subprocess.run(
        [
            "ffmpeg",
            "-v",
            "error",
            "-f",
            "mulaw",
            "-ar",
            "8000",
            "-ac",
            "1",
            "-i",
            "pipe:0",
            "-f",
            "s16le",
            "pipe:1",
        ],
        input=encoded,
        capture_output=True,
        check=True,
    ).stdout


def main():
    sys.path.insert(0, str(ROOT / "ai-worker"))
    from src.audio import EndpointDetector
    from src.vad_service import SileroVADService

    client = VoiceClient()
    try:
        phrases = [
            "Повторите",
            "Сориентируйте, к кому вообще можно обратиться?",
            "У меня иногда болит голова, мне к кому записаться?",
            "Да",
            "Нет",
            "Зачем?",
        ]
        samples = []
        for text in phrases:
            raw = g711_round_trip(client.speak(text, "baya"))
            samples.append(raw + b"\0" * ((320 - len(raw) % 320) % 320))
        for phase_ms in range(0, 32, 4):
            detector = EndpointDetector(SileroVADService().new_stream())
            detector.feed(b"\0" * (phase_ms * 16))
            counts = []
            for index, raw in enumerate(samples):
                # The same stream processes earlier utterances and the silence
                # while the bot speaks. Shift speech relative to 32 ms frames.
                pause_seconds = 17 if index == 2 else 8
                data = b"\0" * (16000 * pause_seconds) + raw + b"\0" * 16000
                completed = []
                for offset in range(0, len(data), 320):
                    complete, _ = detector.feed(data[offset : offset + 320])
                    completed.extend(complete)
                counts.append(len(completed))
            assert counts == [1] * len(phrases), (phase_ms, counts)
            print(f"phase={phase_ms} ms: {counts}", flush=True)
        print("48 real VAD/G.711 phrase cases passed; no audio saved.")
    finally:
        client.close()


if __name__ == "__main__":
    main()
