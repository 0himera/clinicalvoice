import json

import pytest

from src.diagnostics import CallDiagnostics, DialogueTrace
from src.settings import Settings


def test_trace_is_disabled_by_default_and_mock_only(monkeypatch, tmp_path):
    monkeypatch.delenv("DEBUG_DIALOGUE", raising=False)
    monkeypatch.delenv("MIS_API_TOKEN", raising=False)
    assert not Settings.from_env().debug_dialogue
    trace = DialogueTrace(False, str(tmp_path / "disabled"))
    trace("asr_result", text="not retained")
    trace.close()
    assert not (tmp_path / "disabled").exists()
    monkeypatch.setenv("DEBUG_DIALOGUE", "true")
    monkeypatch.setenv("MIS_API_TOKEN", "stand-token")
    with pytest.raises(ValueError, match="local mock"):
        Settings.from_env()


def test_private_jsonl_trace_escapes_newlines_and_rotates(tmp_path):
    directory = tmp_path / "debug"
    trace = DialogueTrace(True, str(directory))
    trace.handler.maxBytes = 200
    for index in range(4):
        trace("asr_result", call_id="test", text=f"Текст\nреплики {index}")
    trace.close()
    assert directory.stat().st_mode & 0o777 == 0o700
    for path in (directory / "speech.jsonl", directory / "speech.jsonl.1"):
        assert path.stat().st_mode & 0o777 == 0o600
        entries = [json.loads(line) for line in path.read_text().splitlines()]
        assert len(entries) == 1
        assert entries[0]["event"] == "asr_result"
        assert "\n" in entries[0]["text"]


def test_technical_trace_drops_text_headers_paths_and_invalid_codes(tmp_path):
    import uuid

    settings = Settings(technical_logs=True, technical_log_dir=str(tmp_path))
    trace = CallDiagnostics(settings)
    call_id = str(uuid.uuid4())
    trace(
        "asr_result",
        call_id=call_id,
        text="Иванов 12.03.1985",
        bot_speech="private reply",
        headers={"Authorization": "private-token"},
        path="/patient/Ivanov",
        state="Ivanov",
        latency_ms=22,
        rms_dbfs=-31.4,
        peak_percent=24.1,
        clipped_percent=0.0,
        text_chars=18,
    )
    trace.close()
    contents = (tmp_path / "speech-events.jsonl").read_text()
    for secret in ("Иванов", "1985", "private", "Ivanov", "Authorization"):
        assert secret not in contents
    event = json.loads(contents)
    assert event["call_id"] == call_id and event["rms_dbfs"] == -31.4
    assert event["latency_ms"] == 22 and event["text_chars"] == 18
    assert not (tmp_path / "speech.jsonl").exists()
