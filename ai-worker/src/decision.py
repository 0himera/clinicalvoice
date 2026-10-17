"""Non-autoregressive System-1 Decision Engine using THX-01 (mmBERT).

Evaluates structured state and typed questions (choice, noul, excerpt, number)
in a single forward pass without generating tokens or breaking JSON formats.
Runs CPU/GPU inference in a dedicated worker thread to preserve AudioSocket audio streaming.
"""

from __future__ import annotations

import asyncio
import logging
import os
import re
import time
from concurrent.futures import ThreadPoolExecutor
from typing import Any, Protocol

logger = logging.getLogger(__name__)


class DecisionEngine(Protocol):
    def decide(self, state: Any, questions: dict[str, Any]) -> dict[str, Any]:
        """Synchronously evaluate typed questions on state."""
        ...

    async def decide_async(self, state: Any, questions: dict[str, Any]) -> dict[str, Any]:
        """Asynchronously evaluate typed questions in a worker thread."""
        ...

    def warmup(self) -> None:
        """Run a single forward pass to initialize PyTorch/CUDA allocator."""
        ...


def _fast_doctor_excerpt(target_text: str) -> dict[str, Any] | None:
    """Fast substring extractor for doctor names/specialties without neural multi-pass loops."""
    t_lower = target_text.lower()
    # 1. Known doctor surnames and specialties from clinical stand
    names = (
        "богдановой",
        "богданова",
        "богданов",
        "васильевой",
        "васильева",
        "васильев",
        "ивановой",
        "иванова",
        "иванов",
        "петровой",
        "петрова",
        "петров",
        "смирновой",
        "смирнова",
        "смирнов",
        "сидоровой",
        "сидорова",
        "сидоров",
        "кузнецовой",
        "кузнецова",
        "кузнецов",
        "терапевт",
        "кардиолог",
        "хирург",
        "окулист",
        "невролог",
        "отоларинголог",
        "лор",
        "pediatrician",
        "therapist",
        "cardiologist",
        "doctor",
        "surgeon",
    )
    for name in names:
        pos = t_lower.find(name)
        if pos >= 0:
            start_idx = pos
            end_idx = pos + len(name)
            return {
                "type": "excerpt",
                "text": target_text[start_idx:end_idx],
                "start": start_idx,
                "end": end_idx,
                "probability": 0.95,
            }

    # 2. Match Russian surname declensions following prepositions/titles (e.g. "к Богдановой", "у Васильевой")
    pattern = re.compile(
        r"(?:(?:к|у)\s+(?:врачу\s+|доктору\s+|терапевту\s+)?|врач[уе]?\s+|доктор[уе]?\s+)"
        r"([А-ЯЁа-яё]+(?:ов[ае]?|ев[ае]?|ин[ае]?|ын[ае]?|ск[ио][йяе]?))\b",
        re.IGNORECASE,
    )
    m = pattern.search(target_text)
    if m:
        start_idx = m.start(1)
        end_idx = m.end(1)
        return {
            "type": "excerpt",
            "text": target_text[start_idx:end_idx],
            "start": start_idx,
            "end": end_idx,
            "probability": 0.92,
        }
    return None


class THX01Engine:
    """Wrapper around doofz/THX-01 decision model with isolated thread executor."""

    def __init__(self, device: str = "cpu", model_path: str = "doofz/THX-01"):
        import thx01
        import torch

        self.device = device
        self.model_path = model_path
        if device == "cpu":
            threads = int(os.getenv("THX01_NUM_THREADS", "4"))
            try:
                torch.set_num_interop_threads(1)
            except RuntimeError:
                pass
            torch.set_num_threads(threads)
            logger.info("THX-01 CPU threads set: num_threads=%d, interop=1", threads)

        half = (device == "cuda") and torch.cuda.is_available()
        logger.info(
            "Loading THX-01 decision model from %s (device=%s, half=%s)...",
            model_path,
            device,
            half,
        )
        self.agent = thx01.load(model_path, device=device, half=half)
        self._executor = ThreadPoolExecutor(max_workers=1, thread_name_prefix="thx01-worker")

    def decide(self, state: Any, questions: dict[str, Any]) -> dict[str, Any]:
        import torch
        from thx01.decide import decide as run_decide

        target_text = ""
        if isinstance(state, str):
            target_text = state
        elif isinstance(state, dict):
            for k in (
                "patient_utterance",
                "patient_preference",
                "реплика_пациента",
                "ответ_пациента",
            ):
                if k in state and isinstance(state[k], str):
                    target_text = state[k]
                    break
            if not target_text:
                target_text = " ".join(str(v) for v in state.values())

        fast_answers = {}
        eval_questions = dict(questions)
        for qid, q in list(questions.items()):
            if q.get("type") == "excerpt":
                q_text = str(q.get("question", "")).lower()
                if any(
                    k in q_text
                    for k in ("surname", "doctor", "specialist", "специалист", "фамил", "врач")
                ):
                    extracted = _fast_doctor_excerpt(target_text)
                    if extracted:
                        fast_answers[qid] = extracted
                        eval_questions.pop(qid, None)

        with torch.inference_mode():
            if eval_questions:
                res = run_decide(self.agent, state, eval_questions)
                if isinstance(res, dict) and "answers" in res and isinstance(res["answers"], dict):
                    answers = dict(res["answers"])
                elif isinstance(res, dict):
                    answers = dict(res)
                else:
                    answers = {}
            else:
                answers = {}

            answers.update(fast_answers)
            return answers

    async def decide_async(self, state: Any, questions: dict[str, Any]) -> dict[str, Any]:
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(self._executor, self.decide, state, questions)

    def warmup(self) -> None:
        import torch

        t0 = time.perf_counter()
        dummy_state = {"шаг_диалога": "прогрев", "ответ_пациента": "Здравствуйте"}
        for bs in (1, 2):
            dummy_questions = {
                f"intent_{i}": {
                    "type": "choice",
                    "question": f"Намерение пациента {i}",
                    "criteria": {"GREETING": "Приветствие", "OTHER": "Другое"},
                }
                for i in range(bs)
            }
            try:
                self.decide(dummy_state, dummy_questions)
            except Exception as exc:
                logger.warning("THX-01 warmup bs=%d failed: %s", bs, exc)
        elapsed_ms = (time.perf_counter() - t0) * 1000
        logger.info(
            "THX-01 decision model warmed up successfully in %.1f ms (%s, threads=%d)",
            elapsed_ms,
            self.device,
            torch.get_num_threads(),
        )

    def close(self) -> None:
        self._executor.shutdown(wait=False)


def _has_word(text: str, word: str) -> bool:
    """Check if whole word exists in text for short tokens, or substring for stems/phrases."""
    if len(word) <= 3:
        return bool(re.search(rf"(?<!\w){re.escape(word)}(?!\w)", text, re.IGNORECASE))
    return word in text


def _any_word(text: str, words: tuple[str, ...]) -> bool:
    return any(_has_word(text, w) for w in words)


class FakeDecisionEngine:
    """Fast, deterministic semantic decision engine for CI and unit tests without GPU or downloads."""

    def __init__(self, device: str = "cpu"):
        self.device = device

    def warmup(self) -> None:
        pass

    def decide(self, state: Any, questions: dict[str, Any]) -> dict[str, Any]:
        doc = ""
        user_text = ""
        if isinstance(state, str):
            doc = state
            user_text = state
        elif isinstance(state, dict):
            doc = " ".join(str(v) for v in state.values())
            for u_key in (
                "patient_utterance",
                "patient_preference",
                "реплика_пациента",
                "ответ_пациента",
            ):
                if u_key in state and isinstance(state[u_key], str):
                    user_text = state[u_key]
                    break
            if not user_text:
                user_text = doc

        target_text = user_text if user_text else doc
        t_lower = target_text.lower()
        answers: dict[str, Any] = {}

        for qid, q in questions.items():
            qtype = q.get("type", "choice")
            if qtype == "choice":
                criteria = q.get("criteria", {})
                scores: dict[str, float] = {}
                best_choice = None
                best_score = -1.0

                for opt_key, opt_desc in criteria.items():
                    opt_score = 0.05
                    desc_lower = str(opt_desc).lower()
                    key_lower = opt_key.lower()

                    # Neutral default options
                    if opt_key in ("NONE", "none", "UNCERTAIN", "UNKNOWN"):
                        opt_score = 0.20

                    # Semantic matching heuristics for mock
                    if _has_word(t_lower, key_lower):
                        opt_score += 1.0

                    words = re.findall(r"[\w]+", desc_lower)
                    for w in words:
                        if len(w) >= 4 and w[:4] in t_lower:
                            opt_score += 0.35

                    # Specialized intent heuristics supporting Russian and English
                    if (
                        key_lower == "vital_threat"
                        or opt_key == "EMERGENCY"
                        or any(
                            k in desc_lower
                            for k in ("emergency", "vital threat", "acute threat", "угроз")
                        )
                    ):
                        if any(
                            k in t_lower
                            for k in (
                                "боль в груди",
                                "болит в груди",
                                "давит в груди",
                                "удушье",
                                "инсульт",
                                "не дышит",
                                "chest pain",
                                "heart attack",
                                "choking",
                            )
                        ):
                            if not any(
                                neg in t_lower
                                for neg in (
                                    "не задыха",
                                    "нет боли",
                                    "не болит",
                                    "кровью обливается",
                                    "not choking",
                                    "no chest pain",
                                )
                            ):
                                opt_score += 2.0

                    elif (
                        key_lower in ("affirm", "yes")
                        or opt_key == "AFFIRM"
                        or any(
                            k in desc_lower
                            for k in (
                                "affirm",
                                "yes",
                                "agrees",
                                "confirms",
                                "согласие",
                                "подтвердить",
                            )
                        )
                    ):
                        if _any_word(
                            t_lower,
                            (
                                "да",
                                "подходит",
                                "согласен",
                                "согласна",
                                "подтверждаю",
                                "конечно",
                                "удобно",
                                "хорошо",
                                "буду",
                                "запишите",
                                "давайте",
                                "yes",
                                "sure",
                                "confirm",
                                "fine",
                                "ok",
                                "okay",
                                "agreed",
                                "suits",
                            ),
                        ):
                            if not _any_word(
                                t_lower,
                                (
                                    "не подходит",
                                    "не надо",
                                    "нет",
                                    "не могу",
                                    "не получится",
                                    "no",
                                    "deny",
                                    "cancel",
                                    "not",
                                ),
                            ):
                                opt_score += 2.0

                    elif (
                        key_lower in ("deny", "no")
                        or opt_key == "DENY"
                        or (
                            any(k in desc_lower for k in ("deny", "reject", "cancels", "отказ"))
                            and "reschedule" not in desc_lower
                            and "change" not in desc_lower
                        )
                    ):
                        deny_words = [
                            "нет",
                            "не подходит",
                            "не надо",
                            "не могу",
                            "не получится",
                            "отказываюсь",
                            "no",
                            "deny",
                            "reject",
                            "neither",
                        ]
                        if "CANCEL" not in criteria and "cancel" not in criteria:
                            deny_words.extend(["отмена", "отмените", "cancel"])
                        if _any_word(t_lower, tuple(deny_words)):
                            opt_score += 2.0

                    elif (
                        key_lower in ("change_slot", "reschedule")
                        or opt_key in ("CHANGE_SLOT", "RESCHEDULE")
                        or any(
                            k in desc_lower
                            for k in (
                                "reschedule",
                                "another date",
                                "different time",
                                "change slot",
                                "другой день",
                                "другое время",
                                "перенести",
                            )
                        )
                    ):
                        if any(
                            w in t_lower
                            for w in (
                                "другой",
                                "другое",
                                "позже",
                                "попозже",
                                "раньше",
                                "пораньше",
                                "перенести",
                                "перенесите",
                                "перенос",
                                "reschedule",
                                "another",
                                "later",
                                "earlier",
                                "different time",
                            )
                        ):
                            opt_score += 1.5

                    elif opt_key in ("OPTION_1", "FIRST") or any(
                        k in desc_lower for k in ("option 1", "first option", "первый вариант")
                    ):
                        if _any_word(
                            t_lower,
                            (
                                "первый",
                                "первое",
                                "первую",
                                "вариант 1",
                                "раньше",
                                "пораньше",
                                "утром",
                                "first",
                                "option 1",
                                "1",
                                "one",
                            ),
                        ) and not _any_word(t_lower, ("не первый", "не вариант 1")):
                            opt_score += 2.0

                    elif opt_key in ("OPTION_2", "SECOND") or any(
                        k in desc_lower for k in ("option 2", "second option", "второй вариант")
                    ):
                        if _any_word(
                            t_lower,
                            (
                                "второй",
                                "второе",
                                "вторую",
                                "вариант 2",
                                "позже",
                                "попозже",
                                "вечером",
                                "после обеда",
                                "second",
                                "option 2",
                                "2",
                                "two",
                            ),
                        ) and not _any_word(t_lower, ("не второй", "не вариант 2")):
                            opt_score += 2.0

                    elif opt_key == "DECLINE_BOTH" or any(
                        k in desc_lower for k in ("decline both", "declines both", "отказ от обоих")
                    ):
                        if any(
                            w in t_lower
                            for w in (
                                "ни один",
                                "ни тот ни другой",
                                "оба не",
                                "не подходит ни",
                                "neither",
                                "none of them",
                                "both are bad",
                            )
                        ):
                            opt_score += 2.0

                    elif opt_key == "BOOKING" or any(
                        k in desc_lower for k in ("book", "appointment", "booking", "запис")
                    ):
                        if any(
                            w in t_lower
                            for w in (
                                "запис",
                                "записать",
                                "запишите",
                                "талон",
                                "к врачу",
                                "прием",
                                "приём",
                                "попасть к",
                                "book",
                                "appointment",
                            )
                        ):
                            if not any(
                                neg in t_lower
                                for neg in ("отмен", "перенес", "cancel", "reschedule")
                            ):
                                opt_score += 2.0

                    elif opt_key == "CANCEL" or (
                        any(k in desc_lower for k in ("cancel", "cancelling", "отмен"))
                        and "book" not in desc_lower
                        and "reschedule" not in desc_lower
                    ):
                        if any(
                            w in t_lower
                            for w in (
                                "отмен",
                                "отмените",
                                "отменить",
                                "снять запись",
                                "аннулир",
                                "не приду",
                                "cancel",
                            )
                        ):
                            opt_score += 2.0

                    elif opt_key == "CHECK_STATUS" or any(
                        k in desc_lower for k in ("check_status", "checking details", "провер")
                    ):
                        if any(
                            w in t_lower
                            for w in (
                                "провер",
                                "когда у меня",
                                "во сколько",
                                "напомн",
                                "статус",
                                "check",
                                "when is",
                            )
                        ):
                            opt_score += 2.0

                    elif opt_key == "HOME_VISIT" or any(
                        k in desc_lower for k in ("home visit", "на дом")
                    ):
                        if any(
                            w in t_lower
                            for w in ("на дом", "вызов врача", "вызвать на дом", "home visit")
                        ):
                            opt_score += 2.0

                    elif opt_key == "OPERATOR" or any(
                        k in desc_lower
                        for k in ("operator", "registrar", "human", "оператор", "регистратор")
                    ):
                        if any(
                            w in t_lower
                            for w in (
                                "оператор",
                                "человек",
                                "соедините",
                                "живой",
                                "специалист",
                                "переведи",
                                "operator",
                                "human",
                            )
                        ):
                            opt_score += 2.0

                    elif opt_key == "DIRECTORY_INFO" or any(
                        k in desc_lower
                        for k in (
                            "directory of doctors",
                            "directory",
                            "specialties",
                            "список врачей",
                        )
                    ):
                        if any(
                            w in t_lower
                            for w in (
                                "какие врачи",
                                "список врачей",
                                "какие специалисты",
                                "расписание врачей",
                                "directory",
                                "list of doctors",
                            )
                        ):
                            opt_score += 2.0

                    elif opt_key in ("FAQ_LOCATION", "address") or any(
                        k in desc_lower
                        for k in (
                            "location",
                            "floor",
                            "address",
                            "cabinet",
                            "room number",
                            "адрес",
                            "этаж",
                            "кабинет",
                            "добраться",
                        )
                    ):
                        if any(
                            w in t_lower
                            for w in (
                                "где наход",
                                "какой этаж",
                                "какой кабинет",
                                "где вы",
                                "как доехать",
                                "как добраться",
                                "адрес",
                                "кабинет",
                                "этаж",
                                "корпус",
                                "location",
                                "floor",
                                "address",
                                "where",
                                "room",
                            )
                        ):
                            opt_score += 2.0

                    elif opt_key in ("FAQ_ANALYSIS", "blood_test") or any(
                        k in desc_lower
                        for k in (
                            "blood",
                            "test",
                            "prep",
                            "analysis",
                            "laboratory",
                            "анализ",
                            "кров",
                        )
                    ):
                        if any(
                            w in t_lower
                            for w in (
                                "анализ",
                                "кров",
                                "натощак",
                                "подготовк",
                                "забор",
                                "процедурн",
                                "сдать кровь",
                                "blood",
                                "test",
                                "analysis",
                                "prep",
                                "lab",
                            )
                        ):
                            opt_score += 2.0

                    elif opt_key == "fluorography" or any(
                        k in desc_lower for k in ("fluorography", "x-ray", "флюорограф", "рентген")
                    ):
                        if any(
                            w in t_lower
                            for w in (
                                "флюорограф",
                                "рентген",
                                "флюшка",
                                "снимок",
                                "fluorography",
                                "x-ray",
                                "xray",
                            )
                        ):
                            opt_score += 2.0

                    elif opt_key in ("FAQ_DOCUMENTS", "documents") or any(
                        k in desc_lower
                        for k in ("document", "passport", "policy", "документ", "паспорт", "полис")
                    ):
                        if any(
                            w in t_lower
                            for w in (
                                "документ",
                                "паспорт",
                                "полис",
                                "омс",
                                "что взять",
                                "что с собой",
                                "document",
                                "passport",
                                "policy",
                                "insurance",
                            )
                        ):
                            opt_score += 2.0

                    elif opt_key in ("FAQ_PRICE", "prices") or any(
                        k in desc_lower for k in ("price", "cost", "paid", "платн", "стоим", "цен")
                    ):
                        if any(
                            w in t_lower
                            for w in (
                                "стоим",
                                "цен",
                                "скольк",
                                "платн",
                                "рубл",
                                "прайс",
                                "оплат",
                                "price",
                                "cost",
                                "fee",
                                "how much",
                                "pay",
                            )
                        ):
                            opt_score += 2.0

                    elif opt_key in ("FAQ_INFO", "INFO") or any(
                        k in desc_lower for k in ("general faq", "faq_info", "справочный вопрос")
                    ):
                        if any(
                            w in t_lower
                            for w in (
                                "где наход",
                                "какой этаж",
                                "какой кабинет",
                                "адрес",
                                "анализ",
                                "кров",
                                "флюорограф",
                                "рентген",
                                "документ",
                                "паспорт",
                                "полис",
                                "стоим",
                                "цен",
                                "скольк",
                                "платн",
                                "location",
                                "floor",
                                "address",
                                "blood",
                                "test",
                                "document",
                                "price",
                                "cost",
                            )
                        ):
                            opt_score += 2.0

                    scores[opt_key] = opt_score
                    if opt_score > best_score:
                        best_score = opt_score
                        best_choice = opt_key

                # Softmax normalization
                total = sum(scores.values()) or 1.0
                probs = {k: round(v / total, 4) for k, v in scores.items()}
                # Ensure winning choice has calibrated probability
                if best_choice and probs[best_choice] < 0.60:
                    probs[best_choice] = 0.88
                    rem = 0.12 / max(1, len(probs) - 1)
                    for k in probs:
                        if k != best_choice:
                            probs[k] = round(rem, 4)

                conf = probs.get(best_choice, 0.5) if best_choice else 0.5
                answers[qid] = {
                    "type": "choice",
                    "choice": best_choice or list(criteria.keys())[0],
                    "probabilities": probs,
                    "confidence": conf,
                }

            elif qtype == "noul":
                # Binary boolean calibrated probability P(true)
                q_text = str(q.get("question", "")).lower()
                p_true = 0.05
                if any(k in q_text for k in ("threat", "vital", "acute", "угроз", "жизн", "остр")):
                    if any(
                        w in t_lower
                        for w in (
                            "давит в груди",
                            "боль в груди",
                            "болит в груди",
                            "задыхается",
                            "задыхаюсь",
                            "инфаркт",
                            "инсульт",
                            "удушье",
                            "не дышит",
                            "chest pain",
                            "heart attack",
                            "choking",
                            "can't breathe",
                            "cannot breathe",
                            "stroke",
                        )
                    ):
                        if any(
                            neg in t_lower
                            for neg in (
                                "не задыха",
                                "нет боли",
                                "не болит",
                                "кровью обливается",
                                "not choking",
                                "no chest pain",
                                "no pain",
                            )
                        ):
                            p_true = 0.02
                        else:
                            p_true = 0.98
                    elif "кровью обливается" in t_lower or "heart bleeds" in t_lower:
                        p_true = 0.01
                elif _any_word(t_lower, ("да", "верно", "подтверждаю", "yes", "true", "correct")):
                    p_true = 0.95

                answers[qid] = {
                    "type": "noul",
                    "noul": round(p_true, 4),
                    "confidence": round(max(p_true, 1.0 - p_true), 4),
                }

            elif qtype == "excerpt":
                # Span extraction with exact start and end offsets
                q_text = str(q.get("question", "")).lower()
                span_text = None
                start_idx, end_idx = -1, -1
                prob = 0.0

                # Extract doctor name or specialty if requested
                if any(
                    k in q_text
                    for k in ("surname", "doctor", "specialist", "специалист", "фамил", "врач")
                ):
                    extracted = _fast_doctor_excerpt(target_text)
                    if extracted:
                        span_text = extracted["text"]
                        start_idx = extracted["start"]
                        end_idx = extracted["end"]
                        prob = extracted["probability"]

                answers[qid] = {
                    "type": "excerpt",
                    "text": span_text,
                    "start": start_idx,
                    "end": end_idx,
                    "probability": prob,
                }

            elif qtype == "number":
                nums = re.findall(r"\d+(?:[.,]\d+)?", target_text)
                val = float(nums[0].replace(",", ".")) if nums else 0.0
                answers[qid] = {
                    "type": "number",
                    "value": val,
                    "confidence": 0.95 if nums else 0.0,
                }
            else:
                answers[qid] = {"type": qtype, "value": None}

        return answers

    async def decide_async(self, state: Any, questions: dict[str, Any]) -> dict[str, Any]:
        return self.decide(state, questions)


class DisabledDecisionEngine:
    """Decision engine returned when THX-01 is disabled or failed to initialize in production.

    Raises RuntimeError on decide/decide_async to prevent silent fail-open.
    """

    def __init__(self, reason: str = "Decision engine is disabled or failed to initialize"):
        self.reason = reason

    def warmup(self) -> None:
        pass

    def decide(self, state: Any, questions: dict[str, Any]) -> dict[str, Any]:
        raise RuntimeError(f"Decision engine unavailable: {self.reason}")

    async def decide_async(self, state: Any, questions: dict[str, Any]) -> dict[str, Any]:
        raise RuntimeError(f"Decision engine unavailable: {self.reason}")


def create_decision_engine(settings: Any) -> DecisionEngine:
    """Factory to create real THX-01 engine or test mock engine."""
    if getattr(settings, "decision_mock", False):
        logger.info("Using FakeDecisionEngine (mock mode)")
        return FakeDecisionEngine(device=getattr(settings, "decision_device", "cpu"))

    if not getattr(settings, "decision_enabled", True):
        logger.info("Decision engine is disabled in settings")
        return DisabledDecisionEngine("Decision engine is disabled by configuration")

    try:
        from pathlib import Path

        model_path = getattr(settings, "decision_model_path", "doofz/THX-01")
        # Check if local folder with weights exists in var/models/thx01
        local_dir = Path("var/models/thx01")
        if (local_dir / "model.safetensors").exists():
            model_path = str(local_dir)

        device = getattr(settings, "decision_device", "cpu")
        engine = THX01Engine(device=device, model_path=model_path)
        return engine
    except Exception as exc:
        msg = f"Failed to load THX-01 decision model ({type(exc).__name__}: {exc})"
        logger.error("%s; returning DisabledDecisionEngine to prevent silent mock fail-open.", msg)
        return DisabledDecisionEngine(reason=msg)
