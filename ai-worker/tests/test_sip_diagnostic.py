import asyncio
import socket
import time
from pathlib import Path

import pytest

SCRIPTS = str(Path(__file__).resolve().parents[2] / "scripts")


@pytest.mark.asyncio
async def test_sip_wait_does_not_accept_previous_reply(monkeypatch):
    monkeypatch.syspath_prepend(SCRIPTS)
    from sip_smoke import Phone

    phone = Phone.__new__(Phone)
    phone.voice_seen = asyncio.Event()
    phone.voice_seen.set()
    phone.last_voice = time.monotonic() - 2
    phone.response_after = time.monotonic()
    waiting = asyncio.create_task(phone.wait_bot())
    await asyncio.sleep(0)
    assert not waiting.done(), "previous reply was mistaken for the new response"
    phone.last_voice = time.monotonic()
    phone.voice_seen.set()
    await asyncio.wait_for(waiting, 2)


@pytest.mark.asyncio
async def test_previous_call_bye_does_not_disconnect_current_call(monkeypatch):
    monkeypatch.syspath_prepend(SCRIPTS)
    from sip_smoke import Phone

    phone = Phone(password="unit-fixture-password")
    peer = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    peer.bind(("127.0.0.1", 0))
    peer.setblocking(False)
    phone.dialog = ("current-call", "local", 1, "<sip:peer@127.0.0.1>", "sip:127.0.0.1")
    phone.target = ("127.0.0.1", 12345)
    listener = asyncio.create_task(phone.listen())
    loop = asyncio.get_running_loop()
    try:
        await loop.sock_sendto(
            peer,
            phone.packet("BYE", "sip:200@127.0.0.1", "previous-call", "peer", 1),
            phone.sip.getsockname(),
        )
        response, _ = await asyncio.wait_for(loop.sock_recvfrom(peer, 4096), 2)
        assert response.startswith(b"SIP/2.0 200 OK")
        await asyncio.sleep(0)
        assert phone.target == ("127.0.0.1", 12345)
    finally:
        listener.cancel()
        await asyncio.gather(listener, return_exceptions=True)
        phone.sip.close()
        phone.rtp.close()
        peer.close()
