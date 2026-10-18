"""Bounded local recognition. Model access is serialized independently of HTTP."""

import io
import os
from math import gcd

import numpy as np
import soundfile as sf
from faster_whisper import WhisperModel
from scipy.signal import resample_poly


class FasterWhisperASR:
    def __init__(self, model_size: str = "small", device: str = "cpu"):
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
        if len(audio) == 0:
            return ""
        if len(audio) > 30 * 16000:
            audio = audio[: 30 * 16000]
        if not np.isfinite(audio).all():
            audio = np.nan_to_num(audio, nan=0.0, posinf=0.0, neginf=0.0)
        if len(audio) == 0 or np.abs(audio).max() < 1e-4:
            return ""
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
        try:
            data = np.frombuffer(pcm_16k_bytes, dtype="<i2").astype(np.float32) / 32768
            return self._transcribe(data)
        except Exception:
            return ""

    def transcribe_wav(self, wav_bytes: bytes) -> str:
        if not wav_bytes:
            return ""
        sample_rate = 16000
        data = np.array([], dtype=np.float32)
        try:
            with sf.SoundFile(io.BytesIO(wav_bytes)) as source:
                sample_rate = source.samplerate
                data = source.read(dtype="float32", always_2d=True).mean(axis=1)
        except Exception:
            try:
                data = np.frombuffer(wav_bytes, dtype="<i2").astype(np.float32) / 32768
                sample_rate = 8000
            except Exception:
                return ""

        if len(data) == 0:
            return ""
        if sample_rate != 16000:
            factor = gcd(sample_rate, 16000)
            data = resample_poly(data, 16000 // factor, sample_rate // factor).astype(np.float32)
        return self._transcribe(data)
