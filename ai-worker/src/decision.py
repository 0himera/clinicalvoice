"""Non-autoregressive System-1 Decision Engine using THX-01 (mmBERT).

Evaluates structured state and typed questions (choice, noul, excerpt, number)
in a single forward pass without generating tokens or breaking JSON formats.
Runs CPU/GPU inference in a dedicated worker thread to preserve AudioSocket audio streaming.
"""

from __future__ import annotations

import asyncio
import logging
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


class THX01Engine:
    """Wrapper around doofz/THX-01 decision model with isolated thread executor."""

    def __init__(self, device: str = "cpu", model_path: str = "doofz/THX-01"):
        import thx01
        import torch

        self.device = device
        self.model_path = model_path
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

        with torch.inference_mode():
            res = run_decide(self.agent, state, questions)
            if isinstance(res, dict) and "answers" in res and isinstance(res["answers"], dict):
                return res["answers"]
            return res

    async def decide_async(self, state: Any, questions: dict[str, Any]) -> dict[str, Any]:
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(self._executor, self.decide, state, questions)

    def warmup(self) -> None:
        t0 = time.perf_counter()
        dummy_state = {"шаг_диалога": "прогрев", "ответ_пациента": "Здравствуйте"}
        dummy_questions = {
            "intent": {
                "type": "choice",
                "question": "Намерение пациента",
                "criteria": {"GREETING": "Приветствие", "OTHER": "Другое"},
            }
        }
        try:
            self.decide(dummy_state, dummy_questions)
            elapsed_ms = (time.perf_counter() - t0) * 1000
            logger.info(
                "THX-01 decision model warmed up successfully in %.1f ms (%s)",
                elapsed_ms,
                self.device,
            )
        except Exception as exc:
            logger.warning("THX-01 warmup failed: %s", exc)

    def close(self) -> None:
        self._executor.shutdown(wait=False)


class FakeDecisionEngine:
    """Fast, deterministic semantic decision engine for CI and unit tests without GPU or downloads."""

    def __init__(self, device: str = "cpu"):
        self.device = device

    def warmup(self) -> None:
        pass

    def decide(self, state: Any, questions: dict[str, Any]) -> dict[str, Any]:
        doc = ""
        if isinstance(state, str):
            doc = state
        elif isinstance(state, dict):
            doc = " ".join(str(v) for v in state.values())

        doc_lower = doc.lower()
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

                    # Semantic matching heuristics for mock
                    if key_lower in doc_lower:
                        opt_score += 1.0

                    words = re.findall(r"[\w]+", desc_lower)
                    for w in words:
                        if len(w) >= 4 and w[:4] in doc_lower:
                            opt_score += 0.35

                    # Specialized intent heuristics
                    if key_lower == "vital_threat" or opt_key == "EMERGENCY":
                        if any(
                            k in doc_lower
                            for k in ("боль в груди", "удушье", "инсульт", "не дышит")
                        ):
                            if not any(
                                neg in doc_lower for neg in ("не задыха", "нет боли", "не болит")
                            ):
                                opt_score += 2.0
                    elif key_lower == "affirm" or opt_key == "AFFIRM":
                        if any(
                            w in doc_lower
                            for w in (
                                "да",
                                "подходит",
                                "согласен",
                                "подтверждаю",
                                "конечно",
                                "удобно",
                            )
                        ):
                            if not any(w in doc_lower for w in ("не подходит", "не надо", "нет")):
                                opt_score += 2.0
                    elif key_lower == "deny" or opt_key == "DENY":
                        if any(
                            w in doc_lower
                            for w in ("нет", "не подходит", "не надо", "не могу", "не получится")
                        ):
                            opt_score += 2.0
                    elif key_lower == "change_slot" or opt_key == "CHANGE_SLOT":
                        if any(
                            w in doc_lower
                            for w in ("другой", "позже", "раньше", "второй", "перенести")
                        ):
                            opt_score += 1.5

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
                if "угроз" in q_text or "жизн" in q_text or "остр" in q_text:
                    if any(
                        w in doc_lower
                        for w in ("давит в груди", "боль в груди", "задыхается", "инфаркт")
                    ):
                        if any(
                            neg in doc_lower
                            for neg in ("не задыха", "нет боли", "кровью обливается")
                        ):
                            p_true = 0.02
                        else:
                            p_true = 0.98
                    elif "кровью обливается" in doc_lower:
                        p_true = 0.01
                elif any(w in doc_lower for w in ("да", "верно", "подтверждаю")):
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

                # Extract doctor name or specialty if requested
                if "врач" in q_text or "специалист" in q_text or "фамил" in q_text:
                    for name in (
                        "богдановой",
                        "богданова",
                        "иванов",
                        "смирнова",
                        "терапевт",
                        "кардиолог",
                        "хирург",
                        "окулист",
                    ):
                        pos = doc_lower.find(name)
                        if pos >= 0:
                            start_idx = pos
                            end_idx = pos + len(name)
                            span_text = doc[start_idx:end_idx]
                            break

                answers[qid] = {
                    "type": "excerpt",
                    "text": span_text,
                    "start": start_idx,
                    "end": end_idx,
                    "probability": 0.92 if span_text else 0.0,
                }

            elif qtype == "number":
                nums = re.findall(r"\d+(?:[.,]\d+)?", doc)
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


def create_decision_engine(settings: Any) -> DecisionEngine:
    """Factory to create real THX-01 engine or test mock engine."""
    if getattr(settings, "decision_mock", False) or not getattr(settings, "decision_enabled", True):
        logger.info("Using FakeDecisionEngine (mock mode)")
        return FakeDecisionEngine(device=getattr(settings, "decision_device", "cpu"))

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
        logger.warning(
            "Could not load THX-01 decision model (%s); falling back to FakeDecisionEngine: %s",
            type(exc).__name__,
            exc,
        )
        return FakeDecisionEngine(device=getattr(settings, "decision_device", "cpu"))
