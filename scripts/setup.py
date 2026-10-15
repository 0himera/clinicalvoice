#!/usr/bin/env python3
"""Create local secrets and render the loopback-only Asterisk AMI configuration."""

import os
import re
import secrets
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


def main():
    environment = ROOT / ".env"
    if not environment.exists():
        text = (ROOT / ".env.example").read_text()
        for key in ("CORE_API_TOKEN", "AI_API_TOKEN", "ASTERISK_AMI_SECRET"):
            text = re.sub(rf"^{key}=$", f"{key}={secrets.token_hex(24)}", text, flags=re.MULTILINE)
        fd = os.open(environment, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        with os.fdopen(fd, "w") as stream:
            stream.write(text)
    settings = {}
    for line in environment.read_text().splitlines():
        if line.strip() and not line.lstrip().startswith("#") and "=" in line:
            key, value = line.split("=", 1)
            settings[key.strip()] = value.strip().strip("\"'")
    username = os.getenv("ASTERISK_AMI_USER", settings.get("ASTERISK_AMI_USER", "tochka"))
    secret = os.getenv("ASTERISK_AMI_SECRET", settings.get("ASTERISK_AMI_SECRET", ""))
    if not re.fullmatch(r"[A-Za-z0-9_-]{1,64}", username) or not re.fullmatch(
        r"[A-Za-z0-9_-]{24,128}", secret
    ):
        raise SystemExit(
            "Set ASTERISK_AMI_SECRET to 24–128 letters/digits in .env, then run setup again."
        )
    directory = ROOT / "var" / "asterisk"
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    target = directory / "manager.conf"
    fd = os.open(target, os.O_CREAT | os.O_TRUNC | os.O_WRONLY, 0o600)
    with os.fdopen(fd, "w") as stream:
        stream.write(
            "[general]\nenabled=yes\nwebenabled=no\nbindaddr=127.0.0.1\nport=5038\n\n"
            f"[{username}]\nsecret={secret}\ndeny=0.0.0.0/0.0.0.0\npermit=127.0.0.1/255.255.255.255\n"
            "read=call\nwrite=originate,call\n"
        )
    print("Local configuration ready; secrets were not printed.")


if __name__ == "__main__":
    main()
