#!/usr/bin/env python3
"""
Интерактивный голосовой клиент "Голос в Голос" (Voice-to-Voice).
Пайплайн:
Микрофон (arecord) -> faster-whisper (/asr) -> Go Core FSM + Gemini 3.5 (/api/dialogue/turn) -> Silero TTS (/tts) -> Динамики (aplay).
"""

import os
import sys
import time
import tempfile
import subprocess
import requests

AI_BRIDGE_URL = "http://localhost:8000"
CORE_SERVER_URL = "http://localhost:8080"

COLOR_CYAN = "\033[36m"
COLOR_GREEN = "\033[32m"
COLOR_YELLOW = "\033[33m"
COLOR_RED = "\033[31m"
COLOR_BLUE = "\033[34m"
COLOR_RESET = "\033[0m"

def play_audio(wav_bytes: bytes):
    """Воспроизводит аудиобайт через aplay или ffplay"""
    with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as f:
        f.write(wav_bytes)
        temp_path = f.name
    try:
        subprocess.run(["aplay", "-q", temp_path], check=False)
    except Exception:
        try:
            subprocess.run(["ffplay", "-nodisp", "-autoexit", "-loglevel", "quiet", temp_path], check=False)
        except Exception:
            pass
    finally:
        if os.path.exists(temp_path):
            os.remove(temp_path)

def speak_bot(text: str) -> bytes:
    """Синтезирует речь через Silero TTS на GPU и воспроизводит"""
    t0 = time.time()
    try:
        resp = requests.post(f"{AI_BRIDGE_URL}/tts", json={"text": text}, timeout=10)
        tts_lat = int((time.time() - t0) * 1000)
        if resp.status_code == 200:
            print(f"{COLOR_GREEN}[БОТ ({tts_lat}мс)]: {text}{COLOR_RESET}")
            play_audio(resp.content)
            return resp.content
        else:
            print(f"{COLOR_RED}[TTS Ошибка]: {resp.text}{COLOR_RESET}")
    except Exception as e:
        print(f"{COLOR_RED}[TTS Ошибка сети]: {e}{COLOR_RESET}")
    return b""

def record_mic(duration_sec=4) -> bytes:
    """Записывает с микрофона 16 кГц mono WAV через arecord"""
    with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as f:
        temp_path = f.name

    try:
        # arecord 16000Hz mono S16_LE
        subprocess.run([
            "arecord", "-d", str(duration_sec), "-r", "16000", "-c", "1", "-f", "S16_LE", "-q", temp_path
        ], check=True)
        with open(temp_path, "rb") as f:
            wav_bytes = f.read()
        return wav_bytes
    finally:
        if os.path.exists(temp_path):
            os.remove(temp_path)

def transcribe(wav_bytes: bytes) -> tuple[str, int]:
    """Отправляет аудио в ASR faster-whisper"""
    t0 = time.time()
    try:
        files = {"file": ("mic.wav", wav_bytes, "audio/wav")}
        resp = requests.post(f"{AI_BRIDGE_URL}/asr", files=files, timeout=10)
        lat = int((time.time() - t0) * 1000)
        if resp.status_code == 200:
            data = resp.json()
            return data.get("text", "").strip(), lat
    except Exception as e:
        print(f"{COLOR_RED}[ASR Ошибка]: {e}{COLOR_RESET}")
    return "", 0

def send_dialogue_turn(call_id: str, text: str) -> dict:
    """Отправляет распознанную реплику в Go Core FSM"""
    try:
        resp = requests.post(f"{CORE_SERVER_URL}/api/dialogue/turn", json={
            "call_id": call_id,
            "text": text
        }, timeout=10)
        if resp.status_code == 200:
            return resp.json()
    except Exception as e:
        print(f"{COLOR_RED}[FSM Ошибка]: {e}{COLOR_RESET}")
    return {}

def main():
    print(COLOR_CYAN + "=" * 65 + COLOR_RESET)
    print(COLOR_CYAN + "   ТЕСТИРОВАНИЕ «ГОЛОС В ГОЛОС» (VOICE-TO-VOICE PIPELINE)   " + COLOR_RESET)
    print(COLOR_CYAN + "=" * 65 + COLOR_RESET)
    print("Проверка доступности сервисов:")
    
    # 1. Проверка AI Bridge (TTS + ASR)
    try:
        r_ai = requests.get(f"{AI_BRIDGE_URL}/health", timeout=3).json()
        print(f"  [AI Bridge]: {COLOR_GREEN}OK{COLOR_RESET} (GPU: {r_ai.get('gpu_name')}, ASR: {r_ai.get('asr_ready')}, TTS: {r_ai.get('tts_ready')})")
    except Exception:
        print(f"  [AI Bridge]: {COLOR_RED}НЕДОСТУПЕН (запустите ai-worker){COLOR_RESET}")
        return

    # 2. Проверка Go Core
    try:
        r_core = requests.get(f"{CORE_SERVER_URL}/healthz", timeout=3).json()
        print(f"  [Go Core]:   {COLOR_GREEN}OK{COLOR_RESET} (Врачей: {r_core.get('doctors')})")
    except Exception:
        print(f"  [Go Core]:   {COLOR_RED}НЕДОСТУПЕН (запустите go-core bin/server){COLOR_RESET}")
        return

    print("\nИнструкция:")
    print("  * Нажмите [Enter], чтобы начать запись реплики в микрофон (запись 4 сек).")
    print("  * Скажите реплику (например: 'Запишите меня к терапевту' или 'Со скольки работает поликлиника?').")
    print("  * Для выхода введите 'q' или скажите 'до свидания'.\n")

    # 3. Старт диалога (Приветствие бота)
    init_resp = requests.get(f"{CORE_SERVER_URL}/api/dialogue/start", timeout=5).json()
    call_id = init_resp["call_id"]
    bot_greeting = init_resp["bot_speech"]

    speak_bot(bot_greeting)

    while True:
        try:
            cmd = input(f"\n{COLOR_YELLOW}[Нажмите Enter для записи речи (или 'q' для выхода)]: {COLOR_RESET}").strip()
            if cmd.lower() in ["q", "exit", "quit"]:
                print(COLOR_BLUE + "Завершение диалога." + COLOR_RESET)
                break

            print(COLOR_RED + "🎙️  [ИДЁТ ЗАПИСЬ С МИКРОФОНА (4 сек)... Говорите в микрофон!]" + COLOR_RESET)
            audio = record_mic(duration_sec=4)

            # Распознавание речи (ASR)
            user_text, asr_lat = transcribe(audio)
            if not user_text:
                print(COLOR_YELLOW + "⚠️ Речь не обнаружена или слишком тихо. Попробуйте ещё раз." + COLOR_RESET)
                continue

            print(f"{COLOR_YELLOW}[ВЫ ({asr_lat}мс)]: \"{user_text}\"{COLOR_RESET}")

            # Обработка в Go Core (FSM + Gemini NLU)
            t_fsm = time.time()
            turn_res = send_dialogue_turn(call_id, user_text)
            fsm_lat = int((time.time() - t_fsm) * 1000)

            bot_reply = turn_res.get("bot_speech", "")
            state = turn_res.get("state", "")
            should_hangup = turn_res.get("should_hangup", False)
            transfer = turn_res.get("transfer_target", "")

            print(f"{COLOR_BLUE}[FSM ({fsm_lat}мс)]: Состояние = {state}" + (f", Перевод = {transfer}" if transfer else "") + COLOR_RESET)

            if bot_reply:
                speak_bot(bot_reply)

            if should_hangup:
                print(COLOR_RED + "\n[ТЕЛЕФОНИЯ]: Звонок завершён со стороны ассистента." + COLOR_RESET)
                break

        except KeyboardInterrupt:
            print(COLOR_BLUE + "\nДиалог прерван пользователем." + COLOR_RESET)
            break

if __name__ == "__main__":
    main()
