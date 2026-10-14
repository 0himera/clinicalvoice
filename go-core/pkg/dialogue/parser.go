package dialogue

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Intent string

const (
	IntentBooking     Intent = "BOOKING"
	IntentReschedule  Intent = "RESCHEDULE"
	IntentCancel      Intent = "CANCEL"
	IntentCheckStatus Intent = "CHECK_STATUS"
	IntentSchedule    Intent = "SCHEDULE_INFO"
	IntentAffirm      Intent = "AFFIRM" // Да, подтверждаю, верно
	IntentDeny        Intent = "DENY"   // Нет, не подходит
	IntentUnknown     Intent = "UNKNOWN"
)

type ExtractedSlots struct {
	Intent     Intent
	Specialty  string
	LastName   string
	FirstName  string
	MiddleName string
	BirthDate  string // ДД.ММ.ГГГГ
	TargetDate string // дата слота если названа
	TargetTime string // время слота если названо
}

var (
	dateRegex      = regexp.MustCompile(`(\d{1,2})[\.\s/](\d{1,2})[\.\s/](\d{2,4})`)
	timeRegex      = regexp.MustCompile(`(\d{1,2})[:\.\s](\d{2})`)
	birthYearRegex = regexp.MustCompile(`(\d{1,2})\s+(январ\w*|феврал\w*|март\w*|апрел\w*|ма[яе]\w*|июн\w*|июл\w*|август\w*|сентябр\w*|октябр\w*|ноябр\w*|декабр\w*)\s+(\d{2,4})`)
)

// ParseIntent классифицирует базовый интент по фразе
func ParseIntent(text string) Intent {
	norm := strings.ToLower(text)
	switch {
	case containsAny(norm, "перенес", "другой день", "другое время", "попозже"):
		return IntentReschedule
	case containsAny(norm, "отмен", "не приду", "не смогу", "уже не нужно"):
		return IntentCancel
	case containsAny(norm, "когда я записан", "на когда", "во сколько мне", "проверьте запись", "моя запись"):
		return IntentCheckStatus
	case containsAny(norm, "график", "режим работы", "как работаете", "до скольки", "со скольк", "со скольки", "в субботу", "адрес", "кров", "процедурн", "анализ", "прививк", "укол"):
		return IntentSchedule
	case containsAny(norm, "запис", "талон", "к врачу", "приём", "попасть к"):
		return IntentBooking
	case containsAny(norm, "да", "верно", "подтверждаю", "давайте", "подходит", "согласен", "хорошо"):
		return IntentAffirm
	case containsAny(norm, "нет", "не надо", "не то", "не подходит", "другое"):
		return IntentDeny
	default:
		return IntentUnknown
	}
}

// ExtractSpecialty извлекает специальность врача из реплики
func ExtractSpecialty(text string) string {
	norm := strings.ToLower(text)
	specs := []struct {
		keys []string
		spec string
	}{
		{[]string{"терапевт", "участков"}, "терапевт"},
		{[]string{"педиатр", "детск"}, "педиатр"},
		{[]string{"лор", "ухо", "горло", "нос", "оториноларинголог"}, "оториноларинголог"},
		{[]string{"окулист", "офтальмолог", "глаз"}, "офтальмолог"},
		{[]string{"хирург"}, "хирург"},
		{[]string{"гинеколог", "акушер", "женск"}, "акушер-гинеколог"},
		{[]string{"стоматолог", "зубн"}, "стоматолог"},
		{[]string{"дерматолог", "дерматовенеролог", "кож"}, "дерматовенеролог"},
		{[]string{"травматолог", "ортопед"}, "травматолог-ортопед"},
		{[]string{"невролог", "нерв"}, "невролог"},
		{[]string{"кардиолог", "сердц"}, "кардиолог"},
		{[]string{"гастроэнтеролог", "желудок"}, "гастроэнтеролог"},
		{[]string{"уролог"}, "уролог"},
		{[]string{"эндокринолог", "щитовидк"}, "эндокринолог"},
	}

	for _, s := range specs {
		for _, k := range s.keys {
			if strings.Contains(norm, k) {
				return s.spec
			}
		}
	}
	return ""
}

// ExtractBirthDate извлекает дату рождения в формате ДД.ММ.ГГГГ
func ExtractBirthDate(text string) string {
	// Формат "12.03.1985" или "12 03 1985"
	if m := dateRegex.FindStringSubmatch(text); len(m) == 4 {
		d, _ := strconv.Atoi(m[1])
		mon, _ := strconv.Atoi(m[2])
		y, _ := strconv.Atoi(m[3])
		if y < 100 {
			if y > 25 {
				y += 1900
			} else {
				y += 2000
			}
		}
		if d >= 1 && d <= 31 && mon >= 1 && mon <= 12 && y >= 1910 && y <= 2026 {
			return fmt.Sprintf("%02d.%02d.%04d", d, mon, y)
		}
	}

	// Словесный формат "12 марта 1985"
	if m := birthYearRegex.FindStringSubmatch(strings.ToLower(text)); len(m) == 4 {
		d, _ := strconv.Atoi(m[1])
		mon := monthToNum(m[2])
		y, _ := strconv.Atoi(m[3])
		if y < 100 {
			if y > 25 {
				y += 1900
			} else {
				y += 2000
			}
		}
		if mon > 0 && d >= 1 && d <= 31 {
			return fmt.Sprintf("%02d.%02d.%04d", d, mon, y)
		}
	}

	return ""
}

// ExtractFIO пытается извлечь фамилию, имя, отчество из реплики
func ExtractFIO(text string) (last, first, middle string) {
	// Очищаем от мусорных слов
	cleaned := strings.ToLower(text)
	stopWords := []string{
		"меня", "зовут", "я", "фамилия", "имя", "отчество", "пациент", "запишите", "пожалуйста",
		"к врачу", "к доктору", "к терапевту", "терапевту", "терапевт", "педиатру", "педиатр",
		"хирургу", "хирург", "лору", "лор", "офтальмологу", "окулисту", "неврологу", "невролог",
		"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря",
		"года", "году", "год", "рождения", "прием", "приём", "талон", "запись",
	}
	for _, sw := range stopWords {
		cleaned = strings.ReplaceAll(cleaned, sw, " ")
	}

	words := strings.Fields(cleaned)
	var names []string
	for _, w := range words {
		// Фильтруем цифры и даты
		if _, err := strconv.Atoi(w); err == nil {
			continue
		}
		if len(w) > 2 && !strings.ContainsAny(w, "0123456789.,/-") {
			names = append(names, capitalize(w))
		}
	}

	if len(names) >= 1 {
		last = names[0]
	}
	if len(names) >= 2 {
		first = names[1]
	}
	if len(names) >= 3 {
		middle = names[2]
	}
	return
}

func capitalize(s string) string {
	if s == "" {
		return ""
	}
	runes := []rune(s)
	runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
	return string(runes)
}

func monthToNum(monthStr string) int {
	m := strings.ToLower(monthStr)
	switch {
	case strings.HasPrefix(m, "янв"):
		return 1
	case strings.HasPrefix(m, "фев"):
		return 2
	case strings.HasPrefix(m, "мар"):
		return 3
	case strings.HasPrefix(m, "апр"):
		return 4
	case strings.HasPrefix(m, "ма"):
		return 5
	case strings.HasPrefix(m, "июн"):
		return 6
	case strings.HasPrefix(m, "июл"):
		return 7
	case strings.HasPrefix(m, "авг"):
		return 8
	case strings.HasPrefix(m, "сен"):
		return 9
	case strings.HasPrefix(m, "окт"):
		return 10
	case strings.HasPrefix(m, "ноя"):
		return 11
	case strings.HasPrefix(m, "дек"):
		return 12
	default:
		return 0
	}
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// FormatDateRussian форматирует время для озвучивания ассистентом (по регламенту: с днём недели словами)
func FormatDateRussian(t time.Time) string {
	days := map[time.Weekday]string{
		time.Monday:    "в понедельник",
		time.Tuesday:   "во вторник",
		time.Wednesday: "в среду",
		time.Thursday:  "в четверг",
		time.Friday:    "в пятницу",
		time.Saturday:  "в субботу",
		time.Sunday:    "в воскресенье",
	}

	months := map[time.Month]string{
		time.January:   "января",
		time.February:  "февраля",
		time.March:     "марта",
		time.April:     "апреля",
		time.May:       "мая",
		time.June:      "июня",
		time.July:      "июля",
		time.August:     "августа",
		time.September:  "сентября",
		time.October:    "октября",
		time.November:   "ноября",
		time.December:   "декабря",
	}

	return fmt.Sprintf("%s, %d %s в %s", days[t.Weekday()], t.Day(), months[t.Month()], t.Format("15:04"))
}
