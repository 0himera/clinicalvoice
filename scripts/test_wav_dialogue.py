#!/usr/bin/env python3
"""
Скрипт для тестирования диалога через готовые WAV-файлы (без микрофона).
Принимает на вход wav-файл пациента, распознает через faster-whisper,
прогоняет через FSM диалога и синтезирует ответный WAV файл.
"""
import sys
import os
import requests

AI_URL = "http://localhost:8000"

def test_wav(wav_path: str, out_wav_path: str = "/tmp/bot_out.wav"):
    if not os.path.exists(wav_path):
        print(f"[ERROR] Файл {wav_path} не найден!")
        return

    print(f"[1] Отправка {wav_path} в ASR (faster-whisper)...")
    with open(wav_path, "rb") as f:
        resp = requests.post(f"{AI_URL}/asr", files={"file": f})
    
    if resp.status_code != 200:
        print(f"[ERROR] ASR ошибка: {resp.text}")
        return
        
    data = resp.json()
    user_text = data.get("text", "")
    latency = data.get("latency_ms", 0)
    print(f"[ASR] Распознано за {latency} мс: '{user_text}'")

    if not user_text:
        print("[WARN] Речь в файле не обнаружена.")
        return

    # Синтезируем тестовый ответ
    bot_reply = f"Вы сказали: {user_text}. Я записала ваш запрос."
    print(f"[2] Синтез ответа через Silero TTS...")
    tts_resp = requests.post(f"{AI_URL}/tts", json={"text": bot_reply})
    
    if tts_resp.status_code == 200:
        with open(out_wav_path, "wb") as f:
            f.write(tts_resp.content)
        print(f"[3] Ответ сохранён в {out_wav_path} ({len(tts_resp.content)} байт).")
    else:
        print(f"[ERROR] TTS ошибка: {tts_resp.text}")

if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("Использование: python scripts/test_wav_dialogue.py <input.wav> [output.wav]")
    else:
        in_file = sys.argv[1]
        out_file = sys.argv[2] if len(sys.argv) > 2 else "/tmp/bot_out.wav"
        test_wav(in_file, out_file)
