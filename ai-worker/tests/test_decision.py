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
    settings = Settings(telephony=False, decision_mock=True, decision_device="cpu")
    app = create_app(settings=settings, services=services)

    async with (
        app.router.lifespan_context(app),
        AsyncClient(transport=ASGITransport(app=app), base_url="http://test") as client,
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
        assert data["model"] == "THX-01"
        assert "answers" in data
        assert data["answers"]["confirm"]["choice"] == "AFFIRM"
        assert "latency_ms" in data
