import pytest

from src.normalizer import normalize_russian_text


@pytest.mark.parametrize(
    ("source", "expected"),
    [
        ("с 7:30 до 20:00", "с семи тридцати до двадцати"),
        ("в 14:45", "в четырнадцать сорок пять"),
        ("15 апреля 1985 года", "пятнадцатого апреля тысяча девятьсот восемьдесят пятого года"),
        ("кабинет № 210", "кабинет номер двести десять"),
        ("103 и 112", "сто три и сто двенадцать"),
        ("31.10.2026", "тридцать первого октября две тысячи двадцать шестого года"),
    ],
)
def test_pronunciation(source, expected):
    assert normalize_russian_text(source) == expected


def test_invalid_calendar_date_rejected():
    with pytest.raises(ValueError):
        normalize_russian_text("31.02.2026")
