#!/usr/bin/env python3
"""Exercise all three outbound reminder outcomes using a loopback SIP phone.

Start the mock core with ALLOW_DEMO_CALLS_OUTSIDE_WINDOW=true for an evening demo.
The normal scheduler always respects the calling window.
"""

import asyncio

from client import VoiceClient
from sip_smoke import Phone


async def main():
    client = VoiceClient()
    phone = None
    try:
        await asyncio.to_thread(client.wait_ready)
        await asyncio.to_thread(client.require_mock)
        cases = [
            ("терапевту", ["Да, это я", "Да, приду"], "Запланирована"),
            (
                "офтальмологу",
                [
                    "Да, это я",
                    "Перенесите запись",
                    "Первый вариант",
                    "Да, подтверждаю",
                    "Нет, спасибо",
                ],
                "Запланирована",
            ),
            ("лору", ["Да, это я", "Не смогу прийти", "Да, отменяйте", "Нет, спасибо"], "Отменена"),
        ]
        for specialty, phrases, status in cases:
            phone = Phone()
            await phone.start()
            before = {v["id"] for v in await asyncio.to_thread(client.visits)}
            await asyncio.to_thread(client.start)
            for text in [
                f"Запишите к {specialty}",
                "Иванов Иван Иванович 12.03.1985",
                "первый",
                "да",
            ]:
                result = await asyncio.to_thread(client.turn, text)
            assert result.get("outcome") == "BOOKED", result
            visit = next(v for v in await asyncio.to_thread(client.visits) if v["id"] not in before)
            await asyncio.to_thread(client.core.post, f"/api/dialogue/{client.call_id}/end")
            response = await asyncio.to_thread(
                client.core.put,
                f"/api/contacts/{visit['client_id']}",
                json={"endpoint": "PJSIP/200", "reminder_consent": True},
            )
            response.raise_for_status()
            phone.connected.clear()
            response = await asyncio.to_thread(
                client.core.post, "/api/reminders/dial", json={"visit_id": visit["id"]}
            )
            response.raise_for_status()
            await asyncio.wait_for(phone.connected.wait(), 10)
            await phone.wait_bot()
            for phrase in phrases:
                await phone.say(await asyncio.to_thread(client.speak, phrase, "baya"))
                await phone.wait_bot()
            actual = next(
                v for v in await asyncio.to_thread(client.visits) if v["id"] == visit["id"]
            )
            assert actual["status"] == status
            if specialty == "терапевту":
                response = await asyncio.to_thread(
                    client.core.get, f"/api/clients/{visit['client_id']}"
                )
                response.raise_for_status()
                marker = f"Подтверждена запись #{visit['id']} {visit['scheduled_at']}"
                assert marker in response.json().get("comment", ""), (
                    "voice confirmation was not saved in the MIS comment"
                )
            if specialty == "офтальмологу":
                assert (actual["scheduled_at"], actual["doctor"]["id"]) != (
                    visit["scheduled_at"],
                    visit["doctor"]["id"],
                )
            print(f"Outbound SIP outcome passed: {specialty}", flush=True)
            await phone.close()
            phone = None
            await asyncio.sleep(1)
            if status == "Запланирована":
                # Clean up only the visit created by this case, through the public
                # dialogue API. Confirmation evidence remains in the client comment.
                await asyncio.to_thread(client.start)
                await asyncio.to_thread(client.turn, "Отмените запись")
                result = await asyncio.to_thread(client.turn, "Иванов Иван Иванович 12.03.1985")
                assert result["state"] == "STATE_CONFIRM_CANCEL", (
                    "run this test with an isolated mock database"
                )
                result = await asyncio.to_thread(client.turn, "да")
                assert result.get("outcome") == "CANCELLED"
                await asyncio.to_thread(client.core.post, f"/api/dialogue/{client.call_id}/end")
        print("All three live outbound SIP scenarios passed.", flush=True)
    finally:
        if phone is not None:
            await phone.close()
        client.close()


if __name__ == "__main__":
    asyncio.run(main())
