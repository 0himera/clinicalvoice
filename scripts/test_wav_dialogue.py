#!/usr/bin/env python3
"""Recognize one WAV, process a real dialogue turn, and write the spoken reply."""

import argparse
from pathlib import Path

from client import VoiceClient


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("input", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    client = VoiceClient()
    try:
        client.start()
        result = client.turn(client.transcribe(args.input.read_bytes()))
        args.output.write_bytes(client.speak(result["bot_speech"]))
        print(result["state"])
    finally:
        client.close()


if __name__ == "__main__":
    main()
