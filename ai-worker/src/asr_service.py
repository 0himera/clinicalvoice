import io
import os
import numpy as np
import scipy.signal
import soundfile as sf
from faster_whisper import WhisperModel

class FasterWhisperASR:
    def __init__(self, model_size="small", device=None, compute_type="float16"):
        self.initial_prompt = (
            "Поликлиника номер семь, запись к врачу, терапевт, педиатр, оториноларинголог, "
            "офтальмолог, хирург, талон, фамилия, имя, отчество, дата рождения."
        )

        if device is None:
            device = "cuda"

        try:
            print(f"[ASR] Инициализация faster-whisper ({model_size}) на {device} ({compute_type})...")
            self.model = WhisperModel(model_size, device=device, compute_type=compute_type)
            # Тестовый прогон на GPU
            _ = self.model.transcribe(np.zeros(16000, dtype=np.float32), language="ru")
            self.device = device
            print(f"[ASR] faster-whisper успешно работает на GPU {device} ({compute_type})!")
        except Exception as e:
            print(f"[ASR] Ошибка CUDA: {e}. Переключение на CPU (int8)...")
            self.model = WhisperModel(model_size, device="cpu", compute_type="int8")
            self.device = "cpu"
            print("[ASR] faster-whisper запущен на CPU (int8)")

    def transcribe(self, pcm_16k_bytes: bytes) -> str:
        """Транскрибирует сырой PCM 16kHz Little Endian"""
        if len(pcm_16k_bytes) == 0:
            return ""

        audio_int16 = np.frombuffer(pcm_16k_bytes, dtype=np.int16)
        audio_float32 = audio_int16.astype(np.float32) / 32768.0

        segments, _ = self.model.transcribe(
            audio_float32,
            language="ru",
            beam_size=1,
            initial_prompt=self.initial_prompt,
            temperature=0.0
        )
        return " ".join([s.text.strip() for s in segments])

    def transcribe_wav(self, wav_bytes: bytes) -> str:
        """Транскрибирует WAV файл любого формата с гарантированным ресемплингом в 16 кГц"""
        with io.BytesIO(wav_bytes) as buf:
            data, sr = sf.read(buf, dtype='float32')

            # Преобразование в моно, если стерео
            if data.ndim > 1:
                data = np.mean(data, axis=1)

            # Обязательный ресемплинг в 16000 Hz, который ожидает Whisper
            if sr != 16000 and len(data) > 0:
                target_len = int(len(data) * 16000 / sr)
                data = scipy.signal.resample(data, target_len).astype(np.float32)

            segments, _ = self.model.transcribe(
                data,
                language="ru",
                beam_size=1,
                initial_prompt=self.initial_prompt,
                temperature=0.0
            )
            return " ".join([s.text.strip() for s in segments])
