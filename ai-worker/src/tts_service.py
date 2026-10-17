"""Local Russian synthesis with explicit voices and telephone sample rates."""

import io
import os
import re
import threading
from math import gcd
from pathlib import Path

import numpy as np
import soundfile as sf
import torch
from scipy.signal import resample_poly

from src.normalizer import normalize_russian_text

SILERO_REVISION = "e382298387332f2bf461fe6eadfde99104611b0c"


class SileroTTSService:
    def __init__(self, device: str = "cpu", speaker: str = "kseniya"):
        self.device = torch.device(device)
        self.speaker = os.getenv("TTS_SPEAKER", speaker)
        self._lock = threading.Lock()
        model_version = os.getenv("TTS_MODEL_VERSION", "v5_ru")
        repository = f"snakers4/silero-models:{SILERO_REVISION}"
        source = "github"
        if os.getenv("MODELS_OFFLINE", "false") == "true":
            local = Path(torch.hub.get_dir()) / f"snakers4_silero-models_{SILERO_REVISION}"
            for required in ("hubconf.py", "models.yml"):
                if not (local / required).is_file():
                    raise RuntimeError("offline Silero cache is incomplete")
            repository, source = str(local), "local"
        self.model, _ = torch.hub.load(
            repository,
            "silero_tts",
            source=source,
            language="ru",
            speaker=model_version,
            trust_repo=True,
            skip_validation=True,
        )
        self.model.to(self.device)

    def synthesize(self, text: str, sample_rate: int = 24000, speaker: str | None = None) -> bytes:
        if not text:
            raise ValueError("text is empty")
        if sample_rate not in {8000, 16000, 24000, 48000}:
            raise ValueError("unsupported sample rate")
        voice = speaker or self.speaker
        if voice not in {"aidar", "baya", "kseniya", "xenia", "eugene", "random"}:
            raise ValueError("unsupported voice")
        normalized = normalize_russian_text(text)
        parts = re.split(r"(?<=[.!?;])\s+", normalized)
        chunks: list[str] = []
        for part in parts:
            if len(part) > 900:
                raise ValueError("sentence exceeds synthesis limit")
            if chunks and len(chunks[-1]) + len(part) < 800:
                chunks[-1] += " " + part
            else:
                chunks.append(part)
        model_rate = 24000 if sample_rate == 16000 else sample_rate
        with self._lock, torch.inference_mode():
            audio = np.concatenate(
                [
                    self.model.apply_tts(
                        text=chunk,
                        speaker=voice,
                        sample_rate=model_rate,
                        put_accent=True,
                        put_yo=True,
                    )
                    .detach()
                    .cpu()
                    .numpy()
                    for chunk in chunks
                ]
            )
        if model_rate != sample_rate:
            factor = gcd(model_rate, sample_rate)
            audio = resample_poly(audio, sample_rate // factor, model_rate // factor)
        buffer = io.BytesIO()
        sf.write(buffer, audio, sample_rate, format="WAV", subtype="PCM_16")
        return buffer.getvalue()

    def synthesize_pcm(
        self, text: str, sample_rate: int = 8000, speaker: str | None = None
    ) -> bytes:
        wav = self.synthesize(text, sample_rate=sample_rate, speaker=speaker)
        audio, _ = sf.read(io.BytesIO(wav), dtype="int16")
        return audio.astype("<i2").tobytes()
