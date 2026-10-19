import asyncio
from types import SimpleNamespace

import httpx
import pytest
from conftest import wav_fixture

from src.cleanup import cancel_tasks
from src.recognition import Recognition
from src.settings import Settings
from src.telephony import Bridge
from src.voice_bridge import create_app


@pytest.mark.parametrize("score,uncertain", [(-0.2, False), (-1.2, True), (float("nan"), True)])
def test_quality_hint_uses_actual_segment_scores(score, uncertain):
    result = Recognition.from_segments([SimpleNamespace(text="да", avg_logprob=score)])
    assert result.text == "да" and result.needs_repeat is uncertain
    assert Recognition.from_segments([]).needs_repeat


async def test_asr_exposes_quality_without_invented_percentage(services):
    class ASR:
        def recognize_wav(self, audio):
            return Recognition("да", True, -1.3)

    services.asr = ASR()
    app = create_app(Settings(telephony=False, api_token="fixture"), services)
    async with (
        app.router.lifespan_context(app),
        httpx.AsyncClient(
            transport=httpx.ASGITransport(app),
            base_url="http://test",
            headers={"Authorization": "Bearer fixture"},
        ) as client,
    ):
        result = (await client.post("/asr", files={"file": ("input.wav", wav_fixture())})).json()
    assert result["text"] == "да" and result["needs_repeat"] is True
    assert result["avg_logprob"] == -1.3 and result["quality_source"] == "whisper_logprob"
    assert "confidence" not in result


async def test_cancel_tasks_drains_failure_and_redacts_message(caplog):
    async def failing():
        raise RuntimeError("private patient and password")

    task = asyncio.create_task(failing())
    await asyncio.sleep(0)
    await cancel_tasks([task, task])
    assert "RuntimeError" in caplog.text
    assert "private patient" not in caplog.text and "password" not in caplog.text


async def test_shutdown_closes_inference_when_transport_cleanup_fails(services, monkeypatch):
    closed = []

    async def start(self):
        pass

    original_close = Bridge.close

    async def close(self):
        await original_close(self)
        raise OSError("fixture failure")

    async def service_close(*args):
        closed.append(True)

    monkeypatch.setattr(Bridge, "start", start)
    monkeypatch.setattr(Bridge, "close", close)
    monkeypatch.setattr(services, "close", service_close)
    app = create_app(Settings(telephony=True), services)
    with pytest.raises(OSError):
        async with app.router.lifespan_context(app):
            pass
    assert closed
