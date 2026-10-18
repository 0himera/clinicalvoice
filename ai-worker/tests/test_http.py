import httpx

from src.settings import Settings
from src.voice_bridge import create_app


async def test_readiness_auth_voice_and_sample_rate(services):
    app = create_app(Settings(telephony=False, api_token="secret"), services)
    async with (
        app.router.lifespan_context(app),
        httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app),
            base_url="http://test",
            headers={"Authorization": "Bearer test-token"},
        ) as client,
    ):
        assert (await client.get("/health")).json()["status"] == "ok"
        assert (
            await client.post("/tts", json={"text": "Здравствуйте"}, headers={"Authorization": ""})
        ).status_code == 401
        response = await client.post(
            "/tts",
            json={"text": "Здравствуйте", "speaker": "baya", "sample_rate": 8000},
            headers={"Authorization": "Bearer secret"},
        )
        assert response.status_code == 200
        assert services.tts.requests == [("Здравствуйте", 8000, "baya")]
        assert response.content.startswith(b"RIFF")


async def test_invalid_audio_and_unknown_fields(services):
    app = create_app(Settings(telephony=False, api_token="test-token"), services)
    async with (
        app.router.lifespan_context(app),
        httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app),
            base_url="http://test",
            headers={"Authorization": "Bearer test-token"},
        ) as client,
    ):
        assert (await client.post("/asr", files={"file": ("bad.wav", b"bad")})).status_code == 422
        assert (await client.post("/tts", json={"text": "", "unknown": True})).status_code == 422
        huge = b"RIFF" + b"\0" * (4 << 20)
        assert (await client.post("/asr", files={"file": ("large.wav", huge)})).status_code == 413
        assert (await client.post("/tts", json={"text": "a" * 17000})).status_code == 413
        assert (
            await client.post("/tts", json={"text": "hello", "speaker": "unknown"})
        ).status_code == 422
        assert (await client.post("/tts", json={"text": "   "})).status_code == 422


async def test_chunked_body_is_bounded_before_decoding(services):
    app = create_app(Settings(telephony=False, api_token="test-token"), services)

    async def chunks():
        yield b'{"text":"'
        yield b"a" * 20000
        yield b'"}'

    async with (
        app.router.lifespan_context(app),
        httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app),
            base_url="http://test",
            headers={"Authorization": "Bearer test-token"},
        ) as client,
    ):
        response = await client.post(
            "/tts", content=chunks(), headers={"Content-Type": "application/json"}
        )
        assert response.status_code == 413


async def test_decide_error_does_not_leak_exception(services):
    class FailingDecision:
        async def decide_async(self, state, questions):
            raise RuntimeError("Database connection string postgres://secret:pwd@host/db failed")

    services.decision = FailingDecision()
    app = create_app(Settings(telephony=False, api_token="test-token"), services)
    async with (
        app.router.lifespan_context(app),
        httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app),
            base_url="http://test",
            headers={"Authorization": "Bearer test-token"},
        ) as client,
    ):
        response = await client.post(
            "/v1/decide",
            json={"state": {"intent": "booking"}, "questions": {"q1": "test"}},
        )
        assert response.status_code == 500
        assert response.json() == {"detail": "Decision engine evaluation failed"}
        assert "Database" not in response.text
        assert "secret" not in response.text


async def test_decide_disconnected_client_returns_499(services, monkeypatch):
    class DummyDecision:
        async def decide_async(self, state, questions):
            return {"answers": {"q1": "a1"}}

    services.decision = DummyDecision()
    app = create_app(Settings(telephony=False, api_token="test-token"), services)

    from fastapi import Request

    async def fake_disconnected(self):
        return True

    monkeypatch.setattr(Request, "is_disconnected", fake_disconnected)

    async with (
        app.router.lifespan_context(app),
        httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app),
            base_url="http://test",
            headers={"Authorization": "Bearer test-token"},
        ) as client,
    ):
        response = await client.post(
            "/v1/decide",
            json={"state": {}, "questions": {}},
        )
        assert response.status_code == 499


async def test_timing_attack_hash_comparison(services):
    app = create_app(Settings(telephony=False, api_token="top_secret_token"), services)
    async with (
        app.router.lifespan_context(app),
        httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app),
            base_url="http://test",
            headers={"Authorization": "Bearer test-token"},
        ) as client,
    ):
        # Incorrect token
        res_bad = await client.post(
            "/tts",
            json={"text": "Привет"},
            headers={"Authorization": "Bearer wrong_token"},
        )
        assert res_bad.status_code == 401
        # Correct token
        res_ok = await client.post(
            "/tts",
            json={"text": "Привет"},
            headers={"Authorization": "Bearer top_secret_token"},
        )
        assert res_ok.status_code == 200


async def test_empty_token_denies_all_protected_endpoints(services):
    app = create_app(Settings(telephony=False), services)
    async with (
        app.router.lifespan_context(app),
        httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url="http://test") as client,
    ):
        assert (await client.get("/health")).status_code == 200
        for path, payload in [
            ("/tts", {"text": "тест"}),
            ("/v1/decide", {"state": {}, "questions": {}}),
        ]:
            assert (await client.post(path, json=payload)).status_code == 401
            assert (
                await client.post(path, json=payload, headers={"Authorization": "Bearer "})
            ).status_code == 401
