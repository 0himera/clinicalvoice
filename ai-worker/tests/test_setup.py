"""Reproduce clean setup without touching the developer's .env or Asterisk."""

import importlib.util
from pathlib import Path


def test_setup_repairs_empty_api_tokens_and_preserves_sip_credentials(tmp_path, monkeypatch):
    root = Path(__file__).resolve().parents[2]
    spec = importlib.util.spec_from_file_location("voice_setup", root / "scripts/setup.py")
    setup = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(setup)
    monkeypatch.setattr(setup, "ROOT", tmp_path)
    for key in (
        "ASTERISK_AMI_USER",
        "ASTERISK_AMI_SECRET",
        "SIP_TEST_PASSWORD",
        *[f"SIP_OPERATOR_{n}_PASSWORD" for n in range(101, 106)],
    ):
        monkeypatch.delenv(key, raising=False)
    (tmp_path / ".env").write_text("CORE_API_TOKEN=\nAI_API_TOKEN=\nASTERISK_AMI_SECRET=\n")
    setup.main()
    credentials = (tmp_path / "var/asterisk/pjsip_auth.conf").read_text()
    environment = (tmp_path / ".env").read_text()
    setup.main()
    assert (tmp_path / ".env").read_text() == environment
    assert (tmp_path / "var/asterisk/pjsip_auth.conf").read_text() == credentials
    passwords = [
        line.partition("=")[2] for line in credentials.splitlines() if line.startswith("password=")
    ]
    assert len(passwords) == len(set(passwords)) == 6
    assert all(len(password) >= 32 for password in passwords)
    assert all(len(line.partition("=")[2]) >= 48 for line in environment.splitlines())
    for filename in (".env", "var/asterisk/manager.conf", "var/asterisk/pjsip_auth.conf"):
        assert (tmp_path / filename).stat().st_mode & 0o777 == 0o600
