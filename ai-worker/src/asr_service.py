"""Bounded local recognition. Model access is serialized independently of HTTP."""

import io
import os
import threading
from math import gcd

import numpy as np
import soundfile as sf
from faster_whisper import WhisperModel
from scipy.signal import resample_poly


class FasterWhisperASR:
    def __init__(self, model_size: str = "small", device: str = "cpu"):
        self._lock = threading.Lock()
        self.model = WhisperModel(
            os.getenv("ASR_MODEL", model_size),
            device=device,
            compute_type="float16" if device == "cuda" else "int8",
            revision=os.getenv("ASR_MODEL_REVISION")
            or (
                "536b0662742c02347bc0e980a01041f333bce120"
                if os.getenv("ASR_MODEL", model_size) == "small"
                else None
            ),
            local_files_only=os.getenv("MODELS_OFFLINE", "false") == "true",
        )
        # Whisper returns a lazy iterator: consuming it is necessary for warm-up.
        segments, _ = self.model.transcribe(np.zeros(16000, dtype=np.float32), language="ru")
        list(segments)

    def _transcribe(self, audio: np.ndarray) -> str:
        if not 0 < len(audio) <= 30 * 16000:
            raise ValueError("audio duration must be between zero and thirty seconds")
        if not np.isfinite(audio).all():
            raise ValueError("audio contains invalid samples")
        with self._lock:
            segments, _ = self.model.transcribe(
                audio,
                language="ru",
                beam_size=1,
                temperature=0.0,
                condition_on_previous_text=False,
                initial_prompt="Поликлиника. Запись, перенос, отмена, фамилия, имя, дата рождения.",
            )
            return " ".join(segment.text.strip() for segment in segments)

    def transcribe(self, pcm_16k_bytes: bytes) -> str:
        if not pcm_16k_bytes:
            return ""
        return self._transcribe(
            np.frombuffer(pcm_16k_bytes, dtype="<i2").astype(np.float32) / 32768
        )

    def transcribe_wav(self, wav_bytes: bytes) -> str:
        with sf.SoundFile(io.BytesIO(wav_bytes)) as source:
            if source.samplerate < 8000 or source.samplerate > 96000:
                raise ValueError("unsupported sample rate")
            if source.frames <= 0 or source.frames / source.samplerate > 30:
                raise ValueError("audio must be at most thirty seconds")
            if source.channels not in {1, 2}:
                raise ValueError("audio must be mono or stereo")
            sample_rate = source.samplerate
            data = source.read(dtype="float32", always_2d=True).mean(axis=1)
        if sample_rate != 16000:
            factor = gcd(sample_rate, 16000)
            data = resample_poly(data, 16000 // factor, sample_rate // factor).astype(np.float32)
        return self._transcribe(data)
