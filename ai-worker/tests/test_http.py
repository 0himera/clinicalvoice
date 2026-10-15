import httpx

from src.settings import Settings
from src.voice_bridge import create_app


async def test_readiness_auth_voice_and_sample_rate(services):
    app = create_app(Settings(telephony=False, api_token="secret"), services)
    async with (
        app.router.lifespan_context(app),
        httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url="http://test") as client,
    ):
        assert (await client.get("/health")).json()["status"] == "ok"
        assert (await client.post("/tts", json={"text": "Здравствуйте"})).status_code == 401
        response = await client.post(
            "/tts",
            json={"text": "Здравствуйте", "speaker": "baya", "sample_rate": 8000},
            headers={"Authorization": "Bearer secret"},
        )
        assert response.status_code == 200
        assert services.tts.requests == [("Здравствуйте", 8000, "baya")]
        assert response.content.startswith(b"RIFF")


async def test_invalid_audio_and_unknown_fields(services):
    app = create_app(Settings(telephony=False), services)
    async with (
        app.router.lifespan_context(app),
        httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url="http://test") as client,
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
    app = create_app(Settings(telephony=False), services)

    async def chunks():
        yield b'{"text":"'
        yield b"a" * 20000
        yield b'"}'

    async with (
        app.router.lifespan_context(app),
        httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url="http://test") as client,
    ):
        response = await client.post(
            "/tts", content=chunks(), headers={"Content-Type": "application/json"}
        )
        assert response.status_code == 413
