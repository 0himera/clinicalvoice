import asyncio
import io
import threading
import time
import uuid
import wave

import httpx
import pytest

from src.audio import EndpointDetector, frame
from src.services import Services
from src.settings import Settings
from src.telephony import Bridge, Call, agi_variable


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
async def test_cancelled_asr_keeps_capacity_until_thread_finishes():
    started, finish = threading.Event(), threading.Event()

    class HangingASR:
        def transcribe_wav(self, audio):
            started.set()
            assert finish.wait(2)
            return "Готово"

    services = Services(
        asr=HangingASR(), tts=SlowTTS(), vad=MockVAD(), asr_capacity=asyncio.Semaphore(1)
    )
    task = asyncio.create_task(services.transcribe(b"dummy"))
    assert await asyncio.to_thread(started.wait, 1)
    task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await task
    try:
        assert len(services._work) == 1
        with pytest.raises(TimeoutError):
            await asyncio.wait_for(services.transcribe(b"next"), 0.02)
    finally:
        finish.set()
        await services.close()
    assert not services._work
    assert await services.transcribe(b"next") == "Готово"
    await services.close()


@pytest.mark.asyncio
async def test_cancelled_tts_does_not_return_running_model_to_pool():
    started, finish = threading.Event(), threading.Event()

    class HangingTTS(SlowTTS):
        def synthesize(self, *args):
            started.set()
            assert finish.wait(2)
            return super().synthesize(*args)

    model = HangingTTS()
    services = Services(asr=SlowASR(), tts=model, vad=MockVAD())
    task = asyncio.create_task(services.synthesize("один"))
    assert await asyncio.to_thread(started.wait, 1)
    task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await task
    try:
        assert services._get_tts_pool().empty()
        with pytest.raises(TimeoutError):
            await asyncio.wait_for(services.synthesize("два"), 0.02)
    finally:
        finish.set()
        await services.close()
    assert services._get_tts_pool().qsize() == 1
    await services.synthesize("три")
    assert model.max_active == 1
    await services.close()


def test_fastagi_command_validates_real_encoder():
    for name, value in [("VAR\r\nNAME", "value"), ("VAR", "value\nINJECTION"), ('VAR"', "value")]:
        with pytest.raises(ValueError):
            agi_variable(name, value)
    assert (
        agi_variable("VAR_NAME", 'Doctor "Smith"')
        == b'SET VARIABLE VAR_NAME "Doctor \\"Smith\\""\n'
    )


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
        for _ in range(5):
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


def test_barge_in_requires_an_accepted_utterance():
    detector = EndpointDetector(lambda pcm: True, barge_threshold=2)
    voice = b"\x01\x00" * 256
    for _ in range(4):
        _, interrupted = detector.feed(voice)
        assert not interrupted
    _, interrupted = detector.feed(voice)
    assert interrupted
    _, interrupted = detector.feed(voice)
    assert not interrupted
