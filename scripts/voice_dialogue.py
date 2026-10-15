#!/usr/bin/env python3
"""Local microphone diagnostic. Official demos use SIP, not this client."""

import subprocess
import tempfile
from pathlib import Path

from client import VoiceClient


def play(audio):
    with tempfile.NamedTemporaryFile(suffix=".wav") as output:
        output.write(audio)
        output.flush()
        subprocess.run(["aplay", "-q", output.name], check=True)


def main():
    client = VoiceClient()
    try:
        result = client.start()
        while True:
            print(result["bot_speech"])
            play(client.speak(result["bot_speech"]))
            if result.get("should_hangup") or result.get("transfer_target"):
                break
            if input("Enter — говорить 5 секунд, q — выход: ").strip() == "q":
                break
            with tempfile.TemporaryDirectory(prefix="tochka-mic-") as directory:
                path = Path(directory) / "input.wav"
                subprocess.run(
                    [
                        "arecord",
                        "-q",
                        "-d",
                        "5",
                        "-r",
                        "16000",
                        "-c",
                        "1",
                        "-f",
                        "S16_LE",
                        str(path),
                    ],
                    check=True,
                )
                result = client.turn(client.transcribe(path.read_bytes()))
    finally:
        client.close()


if __name__ == "__main__":
    main()
