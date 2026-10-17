package dialogue

import (
	"strconv"
	"strings"
)

// requestedClock recognizes a stated appointment time, not an ordinal or a date.
// An hour without minutes is deliberately kept imprecise for disambiguation.
func requestedClock(text string) (hour, minute int, precise, ok bool) {
	s := normalized(text)
	if numericDateRE.MatchString(s) {
		return
	}
	if matches := clockRE.FindAllStringSubmatch(s, -1); len(matches) > 0 {
		if len(matches) != 1 {
			return
		}
		hour, _ = strconv.Atoi(matches[0][1])
		minute, _ = strconv.Atoi(matches[0][2])
		return hour, minute, true, hour < 24 && minute < 60
	}
	tokens := wordsRE.FindAllString(s, -1)
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		start := i
		if token == "в" || token == "на" {
			start++
		} else if i+1 >= len(tokens) || !strings.HasPrefix(tokens[i+1], "час") {
			continue
		}
		if start >= len(tokens) {
			continue
		}
		h, valid := numberToken(tokens[start])
		if !valid || h < 0 || h > 23 {
			continue
		}
		end := start + 1
		if h == 20 && end < len(tokens) {
			if unit, valid := numberToken(tokens[end]); valid && unit >= 1 && unit <= 3 {
				h += unit
				end++
			}
		}
		if end < len(tokens) && strings.HasPrefix(tokens[end], "час") {
			end++
		}
		m, exact := 0, false
		if end < len(tokens) {
			if value, valid := numberToken(tokens[end]); valid && value >= 0 && value < 60 {
				m, exact = value, true
				end++
				if value >= 20 && value%10 == 0 && end < len(tokens) {
					if unit, valid := numberToken(tokens[end]); valid && unit > 0 && unit < 10 {
						m += unit
						end++
					}
				}
			}
		}
		if end < len(tokens) && strings.HasPrefix(tokens[end], "минут") {
			end++
		}
		if end < len(tokens) {
			if tokens[end] == "дня" || tokens[end] == "вечера" {
				if h < 12 {
					h += 12
				}
			} else if tokens[end] == "утра" && h == 12 {
				h = 0
			}
			if monthToNum(tokens[end]) != 0 || strings.HasPrefix(tokens[end], "числ") {
				continue
			}
		}
		if ok {
			return 0, 0, false, false
		} // Multiple stated times need a clarification.
		hour, minute, precise, ok = h, m, exact, true
		i = end - 1
	}
	return
}

var slotOrdinals = map[string]int{
	"первый": 0, "первое": 0, "первая": 0, "первую": 0, "первого": 0, "первом": 0, "первому": 0, "первым": 0,
	"второй": 1, "второе": 1, "вторая": 1, "вторую": 1, "второго": 1, "втором": 1, "второму": 1, "вторым": 1,
	"третий": 2, "третье": 2, "третья": 2, "третью": 2, "третьего": 2, "третьем": 2, "третьему": 2, "третьим": 2,
}
