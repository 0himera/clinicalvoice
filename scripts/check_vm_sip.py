#!/usr/bin/env python3
"""Temporarily switch the VM core to a fresh mock, check real speech/SIP, restore MIS.

Requires passwordless sudo and the installed systemd services. Do not run during
a call or with a softphone registered as 200, 101, 102 or 104. Models stay loaded;
the original .env and MIS database are never rewritten.
"""

import argparse
import json
import os
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DROPIN = Path("/run/systemd/system/tochka-core.service.d/90-sip-check.conf")
ENVIRONMENT = Path("/run/tochka-voice-sip-check.env")
CHECKS = {
    "test_voice_simulation.py": 240,
    "sip_smoke.py": 300,
    "sip_outbound_smoke.py": 600,
    "sip_handoff_smoke.py": 180,
}


def sudo(*args: str):
    subprocess.run(["sudo", "-n", *args], check=True)


def wait_core(mode: str | None = None):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    deadline = time.monotonic() + 120
    while True:
        try:
            with opener.open("http://127.0.0.1:8080/readyz", timeout=3) as response:
                ready = json.load(response)
            if ready.get("status") == "ok" and (mode is None or ready.get("mode") == mode):
                return ready["mode"]
        except (OSError, ValueError, urllib.error.URLError):
            pass
        if time.monotonic() >= deadline:
            raise RuntimeError("core did not become ready; inspect systemd journal")
        time.sleep(0.5)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--only", nargs="+", choices=CHECKS, help="Run only the selected checks")
    args = parser.parse_args()
    os.umask(0o077)
    os.chdir(ROOT)
    if DROPIN.exists() or ENVIRONMENT.exists():
        raise SystemExit("A previous SIP check override exists; inspect it before another run")
    for service in ("tochka-core", "tochka-ai-worker", "asterisk"):
        subprocess.run(["systemctl", "is-active", "--quiet", service], check=True)
    original_mode = wait_core()
    channels = subprocess.check_output(
        ["sudo", "-n", "asterisk", "-rx", "core show channels count"], text=True
    )
    if not channels.startswith("0 active channels"):
        raise SystemExit("Wait for active calls to finish before running this check")
    contacts = subprocess.check_output(
        ["sudo", "-n", "asterisk", "-rx", "pjsip show contacts"], text=True
    )
    if any(f"Contact:  {number}/" in contacts for number in ("200", "101", "102", "104")):
        raise SystemExit("Close diagnostic/operator softphones before running this check")
    tag = uuid.uuid4().hex[:12]
    database = ROOT / "var" / f"vm-sip-check-{tag}.db"
    results = []
    with tempfile.TemporaryDirectory(prefix="vm-sip-check-", dir=ROOT / "var") as temporary:
        directory = Path(temporary)
        environment = directory / "environment"
        environment.write_text(
            f"MIS_API_TOKEN=\nDATABASE_PATH={database}\n"
            "ALLOW_DEMO_CALLS_OUTSIDE_WINDOW=true\nDEBUG_DIALOGUE=false\nTRANSCRIPT_LOGGING=false\n"
        )
        override = directory / "override"
        override.write_text(f"[Service]\nEnvironmentFile={ENVIRONMENT}\n")
        installed = False
        try:
            sudo("install", "-d", "-m", "755", str(DROPIN.parent))
            sudo("install", "-m", "600", str(environment), str(ENVIRONMENT))
            installed = True
            sudo("install", "-m", "644", str(override), str(DROPIN))
            sudo("systemctl", "daemon-reload")
            sudo("systemctl", "restart", "tochka-core")
            wait_core("mock")
            print("Fresh mock core ready; working MIS is disconnected for these checks", flush=True)
            for name in args.only or CHECKS:
                timeout = CHECKS[name]
                print(f"Checking {name}", flush=True)
                result = subprocess.run(
                    [str(ROOT / "ai-worker/.venv/bin/python"), "-u", str(ROOT / "scripts" / name)],
                    timeout=timeout,
                    env={**os.environ, "CORE_URL": "http://127.0.0.1:8080"},
                )
                results.append({"script": name, "passed": result.returncode == 0})
                if result.returncode:
                    raise RuntimeError(
                        f"{name} failed; fictional test database retained at {database}"
                    )
        finally:
            if installed:
                if DROPIN.exists():
                    sudo("unlink", str(DROPIN))
                sudo("unlink", str(ENVIRONMENT))
                sudo("systemctl", "daemon-reload")
                sudo("systemctl", "restart", "tochka-core")
                wait_core(original_mode)
                print("Original core configuration restored and ready", flush=True)
            (ROOT / "var/vm-sip-check-results.json").write_text(
                json.dumps({"database": str(database), "checks": results}, indent=2) + "\n"
            )
    print(
        "All requested speech/SIP checks passed; synthetic visit evidence is retained locally",
        flush=True,
    )


if __name__ == "__main__":
    main()
