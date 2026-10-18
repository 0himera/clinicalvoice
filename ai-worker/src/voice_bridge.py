"""HTTP speech API plus the locally bound Asterisk transports."""

import asyncio
import logging
import secrets
import time
from contextlib import asynccontextmanager
from typing import Annotated, Any, Literal

from fastapi import FastAPI, File, HTTPException, Request, Response, UploadFile
from pydantic import BaseModel, ConfigDict, Field, field_validator

from src.limits import BodyLimit
from src.services import Services, load_services
from src.settings import Settings

logger = logging.getLogger(__name__)
MAX_AUDIO_BYTES = 4 << 20


def create_app(settings: Settings | None = None, services: Services | None = None) -> FastAPI:
    settings = settings or Settings.from_env()

    @asynccontextmanager
    async def lifespan(app: FastAPI):
        app.state.services = services or await asyncio.to_thread(
            load_services, settings.device, settings
        )
        bridge = None
        if settings.telephony:
            from src.telephony import Bridge

            bridge = Bridge(settings, app.state.services)
            await bridge.start()
        try:
            yield
        finally:
            if bridge is not None:
                await bridge.close()
            await app.state.services.close()

    app = FastAPI(title="Tochka Voice speech service", version="0.2.0", lifespan=lifespan)
    app.add_middleware(BodyLimit)

    @app.middleware("http")
    async def authenticate(request: Request, call_next):
        path = request.url.path.rstrip("/")
        if settings.api_token and path not in ("/health", "/healthz", "/readyz"):
            expected = "Bearer " + settings.api_token
            if not secrets.compare_digest(request.headers.get("authorization", ""), expected):
                return Response(status_code=401, headers={"WWW-Authenticate": "Bearer"})
        response = await call_next(request)
        response.headers["Cache-Control"] = "no-store"
        response.headers["X-Content-Type-Options"] = "nosniff"
        return response

    @app.get("/health")
    @app.get("/healthz")
    @app.get("/readyz")
    async def health():
        ready = getattr(app.state, "services", None)
        if ready is None:
            raise HTTPException(503, "models are not ready")
        return {
            "status": "ok",
            "device": ready.device,
            "asr_ready": True,
            "tts_ready": True,
            "decision_ready": getattr(ready, "decision", None) is not None,
        }

    @app.post("/asr")
    async def transcribe_audio(file: Annotated[UploadFile, File()]):
        audio = await file.read(MAX_AUDIO_BYTES + 1)
        await file.close()
        if len(audio) > MAX_AUDIO_BYTES:
            raise HTTPException(413, "audio exceeds four MiB")
        if not audio or len(audio) < 12:
            raise HTTPException(422, "invalid audio file")
        if file.filename and file.filename.endswith(".wav") and not audio.startswith(b"RIFF"):
            raise HTTPException(422, "a WAV file is required")
        started = time.perf_counter()
        try:
            async with asyncio.timeout(40):
                text = await app.state.services.transcribe(audio)
        except (ValueError, RuntimeError) as exc:
            logger.warning("ASR rejected audio (%s)", type(exc).__name__)
            raise HTTPException(422, "audio could not be recognized") from None
        except TimeoutError:
            raise HTTPException(503, "recognition capacity is busy") from None
        return {"text": text, "latency_ms": round((time.perf_counter() - started) * 1000)}

    @app.post("/tts")
    async def synthesize_speech(req: SynthesisRequest):
        try:
            async with asyncio.timeout(40):
                audio = await app.state.services.synthesize(req.text, req.sample_rate, req.speaker)
        except (ValueError, RuntimeError) as exc:
            logger.warning("TTS failed (%s)", type(exc).__name__)
            raise HTTPException(422, "text could not be synthesized") from None
        except TimeoutError:
            raise HTTPException(503, "synthesis capacity is busy") from None
        return Response(audio, media_type="audio/wav")

    @app.post("/v1/decide")
    @app.post("/decide")
    async def evaluate_decision(req: DecisionRequestBody):
        ready = getattr(app.state, "services", None)
        if ready is None or getattr(ready, "decision", None) is None:
            raise HTTPException(503, "decision engine is not ready")
        t0 = time.perf_counter()
        try:
            async with asyncio.timeout(5):
                answers = await ready.decision.decide_async(req.state, req.questions)
                if (
                    isinstance(answers, dict)
                    and "answers" in answers
                    and isinstance(answers["answers"], dict)
                ):
                    answers = answers["answers"]
        except TimeoutError:
            raise HTTPException(504, "decision evaluation timed out") from None
        except Exception as exc:
            logger.warning("Decision engine failed (%s): %s", type(exc).__name__, exc)
            raise HTTPException(500, f"decision engine error: {exc}") from None
        elapsed_ms = round((time.perf_counter() - t0) * 1000, 2)
        logger.info(
            "Decision request evaluated in %.1f ms (%d questions)", elapsed_ms, len(req.questions)
        )
        return {"model": "THX-01", "answers": answers, "latency_ms": elapsed_ms}

    return app


class DecisionRequestBody(BaseModel):
    model_config = ConfigDict(extra="allow")
    state: Any
    questions: dict[str, Any]


class SynthesisRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")
    text: str = Field(min_length=1, max_length=2000)
    speaker: Literal["aidar", "baya", "kseniya", "xenia", "eugene", "random"] | None = None
    sample_rate: Literal[8000, 16000, 24000, 48000] = 24000

    @field_validator("text")
    @classmethod
    def nonempty(cls, value):
        if not value.strip():
            raise ValueError("text must not be blank")
        return value.strip()


app = create_app()
