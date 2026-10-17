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


class RemoteWhisperASR:
    def __init__(
        self,
        api_url: str | None = None,
        api_key: str | None = None,
        model: str = "openai/whisper-large-v3",
        timeout: float = 15.0,
        fallback: FasterWhisperASR | None = None,
    ):
        import requests

        self._requests = requests
        self.api_url = api_url or os.getenv(
            "ASR_API_URL", "https://hub.gigaschool.ru/api/v1/llm/v1/audio/transcriptions"
        )
        self.api_key = api_key or os.getenv("ASR_API_KEY") or os.getenv("LLM_API_KEY")
        self.model = os.getenv("ASR_API_MODEL", model)
        self.timeout = timeout
        self.fallback = fallback

    def transcribe_wav(self, wav_bytes: bytes) -> str:
        if not wav_bytes:
            return ""
        if self.api_url and self.api_key:
            try:
                headers = {"Authorization": f"Bearer {self.api_key}"}
                files = {"file": ("audio.wav", wav_bytes, "audio/wav")}
                data = {"model": self.model, "language": "ru"}
                resp = self._requests.post(
                    self.api_url,
                    headers=headers,
                    files=files,
                    data=data,
                    timeout=self.timeout,
                )
                if resp.status_code == 200:
                    return resp.json().get("text", "").strip()
            except Exception:
                pass
        if self.fallback is not None:
            return self.fallback.transcribe_wav(wav_bytes)
        return ""
