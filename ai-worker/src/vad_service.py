import torch
import numpy as np

class SileroVADService:
    def __init__(self, device="cpu", threshold=0.5):
        self.device = torch.device(device)
        self.threshold = threshold
        print(f"[VAD] Загрузка Silero VAD на {self.device}...")
        self.model, utils = torch.hub.load(
            repo_or_dir='snakers4/silero-vad',
            model='silero_vad',
            trust_repo=True
        )
        self.model.to(self.device)
        self.get_speech_timestamps = utils[0]
        print("[VAD] Silero VAD успешно инициализирован.")

    def process_chunk(self, pcm_16k_bytes: bytes) -> float:
        """Оценивает вероятность наличия речи в чанке (размер чанка: 512 сэмплов = 32 мс)"""
        if len(pcm_16k_bytes) == 0:
            return 0.0
        
        # Преобразуем int16 в float32 [-1.0, 1.0]
        audio_int16 = np.frombuffer(pcm_16k_bytes, dtype=np.int16)
        audio_float32 = torch.from_numpy(audio_int16.astype(np.float32) / 32768.0).to(self.device)
        
        # Silero VAD ожидает чанк размером ровно 512 сэмплов для 16 кГц
        if len(audio_float32) < 512:
            audio_float32 = torch.nn.functional.pad(audio_float32, (0, 512 - len(audio_float32)))
        elif len(audio_float32) > 512:
            audio_float32 = audio_float32[:512]
            
        with torch.no_grad():
            speech_prob = self.model(audio_float32, 16000).item()
        return speech_prob

    def is_speech(self, pcm_16k_bytes: bytes) -> bool:
        return self.process_chunk(pcm_16k_bytes) >= self.threshold

    def reset_states(self):
        self.model.reset_states()
