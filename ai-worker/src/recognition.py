"""Recognition quality hints, not a calibrated probability of correctness."""

from dataclasses import dataclass
from math import isfinite


@dataclass(frozen=True)
class Recognition:
    text: str
    needs_repeat: bool = False
    avg_logprob: float | None = None

    @classmethod
    def from_segments(cls, segments):
        segments = list(segments)
        text = " ".join(s.text.strip() for s in segments).strip()
        scores = [float(s.avg_logprob) for s in segments]
        score = min(scores) if scores and all(isfinite(value) for value in scores) else None
        uncertain = not text or score is None or not isfinite(score) or score < -1.0
        return cls(text, uncertain, score if score is not None and isfinite(score) else None)
