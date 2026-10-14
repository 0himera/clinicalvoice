import os
import sys
import uvicorn

BASE_DIR = os.path.dirname(os.path.abspath(__file__))
if BASE_DIR not in sys.path:
    sys.path.insert(0, BASE_DIR)

if __name__ == "__main__":
    port = int(os.environ.get("AI_WORKER_PORT", 8000))
    host = os.environ.get("AI_WORKER_HOST", "0.0.0.0")
    print(f"[AI-WORKER] Запуск голосового сервиса на {host}:{port}...")
    uvicorn.run("src.voice_bridge:app", host=host, port=port, reload=False)
