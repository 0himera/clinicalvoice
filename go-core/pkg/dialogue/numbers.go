package dialogue

import (
	"strconv"
	"strings"
)

var russianNumbers = func() map[string]int {
	out := map[string]int{}
	groups := []struct {
		n     int
		forms string
	}{
		{0, "ноль нулевого"}, {1, "один одна одно первого первое первый"}, {2, "два две второго второе второй"},
		{3, "три третьего третье третий"}, {4, "четыре четвертого четвертое четвертый"}, {5, "пять пятого пятое пятый"},
		{6, "шесть шестого шестое шестой"}, {7, "семь седьмого седьмое седьмой"}, {8, "восемь восьмого восьмое восьмой"}, {9, "девять девятого девятое девятый"},
		{10, "десять десятого десятое десятый"}, {11, "одиннадцать одиннадцатого одиннадцатое одиннадцатый"}, {12, "двенадцать двенадцатого двенадцатое двенадцатый"},
		{13, "тринадцать тринадцатого тринадцатое тринадцатый"}, {14, "четырнадцать четырнадцатого четырнадцатое четырнадцатый"},
		{15, "пятнадцать пятнадцатого пятнадцатое пятнадцатый"}, {16, "шестнадцать шестнадцатого шестнадцатое шестнадцатый"},
		{17, "семнадцать семнадцатого семнадцатое семнадцатый"}, {18, "восемнадцать восемнадцатого восемнадцатое восемнадцатый"},
		{19, "девятнадцать девятнадцатого девятнадцатое девятнадцатый"}, {20, "двадцать двадцатого двадцатое двадцатый"},
		{30, "тридцать тридцатого тридцатое тридцатый"}, {40, "сорок сорокового сороковой"}, {50, "пятьдесят пятидесятого пятидесятый"},
		{60, "шестьдесят шестидесятого шестидесятый"}, {70, "семьдесят семидесятого семидесятый"}, {80, "восемьдесят восьмидесятого восьмидесятый"},
		{90, "девяносто девяностого девяностый"}, {100, "сто сотого"}, {200, "двести"}, {300, "триста"}, {400, "четыреста"},
		{500, "пятьсот"}, {600, "шестьсот"}, {700, "семьсот"}, {800, "восемьсот"}, {900, "девятьсот"},
		{1000, "тысяча тысячи тысячу тысяч"},
	}
	for _, g := range groups {
		for _, form := range strings.Fields(g.forms) {
			out[form] = g.n
		}
	}
	return out
}()

func numberToken(token string) (int, bool) {
	if n, err := strconv.Atoi(token); err == nil {
		return n, true
	}
	n, ok := russianNumbers[token]
	return n, ok
}
func spokenNumber(tokens []string) (int, bool) {
	if len(tokens) == 0 {
		return 0, false
	}
	sum, group := 0, 0
	for _, w := range tokens {
		n, ok := numberToken(w)
		if !ok {
			return 0, false
		}
		if n == 1000 {
			if group == 0 {
				group = 1
			}
			sum += group * 1000
			group = 0
		} else {
			group += n
		}
	}
	return sum + group, true
}

func spokenBirthDate(text string) string {
	words := wordsRE.FindAllString(normalized(text), -1)
	for i, w := range words {
		month := monthToNum(w)
		if month == 0 || !(strings.HasPrefix(w, "март") || strings.HasPrefix(w, "мая") || strings.HasPrefix(w, "мае") || strings.HasPrefix(w, "июн") || strings.HasPrefix(w, "июл") || strings.HasPrefix(w, "январ") || strings.HasPrefix(w, "феврал") || strings.HasPrefix(w, "апрел") || strings.HasPrefix(w, "август") || strings.HasPrefix(w, "сентябр") || strings.HasPrefix(w, "октябр") || strings.HasPrefix(w, "ноябр") || strings.HasPrefix(w, "декабр")) {
			continue
		}
		start := i
		for start > 0 && i-start < 3 {
			if _, ok := numberToken(words[start-1]); !ok {
				break
			}
			start--
		}
		end := i + 1
		for end < len(words) && end-i <= 7 {
			if _, ok := numberToken(words[end]); !ok {
				break
			}
			end++
		}
		day, ok := spokenNumber(words[start:i])
		if !ok {
			continue
		}
		year, ok := spokenNumber(words[i+1 : end])
		if !ok {
			continue
		}
		if result := validBirth(day, month, year); result != "" {
			return result
		}
	}
	return ""
}

// labelledBirthDate accepts explicitly named fields in any order, rather than
// guessing the order of a bare number sequence. All three fields are required.
func labelledBirthDate(text string) (string, bool) {
	tokens := wordsRE.FindAllString(normalized(text), -1)
	labels := map[string]string{"год": "year", "года": "year", "году": "year", "месяц": "month", "месяца": "month", "день": "day", "дня": "day", "число": "day"}
	isField := func(i int) bool {
		if labels[tokens[i]] == "" {
			return false
		}
		if i > 0 {
			if _, ok := numberToken(tokens[i-1]); ok {
				return true
			}
		}
		if i+1 < len(tokens) {
			if _, ok := numberToken(tokens[i+1]); ok {
				return true
			}
		}
		return false
	}
	fields := map[string]int{}
	first := -1
	for i, token := range tokens {
		if key := labels[token]; isField(i) {
			if first < 0 {
				first = i
			}
			fields[key] = 0
		}
	}
	if len(fields) != 3 {
		return "", false
	}
	suffix := false
	if first > 0 {
		_, suffix = numberToken(tokens[first-1])
	}
	seen := map[string]bool{}
	for i, token := range tokens {
		key := labels[token]
		if !isField(i) {
			continue
		}
		start, end := i+1, i+1
		if suffix {
			start, end = i, i
			for start > 0 && end-start < 7 {
				if _, ok := numberToken(tokens[start-1]); !ok {
					break
				}
				start--
			}
		} else {
			for end < len(tokens) && end-start < 7 {
				if _, ok := numberToken(tokens[end]); !ok {
					break
				}
				end++
			}
		}
		value, ok := spokenNumber(tokens[start:end])
		if !ok || (seen[key] && fields[key] != value) {
			return "", true
		}
		fields[key], seen[key] = value, true
	}
	return validBirth(fields["day"], fields["month"], fields["year"]), true
}
