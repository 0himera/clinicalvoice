import pytest
from httpx import ASGITransport, AsyncClient

from src.decision import FakeDecisionEngine
from src.services import Services
from src.settings import Settings
from src.voice_bridge import create_app


class DummyService:
    def transcribe_wav(self, audio: bytes) -> str:
        return "тест"

    def synthesize(self, text: str, sample_rate: int = 24000, speaker: str | None = None) -> bytes:
        return b"RIFFfake"

    def new_stream(self):
        return None


def test_fake_decision_engine_choice_and_noul():
    engine = FakeDecisionEngine(device="cpu")
    engine.warmup()

    state = {
        "шаг_диалога": "STATE_MAIN_MENU",
        "ответ_пациента": "Здравствуйте! Запишите меня пожалуйста к терапевту",
    }
    questions = {
        "intent": {
            "type": "choice",
            "question": "Каково намерение пациента?",
            "criteria": {
                "BOOKING": "Запись на прием к врачу",
                "CANCEL": "Отмена существующей записи",
                "INFO": "Справочный вопрос о клинике",
            },
        },
        "vital_threat": {
            "type": "noul",
            "question": "Сообщает ли пациент об острой угрозе жизни прямо сейчас?",
        },
        "doctor_name": {
            "type": "excerpt",
            "question": "Упоминание специальности или врача",
        },
    }

    answers = engine.decide(state, questions)
    assert "intent" in answers
    assert answers["intent"]["type"] == "choice"
    assert answers["intent"]["choice"] == "BOOKING"
    assert answers["intent"]["probabilities"]["BOOKING"] >= 0.80

    assert "vital_threat" in answers
    assert answers["vital_threat"]["type"] == "noul"
    assert answers["vital_threat"]["noul"] < 0.15

    assert "doctor_name" in answers
    assert answers["doctor_name"]["type"] == "excerpt"
    assert answers["doctor_name"]["text"] == "терапевт"
    assert answers["doctor_name"]["start"] >= 0


def test_fake_decision_engine_vital_threat_negation_filter():
    engine = FakeDecisionEngine(device="cpu")

    # Real emergency
    real_res = engine.decide(
        "Мне очень плохо, сильная боль в груди и задыхается",
        {"vital_threat": {"type": "noul", "question": "Острая угроза жизни?"}},
    )
    assert real_res["vital_threat"]["noul"] > 0.90

    # Negation
    neg_res = engine.decide(
        "У меня температура 37.5, но я не задыхаюсь и боли в груди нет",
        {"vital_threat": {"type": "noul", "question": "Острая угроза жизни?"}},
    )
    assert neg_res["vital_threat"]["noul"] < 0.15

    # Metaphor
    met_res = engine.decide(
        "У меня сердце кровью обливается, что талонов нет",
        {"vital_threat": {"type": "noul", "question": "Острая угроза жизни?"}},
    )
    assert met_res["vital_threat"]["noul"] < 0.15


@pytest.mark.asyncio
async def test_decision_http_endpoint():
    dummy = DummyService()
    services = Services(
        asr=dummy,
        tts=dummy,
        vad=dummy,
        decision=FakeDecisionEngine(device="cpu"),
        device="cpu",
    )
    settings = Settings(
        telephony=False, decision_mock=True, decision_device="cpu", api_token="test-token"
    )
    app = create_app(settings=settings, services=services)

    async with (
        app.router.lifespan_context(app),
        AsyncClient(
            transport=ASGITransport(app=app),
            base_url="http://test",
            headers={"Authorization": "Bearer test-token"},
        ) as client,
    ):
        health_resp = await client.get("/health")
        assert health_resp.status_code == 200
        assert health_resp.json()["decision_ready"] is True

        payload = {
            "state": {"контекст": "подтверждение", "реплика": "Да, подходит"},
            "questions": {
                "confirm": {
                    "type": "choice",
                    "question": "Подтверждение?",
                    "criteria": {"AFFIRM": "Согласие", "DENY": "Отказ"},
                }
            },
        }
        decide_resp = await client.post("/v1/decide", json=payload)
        assert decide_resp.status_code == 200
        data = decide_resp.json()
        assert data["model"] == "mock"
        assert "answers" in data
        assert data["answers"]["confirm"]["choice"] == "AFFIRM"
        assert "latency_ms" in data


def test_fake_decision_engine_english_prompts():
    engine = FakeDecisionEngine(device="cpu")

    # Confirmation and aside intent (English schema from commit aa7f343)
    state = {
        "dialogue_step": "confirm_booking",
        "last_prompt": "Вы записаны к терапевту Богдановой на завтра в 10:00. Подтверждаете запись?",
        "held_slot": "терапевт Богданова, 10:00",
        "patient_utterance": "Да, подтверждаю, а какой этаж и номер кабинета?",
    }
    questions = {
        "confirmation": {
            "type": "choice",
            "question": "Does the patient confirm the proposed appointment slot?",
            "criteria": {
                "AFFIRM": "Yes, patient agrees and confirms appointment",
                "DENY": "No, patient rejects appointment or cancels",
                "CHANGE_SLOT": "Patient asks for another date, day or time",
                "UNCERTAIN": "Patient is hesitant, unsure or asks questions without confirming",
            },
        },
        "aside_intent": {
            "type": "choice",
            "question": "Did the patient ask an additional FAQ or informational question?",
            "criteria": {
                "NONE": "No additional question",
                "FAQ_LOCATION": "Question about cabinet, room number, floor, or clinic address",
                "FAQ_ANALYSIS": "Question about blood tests, preparation or fluorography",
                "FAQ_DOCUMENTS": "Question about required documents (passport, insurance policy)",
                "FAQ_PRICE": "Question about prices, costs or paid services",
            },
        },
    }
    answers = engine.decide(state, questions)
    assert answers["confirmation"]["choice"] == "AFFIRM"
    assert answers["confirmation"]["confidence"] >= 0.80
    assert answers["aside_intent"]["choice"] == "FAQ_LOCATION"
    assert answers["aside_intent"]["confidence"] >= 0.80

    # Slot choice
    slot_state = {
        "dialogue_step": "choose_slot",
        "option_1": "10:00",
        "option_2": "15:30",
        "patient_preference": "давайте тот что попозже, второй вариант",
    }
    slot_questions = {
        "slot_choice": {
            "type": "choice",
            "question": "Which of the two offered appointment slots does the patient want?",
            "criteria": {
                "OPTION_1": "Option 1: 10:00",
                "OPTION_2": "Option 2: 15:30",
                "DECLINE_BOTH": "Patient declines both offered options or asks for a different time",
                "UNCERTAIN": "Unclear or unrelated response",
            },
        }
    }
    slot_ans = engine.decide(slot_state, slot_questions)
    assert slot_ans["slot_choice"]["choice"] == "OPTION_2"

    # Doctor excerpt with English question
    doc_state = {
        "dialogue_step": "find_doctor",
        "patient_utterance": "запишите к терапевту Богдановой пожалуйста",
    }
    doc_questions = {
        "doctor_name": {
            "type": "excerpt",
            "question": "Surname of the doctor the patient wants to visit",
        }
    }
    doc_ans = engine.decide(doc_state, doc_questions)
    assert doc_ans["doctor_name"]["text"] == "Богдановой"
    assert doc_ans["doctor_name"]["start"] == 21
    assert doc_ans["doctor_name"]["end"] == 31

    # Vital threat with English question
    threat_questions = {
        "vital_threat": {
            "type": "noul",
            "question": "Is the patient reporting an acute, real physical heart attack, severe vital choking or medical emergency right now, excluding negations and emotional metaphors?",
        }
    }
    ans_real = engine.decide(
        "Мне очень плохо, сильная боль в груди и задыхается",
        threat_questions,
    )
    assert ans_real["vital_threat"]["noul"] > 0.90

    ans_neg = engine.decide(
        "У меня температура 37.5, но я не задыхаюсь и боли в груди нет",
        threat_questions,
    )
    assert ans_neg["vital_threat"]["noul"] < 0.15

    # Choice keywords verification (English & Russian matching)
    keywords_test = [
        ("I need to book an appointment with a doctor", "BOOKING"),
        ("Please cancel my appointment", "CANCEL"),
        ("I want to reschedule to another date", "RESCHEDULE"),
        ("Where is the clinic address and what floor?", "FAQ_LOCATION"),
        ("Do I need prep for a blood test?", "FAQ_ANALYSIS"),
        ("What is the price and cost of services?", "FAQ_PRICE"),
        ("Yes, affirm that", "AFFIRM"),
        ("No, deny that", "DENY"),
        ("Option 1 please, the first one", "OPTION_1"),
        ("Option 2 please, the second one", "OPTION_2"),
    ]
    all_criteria = {
        "BOOKING": "Booking an appointment with a doctor",
        "CANCEL": "Cancelling an existing appointment",
        "RESCHEDULE": "Rescheduling an existing appointment to another date",
        "FAQ_LOCATION": "Question about clinic address, location or floor",
        "FAQ_ANALYSIS": "Question about blood test, laboratory or prep",
        "FAQ_PRICE": "Question about price or cost",
        "AFFIRM": "Yes, affirm or confirm",
        "DENY": "No, deny or cancel",
        "OPTION_1": "Option 1, first option",
        "OPTION_2": "Option 2, second option",
        "NONE": "No match",
    }
    for phrase, expected in keywords_test:
        res = engine.decide(phrase, {"q": {"type": "choice", "criteria": all_criteria}})
        assert res["q"]["choice"] == expected, (
            f"Failed for phrase '{phrase}': got {res['q']['choice']}"
        )


def test_disabled_decision_engine():
    from src.decision import DisabledDecisionEngine

    engine = DisabledDecisionEngine("Test disabled reason")
    engine.warmup()

    with pytest.raises(RuntimeError, match="Decision engine unavailable"):
        engine.decide("state", {"q": {"type": "choice"}})


def test_create_decision_engine_factory():
    from src.decision import DisabledDecisionEngine, create_decision_engine

    # Mock mode enabled
    mock_settings = Settings(decision_mock=True, decision_enabled=True)
    engine = create_decision_engine(mock_settings)
    assert isinstance(engine, FakeDecisionEngine)

    # Mock mode disabled, model unavailable -> returns DisabledDecisionEngine (never silent FakeDecisionEngine)
    real_settings = Settings(
        decision_mock=False,
        decision_enabled=True,
        decision_model_path="non_existent/model_thx01",
    )
    engine_real = create_decision_engine(real_settings)
    assert isinstance(engine_real, DisabledDecisionEngine)
    with pytest.raises(RuntimeError, match="Decision engine unavailable"):
        engine_real.decide("test", {})

    # Disabled mode
    disabled_settings = Settings(decision_mock=False, decision_enabled=False)
    engine_disabled = create_decision_engine(disabled_settings)
    assert isinstance(engine_disabled, DisabledDecisionEngine)


def test_real_engine_always_uses_model_for_non_slavic_names(monkeypatch):
    import contextlib
    import sys
    import types

    from src.decision import THX01Engine

    calls = []

    def backend(agent, state, questions):
        calls.append((state, questions))
        return {
            "answers": {
                "doctor": {
                    "type": "excerpt",
                    "text": "Ли",
                    "start": 2,
                    "end": 4,
                    "probability": 0.73,
                }
            }
        }

    monkeypatch.setitem(
        sys.modules, "torch", types.SimpleNamespace(inference_mode=contextlib.nullcontext)
    )
    monkeypatch.setitem(sys.modules, "thx01.decide", types.SimpleNamespace(decide=backend))
    engine = THX01Engine.__new__(THX01Engine)
    engine.agent = object()
    questions = {"doctor": {"type": "excerpt", "question": "Surname of doctor"}}
    for text in ("к Ли", "к Оганесян", "к Богдановой", "к хирургу"):
        result = engine.decide(text, questions)
        assert result["doctor"]["probability"] == 0.73
        assert calls[-1] == (text, questions)
    assert len(calls) == 4


@pytest.mark.asyncio
async def test_cancelled_decision_keeps_native_capacity():
    import asyncio
    import threading
    from concurrent.futures import ThreadPoolExecutor

    from src.decision import THX01Engine

    started, finish = threading.Event(), threading.Event()
    calls = []

    def inference(state, questions):
        calls.append(state)
        started.set()
        assert finish.wait(2)
        return {}

    engine = THX01Engine.__new__(THX01Engine)
    engine.decide = inference
    engine._capacity = asyncio.Semaphore(1)
    engine._executor = ThreadPoolExecutor(max_workers=1)
    first = asyncio.create_task(engine.decide_async("first", {}))
    assert await asyncio.to_thread(started.wait, 1)
    first.cancel()
    with pytest.raises(asyncio.CancelledError):
        await first
    try:
        with pytest.raises(TimeoutError):
            await asyncio.wait_for(engine.decide_async("cancelled-in-queue", {}), 0.02)
        assert calls == ["first"]
    finally:
        finish.set()
    assert await engine.decide_async("next", {}) == {}
    assert calls == ["first", "next"]
    engine.close()
