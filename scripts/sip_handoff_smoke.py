#!/usr/bin/env python3
"""Verify actual AMI redirects to loopback operator and urgent-care SIP accounts."""

import asyncio

from client import VoiceClient
from sip_smoke import Phone


async def main():
    client = VoiceClient()
    try:
        await asyncio.to_thread(client.wait_ready)
        for number, phrase in [("101", "Соедините с оператором"), ("104", "Не могу дышать")]:
            caller = Phone()
            operator = Phone(number, "operatorpass")
            try:
                await caller.start()
                await operator.start()
                await caller.request("INVITE", "sip:700@127.0.0.1", caller.sdp())
                await caller.wait_bot()
                await caller.say(await asyncio.to_thread(client.speak, phrase, "baya"))
                await asyncio.wait_for(operator.connected.wait(), 20)
                print(f"Live SIP handoff to {number} passed.", flush=True)
            finally:
                await caller.close()
                await asyncio.sleep(0.1)
                await operator.close()
    finally:
        client.close()


if __name__ == "__main__":
    asyncio.run(main())
