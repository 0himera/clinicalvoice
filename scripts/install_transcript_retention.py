#!/usr/bin/env python3
"""Install a local user timer so encrypted log expiry also runs with the core stopped."""

import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


def unit_path(path: Path) -> str:
    value = str(path)
    if any(char in value for char in ("\n", "\r", "\0")):
        raise ValueError("invalid service path")
    if " " not in value and '"' not in value and "%" not in value and "\\" not in value:
        return value
    return '"' + value.replace("\\", "\\\\").replace('"', '\\"').replace("%", "%%") + '"'


def main():
    directory = Path.home() / ".config/systemd/user"
    directory.mkdir(parents=True, exist_ok=True)
    service = f"""[Unit]
Description=Expire encrypted Tochka Voice transcripts

[Service]
Type=oneshot
WorkingDirectory={unit_path(ROOT)}
ExecStart={unit_path(ROOT / "go-core/bin/transcripts")} -prune
UMask=0077
NoNewPrivileges=true
"""
    timer = """[Unit]
Description=Run Tochka Voice transcript retention every minute

[Timer]
OnCalendar=*-*-* *:*:00
Persistent=true
AccuracySec=1s
Unit=tochka-transcript-prune.service

[Install]
WantedBy=timers.target
"""
    for name, content in (
        ("tochka-transcript-prune.service", service),
        ("tochka-transcript-prune.timer", timer),
    ):
        path = directory / name
        if path.is_symlink() or path.exists() and path.read_text() != content:
            raise RuntimeError("existing retention unit differs; refusing to replace it")
        path.write_text(content)
        path.chmod(0o600)
    for arguments in (
        ["daemon-reload"],
        ["enable", "--now", "tochka-transcript-prune.timer"],
        ["start", "tochka-transcript-prune.service"],
    ):
        subprocess.run(["systemctl", "--user", *arguments], check=True)
    print("Local encrypted-transcript retention timer enabled.")


if __name__ == "__main__":
    main()
