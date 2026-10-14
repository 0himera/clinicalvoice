#!/usr/bin/env python3
"""
Автоматическая симуляция диалога Голос-в-Голос.
Генерирует аудио реплики пациента, прогоняет через полный цикл:
WAV -> ASR -> Go Core (FSM + Gemini 3.5) -> TTS -> aplay.
Сохраняет полную сквозную аудиозапись разговора в audio_output/full_dialogue.wav.
"""

import sys
import time
import requests
import subprocess
import wave
import io
import os

AI_URL = "http://localhost:8000"
CORE_URL = "http://localhost:8080"
OUTPUT_FILE = "audio_output/full_dialogue.wav"

TEST_REPLICAS = [
    "Здравствуйте, подскажите график работы поликлиники?",
    "А со скольки забор крови в процедурном?",
    "Запишите меня к терапевту, я Иванов Иван Иванович 15 апреля 1985 года",
    "Спасибо, до свидания"
]

def concat_wav_streams(wav_list, pause_sec=0.7, out_path=OUTPUT_FILE):
    """Склеивает аудиопотоки в единый аудиофайл с паузами"""
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    frames = []
    params = None

    for i, wb in enumerate(wav_list):
        with wave.open(io.BytesIO(wb), 'rb') as w:
            if params is None:
                params = w.getparams()
            frames.append(w.readframes(w.getnframes()))
            
            # Добавляем паузу между репликами (тишина)
            if i < len(wav_list) - 1:
                silence_frames = int(params.framerate * pause_sec)
                silence_bytes = b'\x00' * (silence_frames * params.sampwidth * params.nchannels)
                frames.append(silence_bytes)

    with wave.open(out_path, 'wb') as out_w:
        out_w.setparams(params)
        out_w.writeframes(b''.join(frames))

    duration_sec = sum(len(f) for f in frames) / (params.framerate * params.sampwidth * params.nchannels)
    return duration_sec

def main():
    print("=" * 65)
    print(" АВТОМАТИЧЕСКАЯ ВЕРИФИКАЦИЯ ПАЙПЛАЙНА ГОЛОС-В-ГОЛОС ")
    print("=" * 65)

    all_audio_tracks = []

    # 1. Старт сессии
    s_resp = requests.get(f"{CORE_URL}/api/dialogue/start").json()
    call_id = s_resp["call_id"]
    bot_greeting = s_resp["bot_speech"]
    print(f"\n[БОТ ПРИВЕТСТВИЕ]: \"{bot_greeting}\"")

    # Синтезируем приветствие бота
    tts_resp = requests.post(f"{AI_URL}/tts", json={"text": bot_greeting, "speaker": "kseniya"})
    all_audio_tracks.append(tts_resp.content)
    print(f"  -> TTS Приветствия синтезировано: {len(tts_resp.content)} байт")

    for i, user_phrase in enumerate(TEST_REPLICAS):
        print(f"\n--- Реплика {i+1} ---")
        print(f"[ПАЦИЕНТ СКАЗАЛ]: \"{user_phrase}\"")

        # 1. Синтезируем голос пациента (baya)
        patient_wav = requests.post(f"{AI_URL}/tts", json={"text": user_phrase, "speaker": "baya"}).content
        all_audio_tracks.append(patient_wav)

        # 2. ASR (faster-whisper)
        t0 = time.time()
        asr_res = requests.post(f"{AI_URL}/asr", files={"file": ("in.wav", patient_wav, "audio/wav")}).json()
        asr_lat = int((time.time() - t0) * 1000)
        recognized_text = asr_res.get("text", "")
        print(f"[ASR Распознано ({asr_lat}мс)]: \"{recognized_text}\"")

        # 3. Go Core + Gemini 3.5 FSM
        t0 = time.time()
        turn_res = requests.post(f"{CORE_URL}/api/dialogue/turn", json={
            "call_id": call_id,
            "text": recognized_text
        }).json()
        fsm_lat = int((time.time() - t0) * 1000)
        bot_reply = turn_res.get("bot_speech", "")
        state = turn_res.get("state", "")
        llm_used = turn_res.get("llm_used", False)
        llm_tag = "⚡ ДА (Gemini 3.5 Flash Lite)" if llm_used else "💨 НЕТ (локальный Regex < 1мс)"
        print(f"[FSM Состояние ({fsm_lat}мс)]: {state} | Вызов LLM: {llm_tag}")
        print(f"[БОТ ОТВЕТ]: \"{bot_reply}\"")

        # 4. Silero TTS (kseniya)
        t0 = time.time()
        bot_wav = requests.post(f"{AI_URL}/tts", json={"text": bot_reply, "speaker": "kseniya"}).content
        tts_lat = int((time.time() - t0) * 1000)
        all_audio_tracks.append(bot_wav)
        print(f"[TTS Синтез ({tts_lat}мс)]: {len(bot_wav)} байт аудио (24 кГц)")

        total_lat = asr_lat + fsm_lat + tts_lat
        print(f"  >> E2E Задержка ответа: {total_lat} мс (ASR: {asr_lat}мс | FSM/LLM: {fsm_lat}мс | TTS: {tts_lat}мс)")

    # Склеиваем полную аудиозапись диалога
    total_dur = concat_wav_streams(all_audio_tracks, pause_sec=0.6, out_path=OUTPUT_FILE)
    abs_path = os.path.abspath(OUTPUT_FILE)

    print("\n" + "=" * 65)
    print(f" ПОЛНАЯ АУДИОЗАПИСЬ ДИАЛОГА СОХРАНЕНА: ")
    print(f" Файл: {abs_path}")
    print(f" Длительность разговора: {total_dur:.1f} сек ({len(all_audio_tracks)} реплик)")
    print("=" * 65)
    print(f"Чтобы послушать запись прямо сейчас в терминале, выполните:")
    print(f"  aplay {OUTPUT_FILE}")
    print(f"или:")
    print(f"  mpv {OUTPUT_FILE}")

if __name__ == "__main__":
    main()
