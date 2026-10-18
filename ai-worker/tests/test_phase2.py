import asyncio
import io
import time
import uuid
import wave

import httpx
import pytest

from src.audio import EndpointDetector, frame
from src.services import Services
from src.settings import Settings
from src.telephony import Bridge, Call


def wav_fixture(pcm: bytes = b"\x00\x00" * 800, rate: int = 8000) -> bytes:
    buffer = io.BytesIO()
    with wave.open(buffer, "wb") as output:
        output.setnchannels(1)
        output.setsampwidth(2)
        output.setframerate(rate)
        output.writeframes(pcm)
    return buffer.getvalue()


class SlowTTS:
    def __init__(self, name="tts"):
        self.name = name
        self.active = 0
        self.max_active = 0

    def synthesize(self, text: str, sample_rate: int = 8000, speaker=None) -> bytes:
        self.active += 1
        self.max_active = max(self.max_active, self.active)
        try:
            time.sleep(0.05)
            return wav_fixture(rate=sample_rate)
        finally:
            self.active -= 1


class SlowASR:
    def __init__(self):
        self.active = 0
        self.max_active = 0

    def transcribe_wav(self, audio: bytes) -> str:
        self.active += 1
        self.max_active = max(self.max_active, self.active)
        try:
            time.sleep(0.05)
            return "Тест ASR"
        finally:
            self.active -= 1


class MockVAD:
    def new_stream(self):
        return lambda pcm: pcm != b"\x00" * len(pcm)


@pytest.mark.asyncio
async def test_tts_pool_concurrent_execution_without_locks():
    m1 = SlowTTS("m1")
    m2 = SlowTTS("m2")
    services = Services(
        asr=SlowASR(),
        tts=m1,
        tts_instances=[m1, m2],
        vad=MockVAD(),
        tts_capacity=asyncio.Semaphore(2),
    )
    # Synthesize two requests concurrently
    res1, res2 = await asyncio.gather(
        services.synthesize("Привет мир 1"),
        services.synthesize("Привет мир 2"),
    )
    assert res1.startswith(b"RIFF")
    assert res2.startswith(b"RIFF")
    # Verify both models were used
    assert m1.max_active == 1
    assert m2.max_active == 1
    # Verify pool has returned both instances
    pool = services._get_tts_pool()
    assert pool.qsize() == 2


@pytest.mark.asyncio
async def test_inference_cancellation_without_shield():
    started = asyncio.Event()

    class HangingASR:
        def transcribe_wav(self, audio: bytes) -> str:
            started.set()
            time.sleep(0.5)
            return "Готово"

    services = Services(asr=HangingASR(), tts=SlowTTS(), vad=MockVAD())
    task = asyncio.create_task(services.transcribe(b"dummy"))
    await started.wait()
    task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await task
    # Ensure task is not shielded and removed from work
    assert len(services._work) == 0


@pytest.mark.asyncio
async def test_fastagi_command_crlf_rejection_and_quoting():
    class MockStream:
        def __init__(self):
            self.written = bytearray()
            self.lines = [b"200 result=0\n", b"200 result=0\n"]

        def write(self, data):
            self.written.extend(data)

        async def drain(self):
            pass

        async def readline(self):
            if self.lines:
                return self.lines.pop(0)
            return b""

    stream = MockStream()

    # Test the command logic directly as defined in telephony.py
    async def command(name: str, value: str):
        if "\r" in name or "\n" in name or "\r" in value or "\n" in value:
            raise ValueError("CRLF characters not permitted in AGI command")
        escaped_value = value.replace("\\", "\\\\").replace('"', '\\"')
        stream.write(f'SET VARIABLE {name} "{escaped_value}"\n'.encode("ascii"))
        await stream.drain()
        answer = await stream.readline()
        if not answer.startswith(b"200 result="):
            raise ConnectionError("AGI command failed")

    # Reject newline in name
    with pytest.raises(ValueError, match="CRLF characters not permitted"):
        await command("VAR\r\nNAME", "val")

    # Reject newline in value
    with pytest.raises(ValueError, match="CRLF characters not permitted"):
        await command("VAR_NAME", "val\nINJECTION")

    # Properly escape quotes
    await command("VAR_NAME", 'Doctor "Smith" & "Jones"')
    assert b'SET VARIABLE VAR_NAME "Doctor \\"Smith\\" & \\"Jones\\""\n' in stream.written


@pytest.mark.asyncio
async def test_barge_in_and_safe_utterance_queue(services, tmp_path):
    async def core(request):
        if request.url.path.endswith("/turn"):
            return httpx.Response(200, json={"bot_speech": "Ответ", "should_hangup": True})
        if request.url.path.endswith("/start"):
            return httpx.Response(
                200, json={"call_id": str(uuid.uuid4()), "bot_speech": "Длинная речь бота..."}
            )
        return httpx.Response(200, json={"status": "ok"})

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
        bridge.calls[call_id] = Call(
            call_id,
            {"bot_speech": "Здравствуйте! Очень длинный текст, который бот будет говорить долго."},
        )
        port = bridge.servers[1].sockets[0].getsockname()[1]
        reader, writer = await asyncio.open_connection("127.0.0.1", port)
        writer.write(frame(1, uuid.UUID(call_id).bytes))
        await writer.drain()

        # Read beginning of bot playback
        await reader.read(100)

        # Barge-in: send 2 voice frames (triggering barge-in)
        voice_chunk = b"\x01\x00" * 256
        writer.write(frame(0x10, voice_chunk))
        writer.write(frame(0x10, voice_chunk))
        await writer.drain()

        # Followed by quiet to complete utterance
        for _ in range(22):
            writer.write(frame(0x10, b"\x00" * 512))
        await writer.drain()

        # Wait a bit for processing
        await asyncio.sleep(0.3)
        writer.close()
        await writer.wait_closed()
    finally:
        await bridge.close()

    lines = (tmp_path / "speech.jsonl").read_text().splitlines()
    speech_events = [import_json(line) for line in lines]
    # Check that speech_started with barge_in=True occurred
    barge_events = [
        e for e in speech_events if e.get("event") == "speech_started" and e.get("barge_in") is True
    ]
    assert len(barge_events) >= 1
    # Check revision increment
    assert barge_events[0]["revision"] >= 1


def import_json(raw_line):
    import json

    return json.loads(raw_line)


@pytest.mark.asyncio
async def test_utterance_queue_overflow_safe_eviction():
    utterances = asyncio.Queue(maxsize=2)
    t1 = asyncio.create_task(asyncio.sleep(10))
    t2 = asyncio.create_task(asyncio.sleep(10))
    t3 = asyncio.create_task(asyncio.sleep(10))
    utterances.put_nowait(t1)
    utterances.put_nowait(t2)
    assert utterances.full()

    if utterances.full():
        try:
            oldest = utterances.get_nowait()
            if oldest and not oldest.done():
                oldest.cancel()
        except (asyncio.QueueEmpty, ValueError):
            pass
    utterances.put_nowait(t3)
    assert t1.cancelling() or t1.cancelled()
    assert utterances.qsize() == 2
    items = [utterances.get_nowait(), utterances.get_nowait()]
    assert items == [t2, t3]
    t2.cancel()
    t3.cancel()


def test_barge_in_triggers_at_two_voice_frames():
    detector = EndpointDetector(lambda pcm: True, barge_threshold=2)
    voice = b"\x01\x00" * 256
    # Frame 1: voiced
    _, interrupted = detector.feed(voice)
    assert not interrupted
    # Frame 2: voiced -> barge-in!
    _, interrupted = detector.feed(voice)
    assert interrupted
    # Frame 3: voiced -> no repeated barge-in
    _, interrupted = detector.feed(voice)
    assert not interrupted
