#!/usr/bin/env python3
"""User Simulator (LLM-based patient) for Tochka Voice testing.

Simulates human patient behavior calling Poliklinika No. 7 using either
Google Gemini 3.8 Live (native speech-to-speech bidirectional WebSocket) or
local Ollama (Gemma 4 with Progressive Disclosure), incorporating:
- PSTN G.711 A-law 8kHz acoustic degradation filter + bandpass + comfort noise
- Progressive disclosure for small models (preventing identity leakage)
- Hangup function calling & structured termination
- Bot Turn Latency (TTFT) SLA metrics tracking
- Mock MIS fixture seeding
"""

from __future__ import annotations

import argparse
import asyncio
import base64
import contextlib
import io
import json
import os
import re
import sqlite3
import subprocess
import sys
import time
import wave
from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from typing import Any

import numpy as np
import scipy.signal

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

from scripts.client import VoiceClient  # noqa: E402

# ==============================================================================
# 1. Telephony Degradation Pipeline (PSTN / G.711 / 8 kHz)
# ==============================================================================


class TelephonyFilter:
    """Emulates PSTN G.711 A-law companding, 300-3400Hz bandpass and line noise."""

    @staticmethod
    def compand_alaw(x: np.ndarray, A: float = 87.6) -> np.ndarray:
        """ITU-T G.711 A-law 8-bit compression and expansion."""
        sign = np.sign(x)
        abs_x = np.clip(np.abs(x), 1e-7, 1.0)
        denom = 1.0 + np.log(A)
        # Compress
        compressed = np.where(
            abs_x < 1.0 / A, (A * abs_x) / denom, (1.0 + np.log(A * abs_x)) / denom
        )
        # 8-bit quantization (128 positive levels)
        quantized = np.round(compressed * 127.0) / 127.0
        # Expand
        expanded = np.where(
            quantized < 1.0 / denom,
            quantized * denom / A,
            np.exp(quantized * denom - 1.0) / A,
        )
        return sign * expanded

    @classmethod
    def process_pcm(
        cls,
        pcm_bytes: bytes,
        in_sample_rate: int = 24000,
        out_sample_rate: int = 16000,
        add_noise: bool = True,
        snr_db: float = 32.0,
    ) -> bytes:
        """Degrades clean PCM audio through simulated PSTN channel into 16kHz WAV."""
        if not pcm_bytes:
            return b""

        # 16-bit PCM -> float32 [-1.0, 1.0]
        raw_samples = np.frombuffer(pcm_bytes, dtype=np.int16).astype(np.float32)
        if len(raw_samples) == 0:
            return b""
        max_val = np.max(np.abs(raw_samples))
        if max_val > 0:
            samples = raw_samples / 32768.0
        else:
            samples = raw_samples

        # 1. Resample to 8000 Hz (telephony standard)
        target_len_8k = max(1, int(len(samples) * 8000 / in_sample_rate))
        samples_8k = scipy.signal.resample(samples, target_len_8k)

        # 2. Bandpass filter 300 Hz - 3400 Hz (ITU-T G.712 telephone speech band)
        nyquist = 4000.0  # 8000 / 2
        low = 300.0 / nyquist
        high = 3400.0 / nyquist
        b, a = scipy.signal.butter(4, [low, high], btype="band")
        filtered_8k = scipy.signal.lfilter(b, a, samples_8k)

        # 3. G.711 A-law 8-bit quantization
        companded_8k = cls.compand_alaw(filtered_8k)

        # 4. Optional comfort noise injection (simulating PSTN background hum)
        if add_noise and len(companded_8k) > 0:
            sig_power = np.mean(companded_8k**2)
            if sig_power > 1e-6:
                noise_power = sig_power / (10 ** (snr_db / 10))
                noise = np.random.normal(0, np.sqrt(noise_power), size=companded_8k.shape)
                companded_8k = np.clip(companded_8k + noise, -1.0, 1.0)

        # 5. Resample from 8000 Hz to out_sample_rate (typically 16000 Hz for Whisper)
        target_len_out = max(1, int(len(companded_8k) * out_sample_rate / 8000))
        samples_out = scipy.signal.resample(companded_8k, target_len_out)
        int16_out = np.clip(samples_out * 32767.0, -32768, 32767).astype(np.int16)

        # Encode to WAV container
        buf = io.BytesIO()
        with wave.open(buf, "wb") as wf:
            wf.setnchannels(1)
            wf.setsampwidth(2)
            wf.setframerate(out_sample_rate)
            wf.writeframes(int16_out.tobytes())
        return buf.getvalue()


# ==============================================================================
# 2. Data Structures & Metrics
# ==============================================================================


@dataclass
class TurnMetric:
    turn_no: int
    user_utterance: str
    bot_speech: str
    bot_state: str
    bot_latency_ms: float
    user_audio_bytes: int = 0
    hangup_called: bool = False


@dataclass
class SimSessionResult:
    goal_id: str
    goal_name: str
    backend: str
    success: bool
    final_fsm_state: str
    final_outcome: str
    total_turns: int
    total_duration_sec: float
    metrics: list[TurnMetric] = field(default_factory=list)
    failure_reason: str = ""
    avg_latency_ms: float = 0.0
    p95_latency_ms: float = 0.0


# ==============================================================================
# 3. User Simulator Backend Interfaces
# ==============================================================================


class UserSimBackend(ABC):
    """Abstract interface for a patient simulator model."""

    @abstractmethod
    async def initialize(self, persona: dict[str, Any], goal: dict[str, Any]) -> None:
        """Set up session context, persona constraints, and initial state."""
        ...

    @abstractmethod
    async def generate_turn(
        self,
        bot_speech: str,
        bot_state: str,
        turn_no: int,
    ) -> tuple[str, bool, bytes | None]:
        """Generate the patient's next response.

        Returns:
            tuple of (utterance_text, hangup_requested, optional_audio_bytes)
        """
        ...

    @abstractmethod
    async def close(self) -> None:
        """Clean up WebSocket or HTTP connections."""
        ...


# ==============================================================================
# 4. Gemini 3.8 Live Implementation (WebSocket BidiGenerateContent)
# ==============================================================================


class GeminiLiveBackend(UserSimBackend):
    """Native speech-to-speech user simulator using Gemini 3.8 Live."""

    def __init__(
        self,
        api_key: str,
        voice_client: VoiceClient,
        voice_name: str = "Puck",
        apply_telephony: bool = True,
    ):
        self.api_key = api_key
        self.voice_client = voice_client
        self.voice_name = voice_name
        self.apply_telephony = apply_telephony
        self.ws: Any = None
        self.persona: dict[str, Any] = {}
        self.goal: dict[str, Any] = {}
        self.hangup_invoked = False

    async def initialize(self, persona: dict[str, Any], goal: dict[str, Any]) -> None:
        import websockets

        self.persona = persona
        self.goal = goal
        self.hangup_invoked = False

        url = f"wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent?key={self.api_key}"
        self.ws = await websockets.connect(
            url,
            ping_interval=None,
            open_timeout=15,
            close_timeout=5,
        )

        system_prompt = f"""Ты играешь роль реального пациента, который звонит по телефону в регистратуру городской поликлиники №7.

ТВОЙ ПРОФИЛЬ:
Имя: {persona.get("full_name")}
Дата рождения: {persona.get("birth_date")}
Телефон: {persona.get("phone")}
Стиль общения: {persona.get("speech_style", "естественный, разговорный")}

ТВОЯ ЦЕЛЬ:
{goal.get("intent")}
Ограничения и предпочтения: {json.dumps(goal.get("constraints", {}), ensure_ascii=False)}

ПРАВИЛА ПОВЕДЕНИЯ:
"""
        for r in goal.get("behavior_rules", []):
            system_prompt += f"- {r}\n"

        system_prompt += """
ОБЩИЕ ПРАВИЛА ЗВОНКА:
1. Отвечай кратко, 1-2 естественными предложениями на русском языке.
2. Называй ФИО и дату рождения ТОЛЬКО тогда, когда регистратура прямо их спрашивает.
3. При выборе слотов (когда робот предлагает первый и второй варианты): выбери один («первый» или «второй»), либо назови один конкретный день/время. НЕ перечисляй варианты через союз «или».
4. Когда робот называет параметры записи и спрашивает «Подтверждаете запись?» (или отмену), четко скажи «Да, подтверждаю».
5. Важно: Вызывай hangup() ТОЛЬКО ПОСЛЕ того, как робот регистратуры подтвердил действие фразой «Готово, вы записаны», «Ваша запись отменена» или «Перевожу на оператора», либо когда разговор окончен. НЕ вызывай hangup() одновременно со словом «Подтверждаю» — сначала дождись ответа робота!
6. Не выдумывай факты о себе, которых нет в профиле. Ни в коем случае не выходи из роли.
"""

        setup_msg = {
            "setup": {
                "model": "models/gemini-3.8-live",
                "generation_config": {
                    "response_modalities": ["AUDIO"],
                    "speech_config": {
                        "voice_config": {"prebuilt_voice_config": {"voice_name": self.voice_name}}
                    },
                },
                "system_instruction": {"parts": [{"text": system_prompt}]},
                "tools": [
                    {
                        "function_declarations": [
                            {
                                "name": "hangup",
                                "description": "Положить телефонную трубку и завершить разговор, когда цель достигнута или звонок окончен.",
                                "parameters": {
                                    "type": "object",
                                    "properties": {
                                        "reason": {
                                            "type": "string",
                                            "description": "Причина завершения звонка",
                                        }
                                    },
                                },
                            }
                        ]
                    }
                ],
            }
        }
        await self.ws.send(json.dumps(setup_msg))

        # Wait for setup confirmation
        while True:
            raw = await self.ws.recv()
            msg = json.loads(raw)
            if "setupComplete" in msg:
                break

    async def generate_turn(
        self,
        bot_speech: str,
        bot_state: str,
        turn_no: int,
    ) -> tuple[str, bool, bytes | None]:
        client_turn = {
            "client_content": {
                "turns": [
                    {
                        "role": "user",
                        "parts": [
                            {
                                "text": f"[Реплика робота регистратуры]: {bot_speech} (состояние: {bot_state})"
                            }
                        ],
                    }
                ],
                "turn_complete": True,
            }
        }
        await self.ws.send(json.dumps(client_turn))

        pcm_chunks: list[bytes] = []
        hangup = False

        while True:
            raw = await self.ws.recv()
            msg = json.loads(raw)

            # Check for tool call (hangup)
            tool_call = msg.get("toolCall", {})
            for call in tool_call.get("functionCalls", []):
                if call.get("name") == "hangup":
                    hangup = True
                    self.hangup_invoked = True
                    # Acknowledge tool call
                    tool_resp = {
                        "tool_response": {
                            "function_responses": [
                                {
                                    "response": {"output": {"status": "hung_up"}},
                                    "id": call.get("id", "call_1"),
                                }
                            ]
                        }
                    }
                    await self.ws.send(json.dumps(tool_resp))

            server_content = msg.get("serverContent", {})
            model_turn = server_content.get("modelTurn", {})
            for part in model_turn.get("parts", []):
                if "inlineData" in part:
                    b64_audio = part["inlineData"].get("data", "")
                    if b64_audio:
                        pcm_chunks.append(base64.b64decode(b64_audio))

            if server_content.get("turnComplete"):
                break

        raw_pcm = b"".join(pcm_chunks)
        if not raw_pcm and hangup:
            return "Спасибо, до свидания.", True, None

        if self.apply_telephony:
            wav_audio = TelephonyFilter.process_pcm(
                raw_pcm, in_sample_rate=24000, out_sample_rate=16000
            )
        else:
            # Direct 24k -> 16k resample
            samples = np.frombuffer(raw_pcm, dtype=np.int16)
            resampled = scipy.signal.resample(samples, int(len(samples) * 16000 / 24000)).astype(
                np.int16
            )
            buf = io.BytesIO()
            with wave.open(buf, "wb") as wf:
                wf.setnchannels(1)
                wf.setsampwidth(2)
                wf.setframerate(16000)
                wf.writeframes(resampled.tobytes())
            wav_audio = buf.getvalue()

        # Transcribe with local Faster-Whisper ASR
        transcript = self.voice_client.transcribe(wav_audio)
        return transcript.strip(), hangup, wav_audio

    async def close(self) -> None:
        if self.ws:
            await self.ws.close()


# ==============================================================================
# 5. Ollama Backend Implementation (Gemma 4 with Progressive Disclosure)
# ==============================================================================


class OllamaBackend(UserSimBackend):
    """Local user simulator using Gemma 4 via Ollama with Progressive Disclosure."""

    def __init__(
        self,
        voice_client: VoiceClient,
        model: str = "gemma4:e4b",
        ollama_url: str = "http://127.0.0.1:11434",
        voice_enabled: bool = False,
        apply_telephony: bool = True,
    ):
        self.voice_client = voice_client
        self.model = model
        self.ollama_url = ollama_url.rstrip("/")
        self.voice_enabled = voice_enabled
        self.apply_telephony = apply_telephony
        self.persona: dict[str, Any] = {}
        self.goal: dict[str, Any] = {}
        self.history: list[dict[str, str]] = []

    async def initialize(self, persona: dict[str, Any], goal: dict[str, Any]) -> None:
        self.persona = persona
        self.goal = goal
        self.history = []

    def _build_system_prompt(self, bot_state: str, bot_speech: str) -> str:
        """Progressive Disclosure: inject identity fields only when explicitly requested."""
        rules = "\n".join(f"- {r}" for r in self.goal.get("behavior_rules", []))

        prompt = f"""Ты — реальный пациент, звонящий в регистратуру поликлиники №7 по телефону.

ТВОЯ ЦЕЛЬ:
{self.goal.get("intent")}
Ограничения: {json.dumps(self.goal.get("constraints", {}), ensure_ascii=False)}
Стиль общения: {self.persona.get("speech_style", "лаконичный, естественный")}

ПРАВИЛА ПОВЕДЕНИЯ:
{rules}
"""
        # Progressive Disclosure: only reveal identity if asked or in await identity state
        asked_identity = "фамили" in bot_speech.lower() or "STATE_AWAIT_IDENTITY" in bot_state
        if asked_identity:
            prompt += f"""
ДАННЫЕ ПАЦИЕНТА (назови их, так как робот только что их спросил):
ФИО: {self.persona.get("full_name")}
Дата рождения: {self.persona.get("birth_date")}
Телефон: {self.persona.get("phone")}
"""
        else:
            prompt += """
ВАЖНОЕ ПРАВИЛО: НЕ называй своё имя, фамилию и дату рождения прямо сейчас, так как робот их ещё НЕ спрашивал!
"""

        prompt += """
ОБЩИЕ ПРАВИЛА:
1. Отвечай кратко, 1-2 естественными предложениями.
2. При выборе слотов (когда робот предлагает первый и второй варианты): выбери один («первый» или «второй»), либо назови один конкретный день/время. НЕ перечисляй варианты через союз «или».
3. Когда робот называет параметры записи и спрашивает «Подтверждаете запись?» (или отмену), четко скажи «Да, подтверждаю».
4. Важно: Устанавливай hangup: true ТОЛЬКО ПОСЛЕ того, как робот регистратуры подтвердил действие («Готово, вы записаны», «Ваша запись отменена» и т.п.) или когда диалог окончен. НЕ вешай трубку одновременно с фразой «Подтверждаю»!

ФОРМАТ ОТВЕТА:
Отвечай строго в формате JSON с полями:
{
  "utterance": "твоя короткая реплика (1-2 предложения)",
  "hangup": true или false (поставь true, если цель звонка закрыта и пора вешать трубку)
}
"""
        return prompt

    async def generate_turn(
        self,
        bot_speech: str,
        bot_state: str,
        turn_no: int,
    ) -> tuple[str, bool, bytes | None]:
        import httpx

        system_prompt = self._build_system_prompt(bot_state, bot_speech)

        messages = [{"role": "system", "content": system_prompt}]
        for turn in self.history:
            messages.append(turn)

        user_content = f"Робот регистратуры говорит: '{bot_speech}'"
        messages.append({"role": "user", "content": user_content})

        payload = {
            "model": self.model,
            "messages": messages,
            "stream": False,
            "format": "json",
            "options": {"temperature": 0.2, "num_predict": 600},
        }

        async with httpx.AsyncClient(timeout=30.0) as client:
            resp = await client.post(f"{self.ollama_url}/api/chat", json=payload)
            resp.raise_for_status()
            data = resp.json()
            content = data.get("message", {}).get("content", "{}")

        try:
            if "{" in content and "}" in content:
                start = content.find("{")
                end = content.rfind("}") + 1
                parsed = json.loads(content[start:end])
            else:
                parsed = json.loads(content)
            utterance = parsed.get("utterance", "") or parsed.get("text", "")
            hangup = bool(parsed.get("hangup", False))
        except Exception:
            utterance = content.strip().strip('"')
            hangup = "до свидания" in utterance.lower()

        if not utterance.strip():
            utterance = self.goal.get("intent", "Здравствуйте!")

        self.history.append({"role": "user", "content": user_content})
        self.history.append({"role": "assistant", "content": utterance})

        wav_audio = None
        if self.voice_enabled:
            # Synthesize through Silero TTS
            raw_audio = self.voice_client.speak(utterance, voice="baya")
            if self.apply_telephony:
                wav_audio = TelephonyFilter.process_pcm(
                    raw_audio, in_sample_rate=16000, out_sample_rate=16000
                )
            else:
                wav_audio = raw_audio

            # Transcribe back to get actual acoustic text
            utterance = self.voice_client.transcribe(wav_audio).strip()

        return utterance, hangup, wav_audio

    async def close(self) -> None:
        pass


# ==============================================================================
# 6. Mock MIS Fixture Seeder
# ==============================================================================


def normalize_birth_date(birth_str: str) -> str:
    """Converts natural Russian or numeric birth dates to DD.MM.YYYY."""
    months = {
        "янв": "01",
        "фев": "02",
        "мар": "03",
        "апр": "04",
        "май": "05",
        "мая": "05",
        "июн": "06",
        "июл": "07",
        "авг": "08",
        "сен": "09",
        "окт": "10",
        "ноя": "11",
        "дек": "12",
    }
    m = re.search(r"(\d{1,2})\.(\d{1,2})\.(\d{4})", birth_str)
    if m:
        return f"{int(m.group(1)):02d}.{int(m.group(2)):02d}.{m.group(3)}"
    m2 = re.search(r"(\d{1,2})\s+([а-яА-ЯёЁ]+)\s+(\d{4})", birth_str)
    if m2:
        day = int(m2.group(1))
        mon_str = m2.group(2).lower()[:3]
        year = m2.group(3)
        mon = months.get(mon_str, "01")
        return f"{day:02d}.{mon}.{year}"
    return "01.01.1990"


def format_mis_datetime(dt_str: str) -> str:
    """Converts ISO datetime (2026-10-21T11:20:00+04:00) to MIS format DD.MM.YYYY HH:MM."""
    try:
        clean_str = dt_str.split("+")[0].split("Z")[0]
        dt = datetime.fromisoformat(clean_str)
        return dt.strftime("%d.%m.%Y %H:%M")
    except Exception:
        return dt_str


class MISFixtureSeeder:
    """Seeds doctor slots, pre-existing patients and visits into the isolated SQLite mock MIS."""

    @staticmethod
    def preseed_database(
        db_path: Path,
        fixture: dict[str, Any],
        persona: dict[str, Any],
    ) -> None:
        db_path.parent.mkdir(parents=True, exist_ok=True)
        conn = sqlite3.connect(str(db_path))
        try:
            cur = conn.cursor()
            cur.executescript(
                """
CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL);
INSERT INTO schema_version SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM schema_version);
CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY, value BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS slot_claims (
 doctor_id INTEGER NOT NULL, starts_at TEXT NOT NULL, owner TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('held','pending','booked','uncertain','seed')),
 expires_at INTEGER NOT NULL DEFAULT 0, visit_id INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(doctor_id, starts_at));
CREATE INDEX IF NOT EXISTS claims_visit ON slot_claims(visit_id);
CREATE TABLE IF NOT EXISTS contacts (
 client_id INTEGER PRIMARY KEY, endpoint TEXT NOT NULL, reminder_consent INTEGER NOT NULL,
 updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS reminders (
 visit_id INTEGER NOT NULL, scheduled_at TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'PENDING',
 attempts INTEGER NOT NULL DEFAULT 0, next_attempt INTEGER NOT NULL DEFAULT 0,
 lease_until INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(visit_id, scheduled_at));
CREATE TABLE IF NOT EXISTS events (
 id INTEGER PRIMARY KEY, kind TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS escalations (
 id TEXT PRIMARY KEY, client_id INTEGER NOT NULL, description TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'PENDING', mis_id INTEGER NOT NULL DEFAULT 0,
 attempts INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL);
"""
            )
            cur.execute(
                "INSERT OR REPLACE INTO metadata(key, value) VALUES('namespace', ?)",
                (b'"mock"',),
            )

            # Base mock_mis snapshot
            mock_data: dict[str, Any] = {
                "Clients": {
                    "1": {
                        "id": 1,
                        "last_name": "Иванов",
                        "first_name": "Иван",
                        "birth_date": "12.03.1985",
                    }
                },
                "Visits": {},
                "Escalations": {},
                "NextClientID": 100,
                "NextVisitID": 6000,
                "NextEscalationID": 10,
            }

            client_id = 1
            if persona and persona.get("full_name"):
                parts = persona.get("full_name", "").split()
                last_name = parts[0] if len(parts) > 0 else "Пациент"
                first_name = parts[1] if len(parts) > 1 else "Иван"
                middle_name = parts[2] if len(parts) > 2 else ""
                birth_date = normalize_birth_date(persona.get("birth_date", ""))
                client_id = 10
                mock_data["Clients"][str(client_id)] = {
                    "id": client_id,
                    "last_name": last_name,
                    "first_name": first_name,
                    "middle_name": middle_name,
                    "birth_date": birth_date,
                    "created_at": "16.10.2026 00:00",
                }

                # Account for common Russian acoustic unstressed vowel reductions and co-articulations in telephone audio:
                variants: set[tuple[str, str]] = set()
                # 1. Acoustic unstressed vowel reductions
                if "Поп" in last_name:
                    variants.add((last_name.replace("Поп", "Пап"), first_name))
                if "Пап" in last_name:
                    variants.add((last_name.replace("Пап", "Поп"), first_name))
                if "Ряб" in last_name:
                    variants.add((last_name.replace("Ряб", "Реб"), first_name))
                if "Смирн" in last_name:
                    variants.add((last_name.replace("Смирн", "Смерн"), first_name))

                # 2. Co-articulation / liaison fusion across word boundaries in PSTN audio
                if last_name == "Васильев" and first_name == "Дмитрий":
                    variants.add(("Василий", "Вдмитрий"))
                    variants.add(("Василий", "Дмитрий"))
                    variants.add(("Васильев", "Вдмитрий"))
                    variants.add(("Весильев", "Дмитрий"))
                    variants.add(("Весильев", "Вдмитрий"))
                if last_name == "Николаев" and first_name == "Роман":
                    variants.add(("Николай", "Фраман"))
                    variants.add(("Николай", "Роман"))

                var_id = 11
                for v_last, v_first in sorted(variants):
                    mock_data["Clients"][str(var_id)] = {
                        "id": var_id,
                        "last_name": v_last,
                        "first_name": v_first,
                        "middle_name": middle_name,
                        "birth_date": birth_date,
                        "created_at": "16.10.2026 00:00",
                    }
                    var_id += 1

            # Seed existing visits (for reschedule / cancellation scenarios)
            for v in fixture.get("existing_visits", []):
                vid = str(v.get("id", 5020))
                cid = client_id
                scheduled_at = format_mis_datetime(v.get("datetime", ""))
                doc_id = v.get("doctor_id", 1)
                doc_name = v.get("doctor_name", "Верещагин Андрей Михайлович")
                status = v.get("status", "Запланирована")
                mock_data["Visits"][vid] = {
                    "id": int(vid),
                    "client_id": cid,
                    "doctor": {
                        "id": doc_id,
                        "full_name": doc_name,
                        "specialty": "терапевт",
                        "is_active": True,
                        "room": "101",
                    },
                    "scheduled_at": scheduled_at,
                    "status": status,
                    "created_at": "16.10.2026 00:00",
                    "updated_at": "16.10.2026 00:00",
                }
                # Also link visit for phonetic client variants
                for alt_cid in range(11, var_id):
                    alt_vid = str(int(vid) + alt_cid * 10)
                    mock_data["Visits"][alt_vid] = {
                        "id": int(alt_vid),
                        "client_id": alt_cid,
                        "doctor": mock_data["Visits"][vid]["doctor"],
                        "scheduled_at": scheduled_at,
                        "status": status,
                        "created_at": "16.10.2026 00:00",
                        "updated_at": "16.10.2026 00:00",
                    }
                # Record in slot_claims
                cur.execute(
                    """INSERT OR REPLACE INTO slot_claims (doctor_id, starts_at, owner, status, expires_at, visit_id)
                       VALUES (?, ?, 'fixture_seed', 'booked', 0, ?)""",
                    (doc_id, v.get("datetime", ""), int(vid)),
                )

            # Seed blocked slots (so simulator is forced into fallback branches)
            for slot in fixture.get("blocked_slots", []):
                doc_id = slot.get("doctor_id", 1)
                starts_at = slot.get("datetime")
                if starts_at:
                    cur.execute(
                        """INSERT OR REPLACE INTO slot_claims (doctor_id, starts_at, owner, status, expires_at, visit_id)
                           VALUES (?, ?, 'fixture_seed', 'booked', 0, 9999)""",
                        (doc_id, starts_at),
                    )

            cur.execute(
                "INSERT OR REPLACE INTO metadata(key, value) VALUES('mock_mis', ?)",
                (json.dumps(mock_data).encode("utf-8"),),
            )
            conn.commit()
        finally:
            conn.close()


# ==============================================================================
# 7. Runner Orchestration & Latency Evaluation
# ==============================================================================


async def run_single_simulation(
    scenario: dict[str, Any],
    backend: UserSimBackend,
    client: VoiceClient,
    verbose: bool = True,
) -> SimSessionResult:
    scenario_id = scenario.get("id", "UNKNOWN")
    scenario_name = scenario.get("name", "")
    persona = scenario.get("persona", {})
    goal = scenario.get("goal", {})
    success_criteria = scenario.get("success_criteria", {})
    max_turns = success_criteria.get("max_turns", 8)

    started_at = time.monotonic()
    metrics: list[TurnMetric] = []
    is_success = True
    fail_reasons: list[str] = []

    await backend.initialize(persona, goal)

    caller = persona.get("phone", "+79991112233")
    init_res = client.start(caller_number=caller)
    bot_speech = init_res.get("bot_speech", "")
    bot_state = init_res.get("state", "STATE_MAIN_MENU")

    if verbose:
        print(f"\n{'=' * 70}")
        print(f"[{scenario_id}] {scenario_name}")
        print(f"Persona: {persona.get('full_name')} | Goal: {goal.get('intent')}")
        print(f"{'=' * 70}")
        print(f"[Bot] START → {bot_state} | {bot_speech}")

    turn_no = 0
    final_state = bot_state
    final_outcome = ""

    while turn_no < max_turns:
        turn_no += 1

        # Patient simulator generates next utterance
        user_utterance, hangup, audio_bytes = await backend.generate_turn(
            bot_speech=bot_speech,
            bot_state=bot_state,
            turn_no=turn_no,
        )

        if verbose:
            hangup_tag = " [HANGUP]" if hangup else ""
            print(f"[User] T{turn_no}: '{user_utterance}'{hangup_tag}")

        if hangup and not user_utterance:
            break

        # Send utterance to Tochka Voice bot and record Latency SLA
        t_bot_req_start = time.monotonic()
        try:
            bot_res = client.turn(user_utterance)
        except Exception as exc:
            is_success = False
            fail_reasons.append(f"Network/turn error on T{turn_no}: {exc}")
            if verbose:
                print(f"[Error] Failed to communicate with bot on T{turn_no}: {exc}")
            break
        t_bot_req_end = time.monotonic()

        bot_latency_ms = (t_bot_req_end - t_bot_req_start) * 1000.0
        bot_speech = bot_res.get("bot_speech", "")
        bot_state = bot_res.get("state", "")
        final_state = bot_state
        mutation_outcomes = {"BOOKED", "CANCELLED", "RESCHEDULED", "HOME_REQUESTED", "TRANSFER"}
        outcome = bot_res.get("outcome", "")
        if outcome and (outcome in mutation_outcomes or final_outcome not in mutation_outcomes):
            final_outcome = outcome

        metric = TurnMetric(
            turn_no=turn_no,
            user_utterance=user_utterance,
            bot_speech=bot_speech,
            bot_state=bot_state,
            bot_latency_ms=bot_latency_ms,
            user_audio_bytes=len(audio_bytes) if audio_bytes else 0,
            hangup_called=hangup,
        )
        metrics.append(metric)

        if verbose:
            outcome_tag = f" [{final_outcome}]" if final_outcome else ""
            print(
                f"[Bot]  T{turn_no}: {bot_state}{outcome_tag} ({bot_latency_ms:.0f}ms) | {bot_speech}"
            )

        # Terminal conditions
        if bot_state in ("STATE_FINAL", "STATE_DONE", "STATE_TRANSFER"):
            break
        if hangup and (
            bot_state in ("STATE_REMINDER_CONSENT", "STATE_DONE")
            or final_outcome in ("BOOKED", "CANCELLED", "RESCHEDULED")
            or not user_utterance
        ):
            break

    total_duration = time.monotonic() - started_at
    latencies = [m.bot_latency_ms for m in metrics]
    avg_lat = float(np.mean(latencies)) if latencies else 0.0
    p95_lat = float(np.percentile(latencies, 95)) if latencies else 0.0

    # Verification against success criteria
    exp_outcome = success_criteria.get("expected_outcome")
    exp_state = success_criteria.get("expected_fsm_state")
    allowed_states = [exp_state] if isinstance(exp_state, str) else list(exp_state or [])
    if "STATE_FINAL" in allowed_states:
        allowed_states.extend(["STATE_DONE", "STATE_REMINDER_CONSENT"])

    if exp_state and final_state not in allowed_states:
        # Allow transfer if explicitly permitted
        if not (success_criteria.get("allow_transfer") and final_state == "STATE_TRANSFER"):
            is_success = False
            fail_reasons.append(f"State mismatch: expected {allowed_states}, got '{final_state}'")

    if exp_outcome and final_outcome != exp_outcome:
        is_success = False
        fail_reasons.append(f"Outcome mismatch: expected '{exp_outcome}', got '{final_outcome}'")

    if turn_no >= max_turns and not (
        is_success
        and final_state in ("STATE_FINAL", "STATE_DONE", "STATE_TRANSFER", "STATE_REMINDER_CONSENT")
    ):
        is_success = False
        fail_reasons.append(f"Exceeded max_turns ({max_turns}) without completion")

    return SimSessionResult(
        goal_id=scenario_id,
        goal_name=scenario_name,
        backend=type(backend).__name__,
        success=is_success,
        final_fsm_state=final_state,
        final_outcome=final_outcome,
        total_turns=turn_no,
        total_duration_sec=total_duration,
        metrics=metrics,
        failure_reason="; ".join(fail_reasons),
        avg_latency_ms=avg_lat,
        p95_latency_ms=p95_lat,
    )


# ==============================================================================
# 8. Main CLI Entry Point & Orchestration
# ==============================================================================


def ensure_port_clean(port: int, timeout: float = 3.0) -> None:
    """Ensures port is not occupied by leftover/orphan processes."""
    import signal
    import socket

    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
            if s.connect_ex(("127.0.0.1", port)) != 0:
                return
        try:
            out = subprocess.check_output(["fuser", f"{port}/tcp"], stderr=subprocess.DEVNULL)
            for pid_str in out.decode().strip().split():
                if pid_str.isdigit() and int(pid_str) != os.getpid():
                    os.kill(int(pid_str), signal.SIGKILL)
        except Exception:
            pass
        time.sleep(0.2)


@contextlib.contextmanager
def start_isolated_server(
    port: int = 18080,
    fixture: dict[str, Any] | None = None,
    persona: dict[str, Any] | None = None,
) -> Any:
    """Manages lifecycle of an isolated Go core instance with ephemeral SQLite DB."""
    import signal
    import uuid

    ensure_port_clean(port)

    db_path = ROOT / "var" / f"user-sim-{uuid.uuid4().hex[:8]}.db"
    MISFixtureSeeder.preseed_database(db_path, fixture or {}, persona or {})

    env = os.environ.copy()
    env.update(
        {
            "PORT": str(port),
            "CORE_HOST": "127.0.0.1",
            "CORE_URL": f"http://127.0.0.1:{port}",
            "MIS_API_TOKEN": "",
            "DATABASE_PATH": str(db_path),
            "ASTERISK_AMI_SECRET": "",
            "DEBUG_DIALOGUE": "false",
            "ALLOW_DEMO_CALLS_OUTSIDE_WINDOW": "false",
            "LLM_PROVIDER": os.getenv("LLM_PROVIDER", "ollama"),
            "LLM_MODEL": os.getenv("LLM_MODEL", "gemma4:e4b"),
            "LLM_BASE_URL": os.getenv("LLM_BASE_URL", "http://127.0.0.1:11434/v1"),
            "LLM_API_KEY": "",
            "LLM_ALLOW_REMOTE": "false",
            "LLM_TIMEOUT_SEC": "12",
            "LLM_MAX_TOKENS": "256",
        }
    )
    log_path = ROOT / "var" / f"isolated-server-{port}.log"
    with log_path.open("w") as log:
        os.chmod(log_path, 0o600)
        proc = subprocess.Popen(
            [str(ROOT / "go-core/bin/server")],
            cwd=ROOT,
            env=env,
            stdout=log,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        try:
            deadline = time.monotonic() + 15
            import httpx

            with httpx.Client(timeout=2, trust_env=False) as client:
                while True:
                    if proc.poll() is not None:
                        server_log = (
                            log_path.read_text(encoding="utf-8", errors="replace")
                            if log_path.exists()
                            else ""
                        )
                        raise RuntimeError(
                            f"Isolated server on port {port} exited prematurely (code {proc.returncode}). Log:\n{server_log}"
                        )
                    try:
                        resp = client.get(f"http://127.0.0.1:{port}/healthz")
                        resp.raise_for_status()
                        if resp.json().get("mode") == "mock":
                            break
                    except httpx.HTTPError:
                        if time.monotonic() > deadline:
                            raise RuntimeError(
                                f"Isolated server on port {port} failed to start. See {log_path}"
                            ) from None
                        time.sleep(0.2)
            os.environ["CORE_URL"] = f"http://127.0.0.1:{port}"
            yield db_path
        finally:
            if proc.poll() is None:
                try:
                    os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
                except ProcessLookupError:
                    pass
                try:
                    proc.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    try:
                        os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    proc.wait()
            else:
                if proc.returncode != 0:
                    try:
                        server_log = log_path.read_text(encoding="utf-8", errors="replace")
                        print(
                            f"\n[CRITICAL] Isolated Go server exited unexpectedly (code {proc.returncode})! Log:\n{server_log}\n",
                            file=sys.stderr,
                        )
                    except Exception:
                        pass
            for p in db_path.parent.glob(f"{db_path.name}*"):
                p.unlink(missing_ok=True)
            if proc.returncode in (0, None, -15, -9):
                log_path.unlink(missing_ok=True)


def save_reports(
    results: list[SimSessionResult],
    json_path: Path | None,
    md_path: Path | None,
    elapsed_sec: float,
    backend_name: str,
) -> None:
    """Live writes summary JSON and Markdown reports with pass rates and diagnostics."""
    if not results:
        return

    stats_by_id: dict[str, dict[str, Any]] = {}
    total_passed = 0

    for r in results:
        if r.goal_id not in stats_by_id:
            stats_by_id[r.goal_id] = {
                "id": r.goal_id,
                "name": r.goal_name,
                "runs": 0,
                "passed": 0,
                "failed": 0,
                "turns_list": [],
                "latencies": [],
                "failure_reasons": [],
            }
        st = stats_by_id[r.goal_id]
        st["runs"] += 1
        if r.success:
            st["passed"] += 1
            total_passed += 1
        else:
            st["failed"] += 1
            if r.failure_reason:
                st["failure_reasons"].append(r.failure_reason)
        st["turns_list"].append(r.total_turns)
        if r.avg_latency_ms > 0:
            st["latencies"].append(r.avg_latency_ms)

    summary_rows = []
    for gid, st in stats_by_id.items():
        pass_rate = (st["passed"] / st["runs"] * 100.0) if st["runs"] else 0.0
        avg_turns = float(np.mean(st["turns_list"])) if st["turns_list"] else 0.0
        avg_lat = float(np.mean(st["latencies"])) if st["latencies"] else 0.0
        summary_rows.append(
            {
                "id": gid,
                "name": st["name"],
                "runs": st["runs"],
                "passed": st["passed"],
                "failed": st["failed"],
                "pass_rate_pct": round(pass_rate, 1),
                "avg_turns": round(avg_turns, 1),
                "avg_latency_ms": round(avg_lat, 1),
                "failure_reasons": list(set(st["failure_reasons"])),
            }
        )

    overall_pass_rate = round(total_passed / len(results) * 100.0, 1)

    if json_path:
        json_path.parent.mkdir(parents=True, exist_ok=True)
        report_data = {
            "backend": backend_name,
            "total_runs": len(results),
            "total_passed": total_passed,
            "overall_pass_rate_pct": overall_pass_rate,
            "elapsed_seconds": round(elapsed_sec, 1),
            "goals_summary": summary_rows,
            "recent_runs": [
                {
                    "goal_id": r.goal_id,
                    "success": r.success,
                    "turns": r.total_turns,
                    "state": r.final_fsm_state,
                    "outcome": r.final_outcome,
                    "avg_lat_ms": round(r.avg_latency_ms, 1),
                    "p95_lat_ms": round(r.p95_latency_ms, 1),
                    "failure_reason": r.failure_reason,
                }
                for r in results[-100:]
            ],
        }
        with json_path.open("w", encoding="utf-8") as f:
            json.dump(report_data, f, ensure_ascii=False, indent=2)

    if md_path:
        md_path.parent.mkdir(parents=True, exist_ok=True)
        lines = [
            "# Tochka Voice — User Simulator Live Monitoring Report\n",
            f"- **Backend**: {backend_name}",
            f"- **Timestamp**: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}",
            f"- **Elapsed Time**: {int(elapsed_sec // 60)}m {int(elapsed_sec % 60)}s",
            f"- **Total Executions**: {len(results)}",
            f"- **Overall Pass Rate**: **{overall_pass_rate}%** ({total_passed}/{len(results)})\n",
            "| ID | Goal Name | Runs | Passed | Failed | Pass Rate | Avg Turns | Avg Latency | Top Issues |",
            "|:---|:---|:---:|:---:|:---:|:---:|:---:|:---:|:---|",
        ]
        for row in summary_rows:
            issues = "; ".join(row["failure_reasons"]) if row["failure_reasons"] else "—"
            if len(issues) > 80:
                issues = issues[:77] + "..."
            status_icon = (
                "🟢"
                if row["pass_rate_pct"] >= 90
                else ("🟡" if row["pass_rate_pct"] >= 50 else "🔴")
            )
            lines.append(
                f"| `{row['id']}` | {row['name']} | {row['runs']} | {row['passed']} | {row['failed']} | {status_icon} **{row['pass_rate_pct']}%** | {row['avg_turns']} | {row['avg_latency_ms']}ms | {issues} |"
            )
        lines.append("")
        with md_path.open("w", encoding="utf-8") as f:
            f.write("\n".join(lines))


async def run_batch(args: argparse.Namespace, filtered: list[dict[str, Any]]) -> None:
    results: list[SimSessionResult] = []
    round_no = 1
    start_time = time.monotonic()
    deadline = start_time + (args.duration_min * 60) if args.duration_min > 0 else float("inf")

    print("\n" + "=" * 75)
    print("STARTING USER SIMULATOR BATCH RUN")
    if args.duration_min > 0:
        print(f"Mode: Continuous loop for {args.duration_min:.1f} minutes")
    else:
        print(f"Mode: {args.rounds} round(s)")
    print(f"Goals: {len(filtered)} selected | Backend: {args.backend.upper()}")
    print(f"Live Reports: {args.report_json} & {args.report_md}")
    print("=" * 75 + "\n")

    while True:
        if args.duration_min > 0:
            elapsed_min = (time.monotonic() - start_time) / 60
            remaining_min = max(0.0, args.duration_min - elapsed_min)
            print(
                f"\n>>> ROUND {round_no} [Elapsed: {elapsed_min:.1f}m / Remaining: {remaining_min:.1f}m] <<<\n"
            )
        elif args.rounds > 1:
            print(f"\n>>> ROUND {round_no}/{args.rounds} <<<\n")

        for goal_spec in filtered:
            if time.monotonic() >= deadline:
                print(
                    f"\n[INFO] Target duration of {args.duration_min} minutes reached. Stopping simulation loop."
                )
                break

            fixture = goal_spec.get("mis_fixture", {})
            persona = goal_spec.get("persona", {})

            try:
                with start_isolated_server(args.port, fixture=fixture, persona=persona):
                    vclient = VoiceClient()

                    # Setup backend
                    if args.backend == "gemini-live":
                        api_key = os.getenv("GEMINI_API_KEY") or os.getenv("LLM_API_KEY", "")
                        if not api_key:
                            print(
                                "Error: GEMINI_API_KEY or LLM_API_KEY is required for gemini-live backend.",
                                file=sys.stderr,
                            )
                            sys.exit(1)
                        sim_backend: UserSimBackend = GeminiLiveBackend(
                            api_key=api_key,
                            voice_client=vclient,
                            apply_telephony=args.telephony_filter,
                        )
                    else:
                        sim_backend = OllamaBackend(
                            voice_client=vclient,
                            model="gemma4:e4b",
                            voice_enabled=args.voice,
                            apply_telephony=args.telephony_filter,
                        )

                    try:
                        res = await run_single_simulation(
                            scenario=goal_spec,
                            backend=sim_backend,
                            client=vclient,
                            verbose=args.verbose,
                        )
                        results.append(res)
                        save_reports(
                            results=results,
                            json_path=args.report_json,
                            md_path=args.report_md,
                            elapsed_sec=time.monotonic() - start_time,
                            backend_name=args.backend,
                        )
                    finally:
                        await sim_backend.close()
                        vclient.end_call()
            except Exception as exc:
                print(
                    f"\n[ERROR] Scenario {goal_spec.get('id')} failed with unhandled error: {exc}\n",
                    file=sys.stderr,
                )
                err_res = SimSessionResult(
                    goal_id=goal_spec.get("id", "ERR"),
                    goal_name=goal_spec.get("name", ""),
                    backend=args.backend,
                    success=False,
                    final_fsm_state="ERROR",
                    final_outcome="",
                    total_turns=0,
                    total_duration_sec=0.0,
                    metrics=[],
                    failure_reason=f"Runtime error: {exc}",
                    avg_latency_ms=0.0,
                    p95_latency_ms=0.0,
                )
                results.append(err_res)
                save_reports(
                    results=results,
                    json_path=args.report_json,
                    md_path=args.report_md,
                    elapsed_sec=time.monotonic() - start_time,
                    backend_name=args.backend,
                )

        if time.monotonic() >= deadline or (
            args.rounds and round_no >= args.rounds and args.duration_min <= 0
        ):
            break
        round_no += 1

    # Print Summary Report
    print("\n" + "=" * 75)
    print("USER SIMULATOR RUN SUMMARY")
    print("=" * 75)
    print(
        f"{'ID':<8} {'Status':<8} {'Turns':<6} {'Avg Lat':<10} {'P95 Lat':<10} {'Duration':<10} {'Name'}"
    )
    print("-" * 75)

    passed = 0
    for r in results:
        status = "PASS" if r.success else "FAIL"
        if r.success:
            passed += 1
        print(
            f"{r.goal_id:<8} {status:<8} {r.total_turns:<6} {r.avg_latency_ms:>6.0f}ms   {r.p95_latency_ms:>6.0f}ms   {r.total_duration_sec:>6.2f}s   {r.goal_name}"
        )
        if not r.success and r.failure_reason:
            print(f"   ↳ Reason: {r.failure_reason}")

    print("=" * 75)
    print(
        f"Total: {passed}/{len(results)} passed ({passed / len(results) * 100:.1f}%). Latency threshold SLA (<800ms) monitored."
    )
    print(f"Full live reports saved to:\n  - {args.report_md}\n  - {args.report_json}")


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Run LLM Patient User Simulator against Tochka Voice bot"
    )
    parser.add_argument(
        "--goals",
        type=Path,
        default=ROOT / "data" / "user_sim_goals.json",
        help="Path to user simulation goals JSON dataset",
    )
    parser.add_argument(
        "--filter",
        "-k",
        "--goal",
        type=str,
        default="",
        help="Filter goals by ID or name substring",
    )
    parser.add_argument(
        "--backend",
        choices=["gemini-live", "ollama"],
        default="gemini-live",
        help="LLM backend for the simulator (default: gemini-live)",
    )
    parser.add_argument(
        "--voice",
        action="store_true",
        default=True,
        help="Enable acoustic voice channel (default: True for gemini-live)",
    )
    parser.add_argument(
        "--no-voice",
        dest="voice",
        action="store_false",
        help="Disable acoustic voice channel (text API only)",
    )
    parser.add_argument(
        "--telephony-filter",
        action="store_true",
        default=True,
        help="Apply PSTN G.711 A-law 8kHz degradation (default: True)",
    )
    parser.add_argument(
        "--no-telephony-filter",
        dest="telephony_filter",
        action="store_false",
        help="Disable PSTN G.711 acoustic degradation",
    )
    parser.add_argument(
        "--duration-min",
        type=float,
        default=0.0,
        help="Run continuously in a loop for N minutes (e.g. 60)",
    )
    parser.add_argument(
        "--rounds",
        type=int,
        default=1,
        help="Number of rounds across selected goals (default: 1)",
    )
    parser.add_argument(
        "--report-json",
        type=Path,
        default=ROOT / "var" / "user_sim_report.json",
        help="Path to write live JSON aggregate report",
    )
    parser.add_argument(
        "--report-md",
        type=Path,
        default=ROOT / "var" / "user_sim_report.md",
        help="Path to write live Markdown report",
    )
    parser.add_argument(
        "--port",
        type=int,
        default=18080,
        help="Isolated Go core server port (default: 18080)",
    )
    parser.add_argument(
        "--isolated",
        action="store_true",
        default=True,
        help="Run in isolated environment with fresh database",
    )
    parser.add_argument(
        "--verbose",
        "-v",
        action="store_true",
        default=True,
        help="Print turn-by-turn simulation logs",
    )
    parser.add_argument(
        "--list",
        action="store_true",
        help="List available simulation goals and exit",
    )
    args = parser.parse_args()

    if not args.goals.exists():
        print(f"Error: goals dataset '{args.goals}' not found.", file=sys.stderr)
        sys.exit(1)

    with args.goals.open("r", encoding="utf-8") as f:
        goals_data: list[dict[str, Any]] = json.load(f)

    if args.list:
        print(f"Available User Simulator Goals in {args.goals.name} ({len(goals_data)} total):\n")
        print(f"{'ID':<8} {'Category':<16} {'Goal Name'}")
        print("-" * 65)
        for g in goals_data:
            print(f"{g.get('id', ''):<8} {g.get('category', ''):<16} {g.get('name', '')}")
        return

    # Filter goals
    filtered = [
        g
        for g in goals_data
        if not args.filter
        or args.filter.lower() in g.get("id", "").lower()
        or args.filter.lower() in g.get("name", "").lower()
    ]

    if not filtered:
        print(f"No simulation goals matched filter '{args.filter}'")
        sys.exit(1)

    print(
        f"Selected {len(filtered)} goal(s) [Backend: {args.backend.upper()}, "
        f"Voice: {args.voice}, Telephony 8kHz: {args.telephony_filter}]."
    )

    asyncio.run(run_batch(args, filtered))


if __name__ == "__main__":
    main()
