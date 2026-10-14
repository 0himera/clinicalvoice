import re
import num2words

def normalize_russian_text(text: str) -> str:
    """
    Нормализует русский текст для Silero TTS:
    - Преобразует цифры и числа в слова
    - Расшифровывает время (7:30 -> семь тридцать)
    - Расшифровывает символы №, +, ₽, %
    - Преобразует телефоны и номера кабинетов
    """
    if not text:
        return ""

    # 1. Замена спецсимволов
    text = text.replace("№", " номер ")
    text = text.replace("₽", " рублей ")
    text = text.replace("+", " плюс ")
    text = text.replace("%", " процентов ")
    text = text.replace("—", " - ")

    # 2. Специфика экстренных служб и поликлиники
    text = re.sub(r'\b103\b', 'сто три', text)
    text = re.sub(r'\b112\b', 'сто двенадцать', text)
    text = re.sub(r'\b122\b', 'сто двадцать два', text)
    text = re.sub(r'\bдоб\.?\s*(\d+)', lambda m: f"добавочный {num2words.num2words(int(m.group(1)), lang='ru')}", text)

    # 3. Время: 'с 7:30 до 10:30', '08:00', '19:00'
    hours_genitive = {
        7: "семи", 8: "восьми", 9: "девяти", 10: "десяти", 11: "одиннадцати",
        12: "двенадцати", 13: "тринадцати", 14: "четырнадцати", 15: "пятнадцати",
        16: "шестнадцати", 17: "семнадцати", 18: "восемнадцати", 19: "девятнадцати", 20: "двадцати"
    }
    
    # "с 7:30 до 10:30"
    def replace_from_time(m):
        h = int(m.group(1))
        m_val = int(m.group(2))
        h_word = hours_genitive.get(h, num2words.num2words(h, lang='ru'))
        if m_val == 0:
            return f"с {h_word}"
        elif m_val == 30:
            return f"с {h_word} тридцати"
        elif m_val == 20:
            return f"с {h_word} двадцати"
        else:
            return f"с {h_word} {num2words.num2words(m_val, lang='ru')}"

    text = re.sub(r'с\s+(\d{1,2}):(\d{2})', replace_from_time, text)

    # "до 20:00", "до 15:00"
    def replace_to_time(m):
        h = int(m.group(1))
        m_val = int(m.group(2))
        h_word = hours_genitive.get(h, num2words.num2words(h, lang='ru'))
        if m_val == 0:
            return f"до {h_word}"
        elif m_val == 30:
            return f"до {h_word} тридцати"
        else:
            return f"до {h_word} {num2words.num2words(m_val, lang='ru')}"

    text = re.sub(r'до\s+(\d{1,2}):(\d{2})', replace_to_time, text)

    # Обычное время: 08:00, 14:30
    def replace_time_standalone(m):
        h = int(m.group(1))
        m_val = int(m.group(2))
        h_word = num2words.num2words(h, lang='ru')
        if m_val == 0:
            return f"{h_word} ноль ноль"
        elif m_val == 30:
            return f"{h_word} тридцать"
        else:
            return f"{h_word} {num2words.num2words(m_val, lang='ru')}"

    text = re.sub(r'(\d{1,2}):(\d{2})', replace_time_standalone, text)

    # 4. Даты со склонением: "15 апреля" -> "пятнадцатого апреля"
    months = ["января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"]
    for m_name in months:
        pattern = rf'\b(\d{{1,2}})\s+{m_name}'
        def replace_date(match, mon=m_name):
            day = int(match.group(1))
            day_word = num2words.num2words(day, lang='ru', to='ordinal')
            # Склонение в родительный падеж (пятнадцатое -> пятнадцатого)
            if day_word.endswith("ый"):
                day_word = day_word[:-2] + "ого"
            elif day_word.endswith("ой"):
                day_word = day_word[:-2] + "ого"
            elif day_word.endswith("ий"):
                day_word = day_word[:-2] + "его"
            elif day_word.endswith("е"):
                day_word = day_word[:-1] + "ого"
            return f"{day_word} {mon}"
        text = re.sub(pattern, replace_date, text)

    # "1985 года" -> "тысяча девятьсот восемьдесят пятого года"
    def replace_year(m):
        year = int(m.group(1))
        y_word = num2words.num2words(year, lang='ru', to='ordinal')
        if y_word.endswith("ый") or y_word.endswith("ой"):
            y_word = y_word[:-2] + "ого"
        return f"{y_word} года"

    text = re.sub(r'\b(\d{4})\s+года', replace_year, text)

    # 5. Кабинеты: "кабинет 210" -> "кабинет двести десять"
    def replace_cabinet(m):
        cab_num = int(m.group(1))
        return f"кабинет {num2words.num2words(cab_num, lang='ru')}"
    text = re.sub(r'кабинет\s+(\d+)', replace_cabinet, text)

    # 6. Любые оставшиеся числа
    def replace_any_num(m):
        val = int(m.group(0))
        return num2words.num2words(val, lang='ru')

    text = re.sub(r'\b\d+\b', replace_any_num, text)

    # Удаление лишних пробелов
    text = re.sub(r'\s+', ' ', text).strip()
    return text
