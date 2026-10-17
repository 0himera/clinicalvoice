import asyncio
import json
import struct
import uuid

import httpx

from src.audio import frame
from src.settings import Settings
from src.telephony import Bridge, Call


async def test_audio_socket_reaches_dialogue_and_returns_audio(services, tmp_path):
    turns = []

    async def core(request):
        if request.url.path.endswith("/turn"):
            turns.append(json.loads(request.content))
            return httpx.Response(200, json={"bot_speech": "Готово", "should_hangup": True})
        return httpx.Response(200, json={"status": "closed"})

    bridge = Bridge(
        Settings(
            agi_port=0,
            audio_port=0,
            debug_dialogue=True,
            debug_log_dir=str(tmp_path),
            technical_logs=True,
            technical_log_dir=str(tmp_path),
        ),
        services,
    )
    await bridge.http.aclose()
    bridge.http = httpx.AsyncClient(transport=httpx.MockTransport(core), base_url="http://core")
    await bridge.start()
    try:
        call_id = str(uuid.uuid4())
        bridge.calls[call_id] = Call(call_id, {"bot_speech": "Здравствуйте"})
        port = bridge.servers[1].sockets[0].getsockname()[1]
        reader, writer = await asyncio.open_connection("127.0.0.1", port)
        writer.write(frame(1, uuid.UUID(call_id).bytes))
        await writer.drain()
        # Wait until the initial speech actually reaches the TCP socket.
        kind, size = struct.unpack("!BH", await reader.readexactly(3))
        assert kind == 0x10
        await reader.readexactly(size)
        writer.write(frame(0x10, struct.pack("<h", 32124) * 256 * 8))
        writer.write(frame(0x10, b"\x00" * 512 * 6))
        writer.write(frame(0x10, b"\x00" * 512 * 6))
        writer.write(frame(0x10, b"\x00" * 512 * 8))
        await writer.drain()
        async with asyncio.timeout(3):
            while await reader.read(4096):
                pass
        writer.close()
        await writer.wait_closed()
        assert len(turns) == 1
        assert turns[0]["text"] == "Запишите к терапевту"
        assert turns[0]["call_id"] == call_id
    finally:
        await bridge.close()
    events = [json.loads(line) for line in (tmp_path / "speech.jsonl").read_text().splitlines()]
    recognized = next(event for event in events if event["event"] == "asr_result")
    assert recognized["call_id"] == call_id
    assert recognized["text"] == "Запишите к терапевту"
    assert recognized["audio_ms"] > 0 and recognized["latency_ms"] >= 0
    assert "rms_dbfs" in recognized
    assert any(
        event["event"] == "bot_response" and event["bot_speech"] == "Готово" for event in events
    )
    technical = (tmp_path / "speech-events.jsonl").read_text()
    assert "Запишите к терапевту" not in technical and "Готово" not in technical
    safe_asr = next(
        json.loads(line)
        for line in technical.splitlines()
        if json.loads(line)["event"] == "asr_result"
    )
    assert safe_asr["rms_dbfs"] is not None
    # G.711 saturation must be detected even when VAD retains silent padding.
    assert 0 < safe_asr["clipped_percent"] < 100


async def test_unknown_audio_socket_uuid_is_rejected(services):
    bridge = Bridge(Settings(agi_port=0, audio_port=0), services)
    await bridge.start()
    try:
        reader, writer = await asyncio.open_connection(
            "127.0.0.1", bridge.servers[1].sockets[0].getsockname()[1]
        )
        writer.write(frame(1, uuid.uuid4().bytes))
        await writer.drain()
        assert await asyncio.wait_for(reader.read(), 1) == b""
        writer.close()
        await writer.wait_closed()
    finally:
        await bridge.close()


async def test_emergency_speech_bypasses_blocked_dialogue_turn(services):
    waiting, release, transferred = asyncio.Event(), asyncio.Event(), asyncio.Event()

    class Recognizer:
        count = 0

        def transcribe_wav(self, audio):
            self.count += 1
            return "Запишите к терапевту" if self.count == 1 else "Не могу дышать"

    services.asr = Recognizer()

    async def core(request):
        if request.url.path.endswith("/interrupt"):
            if json.loads(request.content)["text"] == "Не могу дышать":
                return httpx.Response(
                    200, json={"bot_speech": "Звоните сто три", "transfer_target": "104"}
                )
            return httpx.Response(200, json={"interrupted": False})
        if request.url.path.endswith("/turn"):
            waiting.set()
            await release.wait()
            return httpx.Response(200, json={"bot_speech": "Не должно звучать"})
        if request.url.path.endswith("/transfer"):
            transferred.set()
        return httpx.Response(200, json={"status": "ok"})

    bridge = Bridge(Settings(agi_port=0, audio_port=0), services)
    await bridge.http.aclose()
    bridge.http = httpx.AsyncClient(transport=httpx.MockTransport(core), base_url="http://core")
    await bridge.start()
    writer = None
    try:
        call_id = str(uuid.uuid4())
        bridge.calls[call_id] = Call(call_id, {"bot_speech": "Здравствуйте"})
        reader, writer = await asyncio.open_connection(
            "127.0.0.1", bridge.servers[1].sockets[0].getsockname()[1]
        )
        writer.write(frame(1, uuid.UUID(call_id).bytes))
        await writer.drain()
        _, size = struct.unpack("!BH", await reader.readexactly(3))
        await reader.readexactly(size)

        async def utterance():
            writer.write(frame(0x10, b"\x01\x00" * 256 * 8))
            for count in (6, 6, 8):
                writer.write(frame(0x10, b"\x00" * 512 * count))
            await writer.drain()

        await utterance()
        await asyncio.wait_for(waiting.wait(), 2)
        await utterance()
        await asyncio.wait_for(transferred.wait(), 2)
        assert not release.is_set(), "emergency waited for the regular dialogue response"
    finally:
        release.set()
        if writer:
            writer.close()
            await writer.wait_closed()
        await bridge.close()
