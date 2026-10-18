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
    text = environment.read_text()
    settings = {}
    for line in text.splitlines():
        if line.strip() and not line.lstrip().startswith("#") and "=" in line:
            key, value = line.split("=", 1)
            settings[key.strip()] = value.strip().strip("\"'")
    # Repair incomplete templates without rotating already configured secrets.
    for key in ("CORE_API_TOKEN", "AI_API_TOKEN", "ASTERISK_AMI_SECRET"):
        if not settings.get(key):
            settings[key] = secrets.token_hex(24)
            pattern = rf"^{key}=.*$"
            if re.search(pattern, text, flags=re.MULTILINE):
                text = re.sub(pattern, f"{key}={settings[key]}", text, flags=re.MULTILINE)
            else:
                text += f"\n{key}={settings[key]}\n"
    os.chmod(environment, 0o600)
    environment.write_text(text)
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
    # Repeated setup/docker-up preserves registered softphone credentials.
    previous = {}
    auth_file = directory / "pjsip_auth.conf"
    if auth_file.exists():
        section = ""
        for line in auth_file.read_text().splitlines():
            match = re.match(r"\[([^]]+)\]", line)
            if match:
                section = match[1]
            elif line.startswith("password="):
                previous[section] = line.partition("=")[2].strip()
    sip_password = (
        os.getenv("SIP_TEST_PASSWORD", settings.get("SIP_TEST_PASSWORD"))
        or previous.get("test200")
        or secrets.token_hex(16)
    )
    if not re.fullmatch(r"[A-Za-z0-9_-]{16,128}", sip_password):
        raise SystemExit("SIP passwords must contain 16–128 letters, digits, _ or -")
    credentials = {"test200": sip_password}
    for op in range(101, 106):
        password = (
            os.getenv(f"SIP_OPERATOR_{op}_PASSWORD", settings.get(f"SIP_OPERATOR_{op}_PASSWORD"))
            or previous.get(f"{op}-auth")
            or secrets.token_hex(16)
        )
        if not re.fullmatch(r"[A-Za-z0-9_-]{16,128}", password):
            raise SystemExit("SIP passwords must contain 16–128 letters, digits, _ or -")
        credentials[f"{op}-auth"] = password
    pjsip_target = directory / "pjsip_auth.conf"
    fd_sip = os.open(pjsip_target, os.O_CREAT | os.O_TRUNC | os.O_WRONLY, 0o600)
    os.fchmod(fd_sip, 0o600)
    with os.fdopen(fd_sip, "w") as stream:
        for section, password in credentials.items():
            stream.write(f"[{section}](+)\npassword={password}\n\n")
    print("Local configuration ready; secrets were not printed.")


if __name__ == "__main__":
    main()
