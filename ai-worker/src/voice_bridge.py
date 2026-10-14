import io
import os
import sys
import time
from contextlib import asynccontextmanager
from fastapi import FastAPI, UploadFile, File, Form, Response
from fastapi.middleware.cors import CORSMiddleware
from pydantic import BaseModel

# Гарантируем, что корневая директория ai-worker доступна в sys.path
BASE_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if BASE_DIR not in sys.path:
    sys.path.insert(0, BASE_DIR)

asr_service = None
tts_service = None
vad_service = None

@asynccontextmanager
async def lifespan(app: FastAPI):
    global asr_service, tts_service, vad_service
    import torch
    device = "cuda" if torch.cuda.is_available() else "cpu"
    print(f"[AI-WORKER] Запуск на устройстве: {device}")
    
    try:
        from src.tts_service import SileroTTSService
        tts_service = SileroTTSService(device=device)
    except Exception as e:
        print(f"[AI-WORKER] Ошибка загрузки TTS: {e}")

    try:
        from src.asr_service import FasterWhisperASR
        # Загружаем small модель для баланса качества и скорости (<200 мс на реплику)
        asr_service = FasterWhisperASR(model_size="small", device=device)
    except Exception as e:
        print(f"[AI-WORKER] Ошибка загрузки ASR: {e}")

    try:
        from src.vad_service import SileroVADService
        vad_service = SileroVADService(device="cpu")
    except Exception as e:
        print(f"[AI-WORKER] Ошибка загрузки VAD: {e}")

    yield

app = FastAPI(title="Voice Bridge AI Worker", version="1.0.0", lifespan=lifespan)

app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

class SynthesisRequest(BaseModel):
    text: str
    speaker: str = "kseniya"

@app.get("/health")
def health():
    import torch
    return {
        "status": "ok",
        "cuda_available": torch.cuda.is_available(),
        "gpu_name": torch.cuda.get_device_name(0) if torch.cuda.is_available() else "N/A",
        "asr_ready": asr_service is not None,
        "tts_ready": tts_service is not None,
        "vad_ready": vad_service is not None
    }

@app.post("/asr")
async def transcribe_audio(file: UploadFile = File(...)):
    """Принимает WAV / PCM аудиофайл от микрофона или Asterisk и возвращает распознанный текст"""
    if asr_service is None:
        return {"error": "ASR не инициализирован", "text": ""}
    
    audio_bytes = await file.read()
    start_t = time.time()
    text = asr_service.transcribe_wav(audio_bytes)
    duration_ms = int((time.time() - start_t) * 1000)
    
    return {
        "text": text,
        "latency_ms": duration_ms
    }

@app.post("/tts")
def synthesize_speech(req: SynthesisRequest):
    """Синтезирует речь через Silero TTS и возвращает аудиофайл WAV"""
    if tts_service is None:
        return Response(content=b"", media_type="audio/wav")
    
    start_t = time.time()
    wav_bytes = tts_service.synthesize(req.text)
    duration_ms = int((time.time() - start_t) * 1000)
    print(f"[TTS] Синтезировано за {duration_ms} мс: '{req.text[:30]}...'")
    
    return Response(content=wav_bytes, media_type="audio/wav")

if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8000, reload=False)
