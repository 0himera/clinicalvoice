#!/usr/bin/env python3
"""Initiate a reminder for a consenting patient's existing visit."""

import argparse

from client import VoiceClient


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("visit_id", type=int)
    args = parser.parse_args()
    client = VoiceClient()
    try:
        response = client.core.post("/api/reminders/dial", json={"visit_id": args.visit_id})
        response.raise_for_status()
        print(response.json()["status"])
    finally:
        client.close()


if __name__ == "__main__":
    main()
