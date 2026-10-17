"""Validated settings shared by HTTP and telephone transports."""

import os
from dataclasses import dataclass


@dataclass(frozen=True)
class Settings:
    host: str = "127.0.0.1"
    port: int = 8000
    device: str = "auto"
    api_token: str = ""
    core_url: str = "http://127.0.0.1:8080"
    core_token: str = ""
    telephony: bool = True
    agi_port: int = 4573
    audio_port: int = 9092
    max_calls: int = 8
    debug_dialogue: bool = False
    debug_log_dir: str = "var/debug"
    technical_logs: bool = False
    technical_log_dir: str = "var/log"

    @classmethod
    def from_env(cls) -> "Settings":
        settings = cls(
            host=os.getenv("AI_WORKER_HOST", "127.0.0.1"),
            port=int(os.getenv("AI_WORKER_PORT", "8000")),
            device=os.getenv("DEVICE", "auto"),
            api_token=os.getenv("AI_API_TOKEN", ""),
            core_url=os.getenv("CORE_URL", "http://127.0.0.1:8080").rstrip("/"),
            core_token=os.getenv("CORE_API_TOKEN", ""),
            telephony=os.getenv("TELEPHONY_ENABLED", "true").lower() == "true",
            agi_port=int(os.getenv("FASTAGI_PORT", "4573")),
            audio_port=int(os.getenv("AUDIOSOCKET_PORT", "9092")),
            max_calls=int(os.getenv("MAX_VOICE_CALLS", "8")),
            debug_dialogue=os.getenv("DEBUG_DIALOGUE", "false").lower() == "true",
            debug_log_dir=os.getenv("DEBUG_LOG_DIR", "var/debug"),
            technical_logs=True,
            technical_log_dir=os.getenv("TECHNICAL_LOG_DIR", "var/log"),
        )
        if settings.debug_dialogue and os.getenv("MIS_API_TOKEN"):
            raise ValueError("dialogue transcript debugging is available only with the local mock")
        if settings.device not in {"auto", "cpu", "cuda"}:
            raise ValueError("DEVICE must be auto, cpu or cuda")
        if any(not 0 < p < 65536 for p in (settings.port, settings.agi_port, settings.audio_port)):
            raise ValueError("ports must be between 1 and 65535")
        if not 1 <= settings.max_calls <= 64:
            raise ValueError("MAX_VOICE_CALLS must be between 1 and 64")
        if settings.host not in {"127.0.0.1", "::1", "localhost"} and len(settings.api_token) < 24:
            raise ValueError("AI_API_TOKEN of at least 24 characters is required outside loopback")
        return settings
