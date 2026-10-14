import os
import io
import torch
import soundfile as sf
import scipy.signal
from src.normalizer import normalize_russian_text

class SileroTTSService:
    def __init__(self, device="cuda" if torch.cuda.is_available() else "cpu", speaker="kseniya"):
        self.device = torch.device(device)
        self.speaker = speaker
        self.sample_rate = 24000  # 24 кГц - высокое студийное качество речи
        print(f"[TTS] Загрузка Silero TTS v4 на {self.device} (голос: {self.speaker}, частота: {self.sample_rate} Гц)...")
        
        # Загружаем модель Silero TTS v4
        self.model, _ = torch.hub.load(
            repo_or_dir='snakers4/silero-models',
            model='silero_tts',
            language='ru',
            speaker='v4_ru',
            trust_repo=True
        )
        self.model.to(self.device)
        print("[TTS] Silero TTS v4 успешно загружен.")

    def synthesize(self, text: str, sample_rate: int = 24000, speaker: str = None) -> bytes:
        """Синтезирует русский текст в WAV байты с автоматической нормализацией чисел и времени"""
        if not text:
            return b""

        # 1. Текстовая нормализация (числа, даты, время, спецсимволы -> в русские слова)
        normalized_text = normalize_russian_text(text)
        spk = speaker if speaker else self.speaker

        # Silero поддерживает [8000, 24000, 48000]
        # Синтезируем на 24 кГц для естественного тембра
        model_sr = 24000 if sample_rate >= 16000 else 8000
        
        audio_tensor = self.model.apply_tts(
            text=normalized_text,
            speaker=spk,
            sample_rate=model_sr,
            put_accent=True,
            put_yo=True
        )
        
        audio_np = audio_tensor.detach().cpu().numpy()

        # Ресемплинг, если требуется специфическая частота (например, 16000)
        if sample_rate != model_sr:
            audio_np = scipy.signal.resample(audio_np, int(len(audio_np) * sample_rate / model_sr)).astype('float32')

        buf = io.BytesIO()
        sf.write(buf, audio_np, sample_rate, format='WAV', subtype='PCM_16')
        return buf.getvalue()

    def synthesize_pcm(self, text: str, sample_rate: int = 8000, speaker: str = None) -> bytes:
        """Синтезирует сырые PCM 16-bit Little Endian байты для телефонии"""
        if not text:
            return b""
        
        normalized_text = normalize_russian_text(text)
        spk = speaker if speaker else self.speaker

        audio_tensor = self.model.apply_tts(
            text=normalized_text,
            speaker=spk,
            sample_rate=8000,
            put_accent=True,
            put_yo=True
        )
        
        audio_int16 = (audio_tensor.detach().cpu().clamp(-1.0, 1.0) * 32767).to(torch.int16).numpy()
        return audio_int16.tobytes()
