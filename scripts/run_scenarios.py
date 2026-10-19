#!/usr/bin/env python3
"""Data-driven scenario runner for Tochka Voice.

Runs declarative test scenarios from data/scenarios_dataset.json against
an isolated or existing Go core service, optionally using real local TTS/ASR.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import secrets
import subprocess
import sys
import time
import uuid
from contextlib import contextmanager
from pathlib import Path

from client import VoiceClient
from dotenv import load_dotenv
from mock_patients import seed_mock_database

ROOT = Path(__file__).resolve().parent.parent
load_dotenv(ROOT / ".env", override=False)


@contextmanager
def isolated_server(port: int = 18085):
    """Spins up an isolated Go server with a clean mock database."""
    db_path = ROOT / "var" / f"scenario-runner-{uuid.uuid4().hex[:8]}.db"
    env = os.environ.copy()
    core_token = os.getenv("CORE_API_TOKEN") or secrets.token_hex(24)
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
            "CORE_API_TOKEN": core_token,
            "CORE_URL": f"http://127.0.0.1:{port}",
            "MIS_API_TOKEN": "",
            "DATABASE_PATH": str(db_path),
            "ASTERISK_AMI_SECRET": "",
            "DEBUG_DIALOGUE": "false",
            "TRANSCRIPT_LOGGING": "false",
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
    seed_mock_database(db_path)
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
            previous = {key: os.environ.get(key) for key in ("CORE_URL", "CORE_API_TOKEN")}
            try:
                os.environ["CORE_URL"] = f"http://127.0.0.1:{port}"
                os.environ["CORE_API_TOKEN"] = core_token
                yield env
            finally:
                for key, value in previous.items():
                    if value is None:
                        os.environ.pop(key, None)
                    else:
                        os.environ[key] = value
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
    last_visit_id = None
    rescheduled_ids = []
    selected_clock = None

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
            added = [
                v for v in active_visits if v["id"] not in {old["id"] for old in before_visits}
            ]
            assert len(added) == 1 and added[0]["status"] == "Запланирована", (
                "reminder setup must create exactly one planned visit"
            )
            outbound_visit_id = added[0]["id"]
            if "client_id" in setup:
                assert added[0]["client_id"] == setup["client_id"], (
                    "reminder setup selected another patient's visit"
                )
            before_visits = active_visits
        last_visit_id = outbound_visit_id or None

        init_res = client.start(caller_number=caller, outbound_visit_id=outbound_visit_id)
        if verbose:
            print(f"[{scenario_id}] START → {init_res.get('state')} | {init_res.get('bot_speech')}")

        previous_response = init_res
        for step in scenario.get("steps", []):
            turn_no = step.get("turn", 0)
            user_text = step["user"]
            if "{offered_time}" in user_text:
                clocks = re.findall(r"\b\d{2}:\d{2}\b", previous_response.get("bot_speech", ""))
                unique = [clock for clock in clocks if clocks.count(clock) == 1]
                assert unique, "fixture needs an unambiguous time actually offered by the workbook"
                selected_clock = unique[0]
                user_text = user_text.format(offered_time=selected_clock)

            if voice:
                audio = client.speak(user_text, "baya")
                spoken_text = client.transcribe(audio)
            else:
                spoken_text = user_text

            before_turn = {v["id"]: v for v in client.visits()}
            res = client.turn(spoken_text)
            previous_response = res
            after_turn = {v["id"]: v for v in client.visits()}
            added = after_turn.keys() - before_turn.keys()
            removed = before_turn.keys() - after_turn.keys()
            changed = [
                i for i in before_turn.keys() & after_turn.keys() if before_turn[i] != after_turn[i]
            ]
            outcome = step.get("expected_outcome")
            if outcome == "BOOKED":
                assert len(added) == 1 and not removed and not changed, (
                    "booking must create exactly one visit and preserve all others"
                )
                last_visit_id = next(iter(added))
                if selected_clock is not None:
                    assert after_turn[last_visit_id]["scheduled_at"].endswith(
                        " " + selected_clock
                    ), "booked time differs from the spoken choice"
                assert after_turn[last_visit_id]["status"] == "Запланирована", (
                    "new visit must be planned"
                )
            elif outcome in ("RESCHEDULED", "CANCELLED"):
                assert not added and not removed and len(changed) == 1, (
                    "change must preserve IDs and affect exactly one existing visit"
                )
                visit_id = changed[0]
                old, new = before_turn[visit_id], after_turn[visit_id]
                assert last_visit_id is None or visit_id == last_visit_id, (
                    "changed an unrelated visit"
                )
                assert new["client_id"] == old["client_id"], "change reassigned the patient"
                if outcome == "RESCHEDULED":
                    assert new["status"] == "Запланирована" and (
                        new["scheduled_at"],
                        new["doctor"]["id"],
                    ) != (old["scheduled_at"], old["doctor"]["id"]), (
                        "reschedule did not move the same visit"
                    )
                    rescheduled_ids.append(visit_id)
                else:
                    assert (
                        new["status"] == "Отменена"
                        and new["scheduled_at"] == old["scheduled_at"]
                        and new["doctor"]["id"] == old["doctor"]["id"]
                    ), "cancellation changed more than status"
                last_visit_id = visit_id
            else:
                assert before_turn == after_turn, (
                    "visit changed without a declared confirmed operation"
                )
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
            actual = next((v for v in after_visits if v["id"] == last_visit_id), None)
            if actual is None or actual.get("status") != expected_status:
                status = actual.get("status") if actual else "NO_VISIT_CHANGED"
                return (
                    False,
                    f"Visit status mismatch: expected '{expected_status}', got '{status}'",
                    time.monotonic() - started,
                )

        if assertions.get("same_visit_id"):
            assert rescheduled_ids, "no existing visit was actually rescheduled"

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
        client.wait_ready(timeout=25, check_ai=args.voice)
        client.require_mock()

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
