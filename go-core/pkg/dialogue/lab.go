package dialogue

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/models"
)

type labTerm struct {
	key     string
	phrase  string
	minDays int
	maxDays int
}

// Ordered from specific to general: a matched key never falls through to blood generic.
var labTerms = []labTerm{
	{"histology", "гистологическое исследование", 10, 14},
	{"cytology", "цитологическое исследование", 7, 10},
	{"culture", "бактериологический посев", 5, 7},
	{"hormones", "гормональное исследование", 3, 5},
	{"pcr", "ПЦР-исследование", 2, 3},
	{"coag", "коагулограмма", 1, 2},
	{"glucose", "исследование глюкозы", 1, 2},
	{"hba1c", "гликированный гемоглобин", 1, 2},
	{"biochem", "биохимический анализ крови", 1, 2},
	{"cbc", "общий анализ крови", 1, 1},
	{"urine", "общий анализ мочи", 1, 1},
	{"blood", "анализ крови", 1, 2},
}

var (
	labNumericDateRE = regexp.MustCompile(`(?:^|[^\d])(\d{1,2})[. /](\d{1,2})[. /](\d{2}|\d{4})(?:$|[^\d])`)
	labShortDateRE   = regexp.MustCompile(`(?:^|[^\d])(\d{1,2})[. /](\d{1,2})(?:$|[^\d])`)
	labWordDateRE    = regexp.MustCompile(`(?i)(\d{1,2})\s+(январ[\p{L}]*|феврал[\p{L}]*|март[\p{L}]*|апрел[\p{L}]*|ма[яе][\p{L}]*|июн[\p{L}]*|июл[\p{L}]*|август[\p{L}]*|сентябр[\p{L}]*|октябр[\p{L}]*|ноябр[\p{L}]*|декабр[\p{L}]*)(?:\s+(\d{4}|\d{2}))?`)
	labDaysAgoRE     = regexp.MustCompile(`(\d{1,2})\s+(?:день|дня|дней)\s+назад`)
	labSubjectRE     = regexp.MustCompile(`(?i)(?:^|\s)(?:анализ\p{L}*|результат\p{L}*)\s+([\p{L}-]+)`)
	labPersonTestRE  = regexp.MustCompile(`(?i)(?:^|\s)([\p{L}-]+)\s+(?:сдавал|сдавала|сдал|сдала)\s+(?:анализ\p{L}*|кровь|мочу)`)
)

var labMonths = [...]string{"", "января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}

var labWeekdays = map[time.Weekday]string{
	time.Monday: "в понедельник", time.Tuesday: "во вторник", time.Wednesday: "в среду",
	time.Thursday: "в четверг", time.Friday: "в пятницу", time.Saturday: "в субботу", time.Sunday: "в воскресенье",
}

func personalLabStatus(s string) bool {
	if !containsAny(s, "анализ", "результат", "исследован") {
		return false
	}
	if containsAny(s, "готовы ли", "готов ли", "готова ли", "готовность моих", "мои анализ", "моих анализ", "мне анализ", "мои результат", "пришли ли", "пришли результат", "пришли анализ") {
		return true
	}
	if containsAny(s, "сдавал", "сдавала", "сдал", "сдала") && containsAny(s, "готов", "результат", "пришли") {
		return true
	}
	return hasWord(s, "мои", "мое", "мой", "моя", "моих") && containsAny(s, "готов", "результат")
}

func labTermFor(text string) (labTerm, bool) {
	s := normalized(text)
	for _, term := range labTerms {
		switch term.key {
		case "histology":
			if containsAny(s, "гистолог") {
				return term, true
			}
		case "cytology":
			if containsAny(s, "цитолог") {
				return term, true
			}
		case "culture":
			if containsAny(s, "посев", "бактериолог") {
				return term, true
			}
		case "hormones":
			if containsAny(s, "гормон", "щитовид") {
				return term, true
			}
		case "pcr":
			if containsAny(s, "пцр") {
				return term, true
			}
		case "coag":
			if containsAny(s, "коагул") {
				return term, true
			}
		case "glucose":
			if containsAny(s, "глюкоз") {
				return term, true
			}
		case "hba1c":
			if containsAny(s, "гликирован") {
				return term, true
			}
		case "biochem":
			if containsAny(s, "биохим", "холестерин") {
				return term, true
			}
		case "cbc":
			if containsAny(s, "общий анализ крови", "оак") {
				return term, true
			}
		case "urine":
			if containsAny(s, "общий анализ мочи", "анализ мочи", "моча", "мочи", "мочу") {
				return term, true
			}
		case "blood":
			if containsAny(s, "кровь", "крови") {
				return term, true
			}
		}
	}
	return labTerm{}, false
}

func labDetail(text string) bool {
	if _, ok := labTermFor(text); ok {
		return true
	}
	if _, ok := extractLabCollectedDate(text, time.Now().In(models.ClinicLocation)); ok {
		return true
	}
	return containsAny(normalized(text), "не знаю", "не помню")
}

func labTermByKey(key string) (labTerm, bool) {
	for _, term := range labTerms {
		if term.key == key {
			return term, true
		}
	}
	return labTerm{}, false
}

func thirdPartyLabRequest(text string) bool {
	s := normalized(text)
	if familyRequest(s) || hasWord(s, "она", "он", "нее", "него", "чужой", "чужую", "чужое") {
		return true
	}
	for _, match := range labSubjectRE.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 {
			continue
		}
		subject := normalized(match[1])
		if !hasWord(subject, "мои", "мой", "моя", "мое", "моих", "моей", "моего", "моему", "мою", "крови", "мочи", "мочу", "готов", "готовы", "готова", "пришли", "пришел", "пришла", "анализа", "анализов", "анализ", "исследования", "исследований", "сегодня", "вчера", "позавчера", "хорошие", "плохие", "нормальные", "норма", "повышены", "понижены", "и", "или") {
			return true
		}
	}
	for _, match := range labPersonTestRE.FindAllStringSubmatch(text, -1) {
		if len(match) > 1 && !hasWord(normalized(match[1]), "я", "мы", "мои", "мой", "моя") {
			return true
		}
	}
	return false
}

func extractLabCollectedDate(text string, now time.Time) (time.Time, bool) {
	now = now.In(models.ClinicLocation)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, models.ClinicLocation)
	s := normalized(text)
	switch {
	case strings.Contains(s, "позавчера"):
		return today.AddDate(0, 0, -2), true
	case strings.Contains(s, "вчера"):
		return today.AddDate(0, 0, -1), true
	case strings.Contains(s, "сегодня"):
		return today, true
	}
	if match := labDaysAgoRE.FindStringSubmatch(s); len(match) == 2 {
		if days, err := strconv.Atoi(match[1]); err == nil && days >= 0 && days <= 30 {
			return today.AddDate(0, 0, -days), true
		}
	}
	if match := labNumericDateRE.FindStringSubmatch(s); len(match) == 4 {
		day, _ := strconv.Atoi(match[1])
		month, _ := strconv.Atoi(match[2])
		year, _ := strconv.Atoi(match[3])
		if year < 100 {
			year += 2000
		}
		return validLabDate(day, month, year, today)
	}
	if match := labShortDateRE.FindStringSubmatch(s); len(match) == 3 {
		day, _ := strconv.Atoi(match[1])
		month, _ := strconv.Atoi(match[2])
		return validLabDate(day, month, today.Year(), today)
	}
	if match := labWordDateRE.FindStringSubmatch(strings.ToLower(text)); len(match) >= 3 {
		day, _ := strconv.Atoi(match[1])
		month := monthToNum(match[2])
		year := today.Year()
		explicitYear := len(match) == 4 && match[3] != ""
		if explicitYear {
			year, _ = strconv.Atoi(match[3])
			if year < 100 {
				year += 2000
			}
		}
		if parsed, ok := validLabDate(day, month, year, today); ok {
			return parsed, true
		}
		if !explicitYear {
			return validLabDate(day, month, year-1, today)
		}
	}
	return time.Time{}, false
}

func validLabDate(day, month, year int, today time.Time) (time.Time, bool) {
	parsed := time.Date(year, time.Month(month), day, 0, 0, 0, 0, models.ClinicLocation)
	if parsed.Year() != year || int(parsed.Month()) != month || parsed.Day() != day || parsed.After(today) {
		return time.Time{}, false
	}
	return parsed, true
}

func (f *FSMController) addWorkingDays(start time.Time, days int) time.Time {
	result := start
	for days > 0 {
		result = result.AddDate(0, 0, 1)
		if result.Weekday() == time.Saturday || result.Weekday() == time.Sunday {
			continue
		}
		if f.holidays != nil && f.holidays[result.Format("2006-01-02")] {
			continue
		}
		days--
	}
	return result
}

func formatLabDate(t, now time.Time) string {
	date := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, models.ClinicLocation)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, models.ClinicLocation)
	switch {
	case date.Equal(today):
		return "сегодня"
	case date.Equal(today.AddDate(0, 0, -1)):
		return "вчера"
	case date.Equal(today.AddDate(0, 0, -2)):
		return "позавчера"
	case date.Year() != today.Year():
		return fmt.Sprintf("%d %s %d года", date.Day(), labMonths[date.Month()], date.Year())
	default:
		return fmt.Sprintf("%d %s", date.Day(), labMonths[date.Month()])
	}
}

func formatLabExpectedDate(t time.Time) string {
	return fmt.Sprintf("%s, %d %s", labWeekdays[t.Weekday()], t.Day(), labMonths[t.Month()])
}

func (f *FSMController) labStatus(ctx context.Context, s *models.CallSession, text string) StepResult {
	norm := normalized(text)
	if thirdPartyLabRequest(text) || (s.ThirdParty && !s.Guardian) {
		s.ThirdParty = true
		return f.transfer(ctx, s, "101", "R-07: сведения о другом пациенте, полномочия не проверены", "Сведения о чужих анализах сообщает сотрудник после проверки полномочий. Соединяю с регистратором.")
	}
	if isMedicalQuestion(norm) {
		return f.primaryCareOffer(ctx, s, text, false)
	}
	if s.CurrentState == StateLabStatus && containsAny(norm, "не знаю", "не помню", "не помню дату", "не помню когда") {
		return f.transfer(ctx, s, "101", "R-08: пациент не назвал исследование или дату сдачи", "Точный статус анализов проверит регистратор. Соединяю с сотрудником.")
	}
	if term, ok := labTermFor(text); ok {
		s.LabTest = term.key
	}
	now := time.Now().In(models.ClinicLocation)
	if collected, ok := extractLabCollectedDate(text, now); ok {
		s.LabCollectedAt = collected.Format("02.01.2006")
	}
	if s.LabTest == "" {
		return say(StateLabStatus, "Точный статус я не вижу. Общий анализ крови и мочи готовятся один рабочий день. Какой анализ и когда вы сдавали? Значения не сообщаю.")
	}
	term, _ := labTermByKey(s.LabTest)
	if s.LabCollectedAt == "" {
		return say(StateLabStatus, fmt.Sprintf("Когда вы сдавали %s? Назовите дату или скажите «сегодня» или «вчера».", term.phrase))
	}
	collected, err := time.ParseInLocation("02.01.2006", s.LabCollectedAt, models.ClinicLocation)
	if err != nil {
		s.LabCollectedAt = ""
		return say(StateLabStatus, "Назовите дату сдачи анализа, например 20 октября.")
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, models.ClinicLocation)
	if collected.After(today) {
		s.LabCollectedAt = ""
		return say(StateLabStatus, "Дата сдачи не может быть в будущем. Назовите дату ещё раз.")
	}
	if today.Sub(collected) > 30*24*time.Hour {
		return f.transfer(ctx, s, "101", "R-08: заказ старше 30 дней", "Точный статус анализов старше тридцати дней проверит регистратор. Соединяю с сотрудником.")
	}
	expectedMin := f.addWorkingDays(collected, term.minDays)
	expectedMax := f.addWorkingDays(collected, term.maxDays)
	var answer string
	switch {
	case !today.Before(expectedMax):
		answer = fmt.Sprintf("По нормативу %s, сданный %s, уже должен быть готов.", term.phrase, formatLabDate(collected, now))
	case !today.Before(expectedMin):
		answer = fmt.Sprintf("По нормативу %s, сданный %s, может быть готов уже сегодня. Окончательный срок — %s.", term.phrase, formatLabDate(collected, now), formatLabExpectedDate(expectedMax))
	default:
		answer = fmt.Sprintf("По нормативу %s, сданный %s, будет готов не позднее %s.", term.phrase, formatLabDate(collected, now), formatLabExpectedDate(expectedMax))
	}
	answer += " Значения результатов я не вижу и не оцениваю. Получить результат можно через Госуслуги, у лечащего врача или в окне номер три по будням с двенадцати до девятнадцати."
	s.LabTest, s.LabCollectedAt, s.FailCount = "", "", 0
	return say(StateFinal, answer)
}
