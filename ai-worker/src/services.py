"""The inference boundary is injectable so transport tests need no model downloads."""

import asyncio
from dataclasses import dataclass, field
from typing import Any, Protocol


class ASR(Protocol):
    def transcribe_wav(self, audio: bytes) -> str: ...


class TTS(Protocol):
    def synthesize(
        self, text: str, sample_rate: int = 24000, speaker: str | None = None
    ) -> bytes: ...


class VAD(Protocol):
    def new_stream(self): ...


class Decision(Protocol):
    def decide(self, state, questions): ...
    async def decide_async(self, state, questions): ...
    def warmup(self): ...


@dataclass
class Services:
    asr: ASR
    tts: TTS
    vad: VAD
    decision: Decision | None = None
    device: str = "test"
    asr_capacity: asyncio.Semaphore = field(default_factory=lambda: asyncio.Semaphore(2))
    tts_capacity: asyncio.Semaphore = field(default_factory=lambda: asyncio.Semaphore(2))
    _work: set[asyncio.Task] = field(default_factory=set)

    @property
    def capacity(self) -> asyncio.Semaphore:
        return self.asr_capacity

    async def _run(self, sem: asyncio.Semaphore, function, *args):
        await sem.acquire()
        task = asyncio.create_task(asyncio.to_thread(function, *args))
        self._work.add(task)

        def finished(done):
            sem.release()
            self._work.discard(done)
            if not done.cancelled():
                done.exception()  # retrieve errors even if the HTTP caller disconnected

        task.add_done_callback(finished)
        # Cancelling a coroutine does not stop a running inference thread. Its
        # permit must remain held until that thread actually finishes.
        return await asyncio.shield(task)

    async def transcribe(self, audio: bytes) -> str:
        return await self._run(self.asr_capacity, self.asr.transcribe_wav, audio)

    async def synthesize(
        self, text: str, sample_rate: int = 24000, speaker: str | None = None
    ) -> bytes:
        return await self._run(self.tts_capacity, self.tts.synthesize, text, sample_rate, speaker)

    async def close(self):
        await asyncio.gather(*self._work, return_exceptions=True)


def load_services(requested_device: str, settings: Any = None) -> Services:
    import os

    import torch

    from src.asr_service import FasterWhisperASR, RemoteWhisperASR
    from src.decision import create_decision_engine
    from src.tts_service import SileroTTSService
    from src.vad_service import SileroVADService

    device = requested_device
    if device == "auto":
        device = "cuda" if torch.cuda.is_available() else "cpu"
    if device == "cuda" and not torch.cuda.is_available():
        raise RuntimeError("CUDA was requested but is unavailable")

    decision_engine = create_decision_engine(settings)
    decision_engine.warmup()

    tts = SileroTTSService(device=device)
    try:
        tts.synthesize("прогрев", 8000)
    except Exception:
        pass

    asr_provider = os.getenv("ASR_PROVIDER", "local").lower()
    if asr_provider in ("api", "remote") or (
        os.getenv("ASR_API_URL") and (os.getenv("ASR_API_KEY") or os.getenv("LLM_API_KEY"))
    ):
        fallback = None
        if os.getenv("ASR_LOCAL_FALLBACK", "false") == "true":
            try:
                fallback = FasterWhisperASR(device=device)
            except Exception:
                pass
        asr = RemoteWhisperASR(fallback=fallback)
    else:
        asr = FasterWhisperASR(device=device)

    return Services(
        asr=asr,
        tts=tts,
        vad=SileroVADService(),
        decision=decision_engine,
        device=device,
    )
