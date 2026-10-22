"""Check generated Asterisk routing rather than a shell-escaped copy of it."""

import importlib.util
from pathlib import Path


def test_gateway_routes_use_project_audio_and_outbound_contexts():
    root = Path(__file__).resolve().parents[2]
    spec = importlib.util.spec_from_file_location(
        "gateway_installer", root / "scripts/install_hackathon_asterisk.py"
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    number, configuration = module.gateway_configuration(
        "[transport-udp]\ntype=transport\n"
        "[gateway]\ntype=registration\ncontact_user = 078\n"
        "[gateway]\ntype=auth\npassword=fixture-only\n"
    )
    assert number == "078"
    assert "type=transport" not in configuration
    assert configuration.count("[gateway]") == 2
    plan = module.dialplan(number)
    assert "Goto(voice-inbound,s,1)" in plan
    assert "[voice-outbound]" in plan
    assert "AGI(agi://127.0.0.1:4573/start,outbound,${VISIT_ID})" in plan
    assert "AudioSocket(${VOICE_ID},127.0.0.1:9092)" in plan
    assert "Set(CALLBACK=${CUT(CALLERID(num),@,1)})" in plan
    assert "Originate(PJSIP/${CALLBACK}@gateway,exten,reminder-answered,s,1,45,a)" in plan
    assert "[reminder-answered]" in plan
    assert plan.count("exten => h,1") >= 2
    assert "$\\{" not in plan
