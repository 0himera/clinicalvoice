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
    tts_instances: list[TTS] | None = None
    _work: set[asyncio.Task] = field(default_factory=set)
    tts_pool: asyncio.Queue[TTS] | None = None

    @property
    def capacity(self) -> asyncio.Semaphore:
        return self.asr_capacity

    def _get_tts_pool(self) -> asyncio.Queue[TTS]:
        if self.tts_pool is None:
            self.tts_pool = asyncio.Queue()
            if self.tts_instances:
                for inst in self.tts_instances:
                    self.tts_pool.put_nowait(inst)
            elif self.tts is not None:
                count = getattr(self.tts_capacity, "_value", 2)
                for _ in range(count):
                    self.tts_pool.put_nowait(self.tts)
        return self.tts_pool

    async def _run(self, sem: asyncio.Semaphore | None, function, *args):
        if sem:
            await sem.acquire()
        task = asyncio.create_task(asyncio.to_thread(function, *args))
        self._work.add(task)

        def finished(done):
            if sem:
                sem.release()
            self._work.discard(done)
            if not done.cancelled():
                done.exception()  # retrieve errors even if the HTTP caller disconnected

        task.add_done_callback(finished)
        try:
            return await task
        except asyncio.CancelledError:
            task.cancel()
            raise

    async def transcribe(self, audio: bytes) -> str:
        return await self._run(self.asr_capacity, self.asr.transcribe_wav, audio)

    async def synthesize(
        self, text: str, sample_rate: int = 24000, speaker: str | None = None
    ) -> bytes:
        pool = self._get_tts_pool()
        model = await pool.get()
        try:
            return await self._run(None, model.synthesize, text, sample_rate, speaker)
        finally:
            pool.put_nowait(model)

    async def close(self):
        await asyncio.gather(*self._work, return_exceptions=True)


def load_services(requested_device: str, settings: Any = None) -> Services:
    import logging

    import torch

    from src.asr_service import FasterWhisperASR
    from src.decision import DisabledDecisionEngine, create_decision_engine
    from src.tts_service import SileroTTSService
    from src.vad_service import SileroVADService

    logger = logging.getLogger("ai_worker.services")

    device = requested_device
    if device == "auto":
        device = "cuda" if torch.cuda.is_available() else "cpu"
    if device == "cuda" and not torch.cuda.is_available():
        raise RuntimeError("CUDA was requested but is unavailable")

    # 1. Independent VAD initialization
    logger.info("Initializing VAD service...")
    vad = SileroVADService()

    # 2. Independent TTS initialization with model pool
    logger.info("Initializing TTS service pool on %s...", device)
    tts_capacity_count = 2
    tts_instances = [SileroTTSService(device=device) for _ in range(tts_capacity_count)]
    try:
        tts_instances[0].synthesize("прогрев", 8000)
    except Exception as exc:
        logger.warning("TTS warmup notice: %s", exc)

    # 3. Independent ASR initialization
    logger.info("Initializing FasterWhisper ASR on %s...", device)
    asr = FasterWhisperASR(device=device)

    # 4. Independent Decision Engine initialization (failure does not break core voice pipeline)
    decision_engine = None
    try:
        logger.info("Initializing Decision engine...")
        decision_engine = create_decision_engine(settings)
        decision_engine.warmup()
    except Exception as exc:
        logger.error("Decision engine failed to initialize: %s. Using DisabledDecisionEngine.", exc)
        decision_engine = DisabledDecisionEngine(str(exc))

    return Services(
        asr=asr,
        tts=tts_instances[0],
        tts_instances=tts_instances,
        vad=vad,
        decision=decision_engine,
        device=device,
    )
