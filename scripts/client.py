"""Small authenticated client shared by the local diagnostic scripts."""

import os
import time
import uuid
from pathlib import Path

import httpx
from dotenv import load_dotenv

ROOT = Path(__file__).resolve().parent.parent


class VoiceClient:
    def __init__(self):
        load_dotenv(ROOT / ".env", override=False)
        self.core = httpx.Client(
            base_url=os.getenv("CORE_URL", "http://127.0.0.1:8080"),
            headers={"Authorization": "Bearer " + os.environ["CORE_API_TOKEN"]}
            if os.getenv("CORE_API_TOKEN")
            else {},
            timeout=40,
            trust_env=False,
        )
        self.ai = httpx.Client(
            base_url=os.getenv("AI_WORKER_URL", "http://127.0.0.1:8000"),
            headers={"Authorization": "Bearer " + os.environ["AI_API_TOKEN"]}
            if os.getenv("AI_API_TOKEN")
            else {},
            timeout=40,
            trust_env=False,
        )
        self.call_id = ""

    def wait_ready(self, timeout: float = 45, check_ai: bool = True):
        deadline = time.monotonic() + timeout
        while True:
            try:
                self.core.get("/healthz").raise_for_status()
                if check_ai:
                    self.ai.get("/health").raise_for_status()
                return
            except httpx.HTTPError:
                if time.monotonic() >= deadline:
                    target = "core or speech service" if check_ai else "core service"
                    raise RuntimeError(f"{target} did not become ready") from None
                time.sleep(0.25)

    @staticmethod
    def checked(response: httpx.Response) -> httpx.Response:
        response.raise_for_status()
        return response

    def start(self, caller_number: str = "", outbound_visit_id: int = 0) -> dict:
        payload = {}
        if caller_number:
            payload["caller_number"] = caller_number
        if outbound_visit_id:
            payload["outbound_visit_id"] = outbound_visit_id
        result = self.checked(self.core.post("/api/dialogue/start", json=payload)).json()
        self.call_id = result["call_id"]
        return result

    def turn(self, text: str) -> dict:
        return self.checked(
            self.core.post(
                "/api/dialogue/turn",
                json={
                    "call_id": self.call_id,
                    "turn_id": str(uuid.uuid4()),
                    "text": text,
                },
            )
        ).json()

    def transcribe(self, audio: bytes) -> str:
        return self.checked(
            self.ai.post("/asr", files={"file": ("input.wav", audio, "audio/wav")})
        ).json()["text"]

    def speak(self, text: str, speaker: str = "kseniya") -> bytes:
        return self.checked(
            self.ai.post(
                "/tts",
                json={
                    "text": text,
                    "speaker": speaker,
                    "sample_rate": 8000,
                },
            )
        ).content

    def visits(self) -> list:
        return self.checked(self.core.get("/api/visits")).json() or []

    def end_call(self):
        if self.call_id:
            try:
                self.core.post(f"/api/dialogue/{self.call_id}/end")
            except Exception:
                pass
            self.call_id = ""

    def close(self):
        try:
            if self.call_id:
                self.core.post(f"/api/dialogue/{self.call_id}/end")
        except Exception:
            pass
        finally:
            self.core.close()
            self.ai.close()
