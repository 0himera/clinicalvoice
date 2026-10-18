#!/usr/bin/env python3
"""Install systemd units for an already prepared checkout; do not start them."""

import argparse
import os
import pwd
import subprocess
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


def quoted(path: Path) -> str:
    return '"' + str(path).replace("\\", "\\\\").replace('"', '\\"').replace("%", "%%") + '"'


def unit(user: str, command: str, description: str) -> str:
    return f"""[Unit]
Description={description}
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=300
StartLimitBurst=5

[Service]
Type=simple
User={user}
WorkingDirectory={str(ROOT).replace("%", "%%")}
EnvironmentFile={str(ROOT / ".env").replace("%", "%%")}
Environment=PYTHONUNBUFFERED=1
ExecStart={command}
Restart=on-failure
RestartSec=5
TimeoutStopSec=30
UMask=0077
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
"""


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--user", required=True, help="Non-root owner of the checkout and services")
    args = parser.parse_args()
    account = pwd.getpwnam(args.user)
    if os.geteuid() != 0 or account.pw_uid == 0:
        raise SystemExit("Run with sudo and --user naming the non-root project owner")
    if ROOT.stat().st_uid != account.pw_uid:
        raise SystemExit("The service user must own the checkout")
    required = (
        ROOT / ".env",
        ROOT / "go-core/bin/server",
        ROOT / "ai-worker/.venv/bin/python",
    )
    if any(not path.exists() for path in required):
        raise SystemExit("Run make setup install build as the project owner first")
    units = {
        "tochka-core.service": unit(
            args.user,
            quoted(ROOT / "go-core/bin/server"),
            "Tochka Voice dialogue controller",
        ),
        "tochka-ai-worker.service": unit(
            args.user,
            f"{quoted(ROOT / 'ai-worker/.venv/bin/python')} {quoted(ROOT / 'ai-worker/main.py')}",
            "Tochka Voice local speech and SIP bridge",
        ),
    }
    with tempfile.TemporaryDirectory(prefix="tochka-units-") as directory:
        paths = []
        for name, content in units.items():
            path = Path(directory) / name
            path.write_text(content)
            paths.append(str(path))
        subprocess.run(["systemd-analyze", "verify", *paths], check=True)
    for name, content in units.items():
        path = Path("/etc/systemd/system") / name
        if path.exists() and not path.with_suffix(".service.before-tochka").exists():
            path.with_suffix(".service.before-tochka").write_bytes(path.read_bytes())
        path.write_text(content)
        path.chmod(0o644)
    subprocess.run(["systemctl", "daemon-reload"], check=True)
    print("Units installed. Start with: sudo systemctl enable --now tochka-core tochka-ai-worker")


if __name__ == "__main__":
    main()
