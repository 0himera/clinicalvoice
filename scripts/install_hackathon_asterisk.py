#!/usr/bin/env python3
"""Connect the organizer SIP gateway to the project's FastAGI/AudioSocket routes."""

import grp
import os
import re
import shutil
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


def gateway_configuration(template: str) -> tuple[str, str]:
    number = re.search(r"^\s*contact_user\s*=\s*(\d{3})\s*$", template, re.MULTILINE)
    if not number or "[gateway]" not in template:
        raise ValueError("organizer SIP template is missing gateway/contact_user")
    # The project defines the transport once; keep all repeated gateway sections.
    sections = re.split(r"(?=^\[)", template, flags=re.MULTILINE)
    gateway = "".join(section for section in sections if section.startswith("[gateway]"))
    return number[1], gateway


def dialplan(number: str) -> str:
    if not re.fullmatch(r"\d{3}", number):
        raise ValueError("invalid team number")
    return (
        (ROOT / "asterisk/extensions.conf").read_text()
        + f"""

; The gateway identifies this team's softphone, not a public patient number.
[from-gateway]
exten => {number},1,Set(CALLERID(num)=phone-{number}@gateway)
 same => n,Goto(voice-inbound,s,1)

[reminder-answered]
exten => s,1,Goto(voice-outbound,s,1)
"""
    )


def main():
    if os.geteuid() != 0:
        raise SystemExit("Run with sudo on the hackathon VM")
    template = Path("/opt/hackathon/sip/pjsip.conf").read_text()
    number, gateway = gateway_configuration(template)
    auth = ROOT / "var/asterisk/pjsip_auth.conf"
    manager = ROOT / "var/asterisk/manager.conf"
    if not auth.is_file() or not manager.is_file():
        raise SystemExit("Run make setup first")
    base = (
        (ROOT / "asterisk/pjsip.conf")
        .read_text()
        .replace("bind=127.0.0.1:5060", "bind=0.0.0.0:5060")
    )
    files = {
        "pjsip.conf": base + "\n" + gateway,
        "pjsip_auth.conf": auth.read_text(),
        "extensions.conf": dialplan(number),
        "manager.conf": manager.read_text(),
        "rtp.conf": "[general]\nrtpstart=10000\nrtpend=20000\nicesupport=false\n",
        "modules.conf": "[modules]\nautoload=yes\nnoload => chan_sip.so\n",
        "http.conf": "[general]\nenabled=no\n",
    }
    directory = Path("/etc/asterisk")
    # This installer runs with Ubuntu 22.04's system Python 3.10 under sudo.
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ")  # noqa: UP017
    backup = directory / ("before-tochka-" + stamp)
    backup.mkdir(mode=0o700)
    group = grp.getgrnam("asterisk").gr_gid
    for name, content in files.items():
        target = directory / name
        if target.exists():
            shutil.copy2(target, backup / name)
        # Restrict existing files before writing any credentials.
        fd = os.open(target, os.O_CREAT | os.O_WRONLY | os.O_TRUNC, 0o640)
        os.fchmod(fd, 0o640)
        os.fchown(fd, 0, group)
        with os.fdopen(fd, "w") as stream:
            stream.write(content)
    print(f"Asterisk configuration installed for team {number}; backup: {backup}")
    print("Restart separately: sudo systemctl restart asterisk")


if __name__ == "__main__":
    main()
