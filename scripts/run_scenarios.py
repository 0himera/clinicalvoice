#!/usr/bin/env python3
"""Data-driven scenario runner for Tochka Voice.

Runs declarative test scenarios from data/scenarios_dataset.json against
an isolated or existing Go core service, optionally using real local TTS/ASR.
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time
import uuid
from contextlib import contextmanager
from pathlib import Path

from client import VoiceClient
from dotenv import load_dotenv

ROOT = Path(__file__).resolve().parent.parent
load_dotenv(ROOT / ".env", override=False)


@contextmanager
def isolated_server(port: int = 18085):
    """Spins up an isolated Go server with a clean mock database."""
    db_path = ROOT / "var" / f"scenario-runner-{uuid.uuid4().hex[:8]}.db"
    env = os.environ.copy()
    llm_allow_remote = os.getenv("LLM_ALLOW_REMOTE", "false").lower() in ("true", "1")
    default_base_url = "http://127.0.0.1:11434/v1"
    raw_base_url = os.getenv("LLM_BASE_URL", default_base_url)
    is_loopback = any(h in raw_base_url for h in ("127.0.0.1", "localhost", "::1"))
    final_base_url = raw_base_url if (llm_allow_remote or is_loopback) else default_base_url

    env.update(
        {
            "PORT": str(port),
            "HTTP_PORT": str(port),
            "CORE_HOST": "127.0.0.1",
            "CORE_URL": f"http://127.0.0.1:{port}",
            "MIS_API_TOKEN": "",
            "DATABASE_PATH": str(db_path),
            "ASTERISK_AMI_SECRET": "",
            "DEBUG_DIALOGUE": "false",
            "ALLOW_DEMO_CALLS_OUTSIDE_WINDOW": "false",
            "LLM_PROVIDER": os.getenv("LLM_PROVIDER", "ollama"),
            "LLM_MODEL": os.getenv("LLM_MODEL", "gemma4:e4b"),
            "LLM_BASE_URL": final_base_url,
            "LLM_API_KEY": os.getenv("LLM_API_KEY", ""),
            "LLM_ALLOW_REMOTE": os.getenv("LLM_ALLOW_REMOTE", "false"),
            "LLM_TIMEOUT_SEC": os.getenv("LLM_TIMEOUT_SEC", "12"),
            "LLM_MAX_TOKENS": os.getenv("LLM_MAX_TOKENS", "256"),
            "AI_API_TOKEN": os.getenv("AI_API_TOKEN", ""),
            "DECISION_MODEL_URL": os.getenv(
                "DECISION_MODEL_URL", "http://127.0.0.1:8000/v1/decide"
            ),
            "DECISION_MODEL_ENABLED": os.getenv("DECISION_MODEL_ENABLED", "true"),
        }
    )
    xlsx_files = list((ROOT / "data").glob("*.xlsx"))
    if xlsx_files:
        env["SCHEDULE_PATH"] = str(xlsx_files[0])
    (ROOT / "var").mkdir(parents=True, exist_ok=True)
    log_path = ROOT / "var" / f"isolated-server-{port}.log"
    with log_path.open("w") as log:
        os.chmod(log_path, 0o600)
        proc = subprocess.Popen(
            [str(ROOT / "go-core/bin/server")],
            cwd=ROOT,
            env=env,
            stdout=log,
            stderr=subprocess.STDOUT,
        )
        try:
            deadline = time.monotonic() + 15
            import httpx

            with httpx.Client(timeout=2, trust_env=False) as client:
                while True:
                    try:
                        resp = client.get(f"http://127.0.0.1:{port}/healthz")
                        resp.raise_for_status()
                        if resp.json().get("mode") == "mock":
                            break
                    except httpx.HTTPError:
                        if proc.poll() is not None or time.monotonic() > deadline:
                            err_content = log_path.read_text() if log_path.exists() else "no log"
                            raise RuntimeError(
                                f"Isolated server on port {port} failed to start: {err_content}"
                            ) from None
                        time.sleep(0.2)
            os.environ["CORE_URL"] = f"http://127.0.0.1:{port}"
            yield env
        finally:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
            for p in db_path.parent.glob(f"{db_path.name}*"):
                p.unlink(missing_ok=True)
            log_path.unlink(missing_ok=True)


def run_scenario(
    scenario: dict,
    client: VoiceClient,
    voice: bool = False,
    verbose: bool = False,
) -> tuple[bool, str, float]:
    """Runs a single scenario from the dataset. Returns (passed, error_msg, duration)."""
    started = time.monotonic()
    scenario_id = scenario["id"]
    caller = scenario.get("caller_number", "")

    try:
        before_visits = client.visits()
        outbound_visit_id = scenario.get("outbound_visit_id", 0)
        if scenario.get("setup_booking"):
            setup = scenario["setup_booking"]
            client.start(caller_number=setup.get("caller_number", ""))
            for step_user in setup.get("turns", []):
                client.turn(step_user)
            client.end_call()
            active_visits = client.visits()
            if active_visits:
                outbound_visit_id = active_visits[-1]["id"]
            before_visits = active_visits

        init_res = client.start(caller_number=caller, outbound_visit_id=outbound_visit_id)
        if verbose:
            print(f"[{scenario_id}] START → {init_res.get('state')} | {init_res.get('bot_speech')}")

        for step in scenario.get("steps", []):
            turn_no = step.get("turn", 0)
            user_text = step["user"]

            if voice:
                audio = client.speak(user_text, "baya")
                spoken_text = client.transcribe(audio)
            else:
                spoken_text = user_text

            res = client.turn(spoken_text)
            curr_state = res.get("state")
            exp_state = step.get("expected_state")

            if verbose:
                outcome_str = f" [{res.get('outcome')}]" if res.get("outcome") else ""
                print(f"[{scenario_id}] T{turn_no}: '{spoken_text}' → {curr_state}{outcome_str}")

            if exp_state and curr_state != exp_state:
                return (
                    False,
                    f"Turn {turn_no}: state mismatch. Expected '{exp_state}', got '{curr_state}' (bot: {res.get('bot_speech')})",
                    time.monotonic() - started,
                )

            for fragment in step.get("expected_fragments", []):
                bot_speech = res.get("bot_speech", "")
                if fragment.lower() not in bot_speech.lower():
                    return (
                        False,
                        f"Turn {turn_no}: fragment '{fragment}' missing in bot speech: '{bot_speech}'",
                        time.monotonic() - started,
                    )

            if "expected_outcome" in step:
                exp_outcome = step["expected_outcome"]
                curr_outcome = res.get("outcome", "")
                if curr_outcome != exp_outcome:
                    return (
                        False,
                        f"Turn {turn_no}: outcome mismatch. Expected '{exp_outcome}', got '{curr_outcome}'",
                        time.monotonic() - started,
                    )

            if "transfer_target" in step:
                exp_target = step["transfer_target"]
                curr_target = res.get("transfer_target", "")
                if curr_target != exp_target:
                    return (
                        False,
                        f"Turn {turn_no}: transfer target mismatch. Expected '{exp_target}', got '{curr_target}'",
                        time.monotonic() - started,
                    )

            if step.get("require_nlu"):
                if not res.get("llm_used"):
                    return (
                        False,
                        f"Turn {turn_no}: NLU was required (llm_used=True) but was not used.",
                        time.monotonic() - started,
                    )

        client.end_call()

        assertions = scenario.get("assertions", {})
        if "visits_delta" in assertions:
            after_visits = client.visits()
            delta = len(after_visits) - len(before_visits)
            expected_delta = assertions["visits_delta"]
            if delta != expected_delta:
                return (
                    False,
                    f"Visits delta mismatch: expected {expected_delta:+d}, got {delta:+d}",
                    time.monotonic() - started,
                )

        if "expected_visit_status" in assertions:
            after_visits = client.visits()
            expected_status = assertions["expected_visit_status"]
            if not after_visits or after_visits[-1].get("status") != expected_status:
                status = after_visits[-1].get("status") if after_visits else "NO_VISITS"
                return (
                    False,
                    f"Visit status mismatch: expected '{expected_status}', got '{status}'",
                    time.monotonic() - started,
                )

        if assertions.get("same_visit_id"):
            after_visits = client.visits()
            if before_visits and after_visits:
                orig_id = before_visits[0]["id"]
                new_id = after_visits[0]["id"]
                if orig_id != new_id:
                    return (
                        False,
                        f"Reschedule failed: visit ID changed from {orig_id} to {new_id}",
                        time.monotonic() - started,
                    )

        return True, "", time.monotonic() - started

    except Exception as exc:
        client.end_call()
        return False, f"Exception: {exc}", time.monotonic() - started


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--dataset",
        type=Path,
        default=ROOT / "data" / "scenarios_dataset.json",
        help="Path to scenarios JSON dataset",
    )
    parser.add_argument(
        "--filter",
        "-k",
        type=str,
        default="",
        help="Filter scenarios by ID or substring in name",
    )
    parser.add_argument(
        "--category",
        "-c",
        type=str,
        default="",
        help="Filter scenarios by category",
    )
    parser.add_argument(
        "--voice",
        action="store_true",
        help="Use real local TTS and ASR for each turn",
    )
    parser.add_argument(
        "--isolated",
        action="store_true",
        default=True,
        help="Spin up an isolated mock server process (default: True)",
    )
    parser.add_argument(
        "--no-isolated",
        dest="isolated",
        action="store_false",
        help="Use existing running server from CORE_URL instead of launching an isolated server",
    )
    parser.add_argument(
        "--port",
        type=int,
        default=18085,
        help="Port for isolated server (default: 18085)",
    )
    parser.add_argument(
        "--stop-on-fail",
        action="store_true",
        help="Stop on first failing scenario",
    )
    parser.add_argument(
        "--verbose",
        "-v",
        action="store_true",
        help="Print turn-by-turn details",
    )
    parser.add_argument(
        "--list",
        action="store_true",
        help="List available scenarios and exit",
    )
    args = parser.parse_args()

    if not args.dataset.exists():
        print(f"Error: dataset file '{args.dataset}' not found", file=sys.stderr)
        sys.exit(1)

    with args.dataset.open("r", encoding="utf-8") as f:
        scenarios: list[dict] = json.load(f)

    if args.list:
        print(f"Available scenarios in {args.dataset.name} ({len(scenarios)} total):\n")
        print(f"{'ID':<8} {'Category':<18} {'Steps':<6} {'Name'}")
        print("-" * 75)
        for s in scenarios:
            print(
                f"{s['id']:<8} {s.get('category', ''):<18} {len(s.get('steps', [])):<6} {s.get('name', '')}"
            )
        return

    # Filter
    selected = []
    for s in scenarios:
        if args.filter:
            needle = args.filter.lower()
            if (
                needle not in s["id"].lower()
                and needle not in s.get("name", "").lower()
                and needle not in s.get("category", "").lower()
            ):
                continue
        if args.category:
            if s.get("category", "").lower() != args.category.lower():
                continue
        selected.append(s)

    if not selected:
        print("No scenarios matched the filters.")
        return

    print(f"Selected {len(selected)} scenario(s) [mode: {'VOICE' if args.voice else 'API TEXT'}].")

    runner_context = isolated_server(args.port) if args.isolated else None
    if runner_context:
        print(f"Starting isolated Go server on port {args.port}...")
        runner_context.__enter__()

    passed_count = 0
    failed_count = 0
    results = []

    try:
        client = VoiceClient()
        client.wait_ready(timeout=25)

        for s in selected:
            scenario_id = s["id"]
            name = s.get("name", "")
            print(f"Running {scenario_id}: {name} ... ", end="", flush=True)

            passed, err_msg, dur = run_scenario(s, client, voice=args.voice, verbose=args.verbose)
            if passed:
                passed_count += 1
                status_str = "\033[32mPASS\033[0m"
                print(f"{status_str} ({dur:.2f}s)")
            else:
                failed_count += 1
                status_str = "\033[31mFAIL\033[0m"
                print(f"{status_str} ({dur:.2f}s)")
                print(f"   ↳ Error: {err_msg}")
                if args.stop_on_fail:
                    break

            results.append((scenario_id, name, passed, dur, err_msg))

    finally:
        if runner_context:
            runner_context.__exit__(None, None, None)

    print("\n" + "=" * 60)
    print(f"Summary: {passed_count} passed, {failed_count} failed out of {len(selected)} executed.")
    print("=" * 60)

    if failed_count > 0:
        sys.exit(1)


if __name__ == "__main__":
    main()
