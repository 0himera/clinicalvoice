"""Deterministic pronunciation of the formats used by the MIS contract."""

import re
from datetime import date

from num2words import num2words

MONTHS = (
    "января",
    "февраля",
    "марта",
    "апреля",
    "мая",
    "июня",
    "июля",
    "августа",
    "сентября",
    "октября",
    "ноября",
    "декабря",
)


def number(value: int, *, ordinal: bool = False, genitive: bool = False) -> str:
    return num2words(
        value, lang="ru", to="ordinal" if ordinal else "cardinal", case="g" if genitive else "n"
    )


def normalize_russian_text(text: str) -> str:
    def numeric_date(match: re.Match) -> str:
        day, month, year = map(int, match.groups())
        date(year, month, day)  # invalid dates must not be pronounced as valid ones
        return f"{number(day, ordinal=True, genitive=True)} {MONTHS[month - 1]} {number(year, ordinal=True, genitive=True)} года"

    text = re.sub(r"(?<!\d)(\d{2})\.(\d{2})\.(\d{4})(?!\d)", numeric_date, text)

    def named_date(match: re.Match) -> str:
        day = int(match[1])
        if not 1 <= day <= 31:
            raise ValueError("invalid day")
        return f"{number(day, ordinal=True, genitive=True)} {match[2]}"

    text = re.sub(r"\b(\d{1,2})\s+(" + "|".join(MONTHS) + r")\b", named_date, text)
    text = re.sub(
        r"\b(\d{4})\s+года\b",
        lambda m: number(int(m[1]), ordinal=True, genitive=True) + " года",
        text,
    )

    def clock(match: re.Match) -> str:
        prefix, hour, minute = match.groups()
        h, m = int(hour), int(minute)
        if not 0 <= h <= 23 or not 0 <= m <= 59:
            raise ValueError("invalid clock time")
        genitive = prefix in {"с", "до"}
        result = (prefix + " " if prefix else "") + number(h, genitive=genitive)
        if m:
            result += " " + number(m, genitive=genitive)
        elif not genitive:
            result += " ноль ноль"
        return result

    text = re.sub(r"\b(?:(с|до|в)\s+)?(\d{1,2}):(\d{2})\b", clock, text)
    for symbol, replacement in {
        "№": " номер ",
        "₽": " рублей ",
        "%": " процентов ",
        "+": " плюс ",
    }.items():
        text = text.replace(symbol, replacement)
    text = re.sub(r"\bдоб\.?\s*(\d+)", lambda m: "добавочный " + number(int(m[1])), text)
    text = re.sub(
        r"\b\d+\b",
        lambda m: number(int(m[0])) if len(m[0]) <= 6 else " ".join(number(int(d)) for d in m[0]),
        text,
    )
    return re.sub(r"\s+", " ", text).strip()
