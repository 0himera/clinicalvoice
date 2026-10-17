package dialogue

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/slotmgr"
)

type Intent string

const (
	IntentBooking     Intent = "BOOKING"
	IntentReschedule  Intent = "RESCHEDULE"
	IntentCancel      Intent = "CANCEL"
	IntentCheckStatus Intent = "CHECK_STATUS"
	IntentSchedule    Intent = "SCHEDULE_INFO"
	IntentHomeVisit   Intent = "HOME_VISIT"
	IntentAffirm      Intent = "AFFIRM"
	IntentDeny        Intent = "DENY"
	IntentUnknown     Intent = "UNKNOWN"
	IntentDirectory   Intent = "DIRECTORY_INFO"
)

var wordsRE = regexp.MustCompile(`[\p{L}]+(?:-[\p{L}]+)*|\d+`)
var numericDateRE = regexp.MustCompile(`(?:^|[^\d])(\d{1,2})[. /](\d{1,2})[. /](\d{2}|\d{4})(?:$|[^\d])`)
var wordDateRE = regexp.MustCompile(`(\d{1,2})\s+(январ[\p{L}]*|феврал[\p{L}]*|март[\p{L}]*|апрел[\p{L}]*|ма[яе][\p{L}]*|июн[\p{L}]*|июл[\p{L}]*|август[\p{L}]*|сентябр[\p{L}]*|октябр[\p{L}]*|ноябр[\p{L}]*|декабр[\p{L}]*)\s+(\d{2}|\d{4})(?:$|[^\d])`)
var clockRE = regexp.MustCompile(`(?:^|[^\d])(\d{1,2})[:.](\d{2})(?:$|[^\d])`)

var confirmationWords = func() map[string]bool {
	allowed := map[string]bool{}
	for _, token := range strings.Fields(
		"да ага угу верно правильно подтверждаю подходит согласен согласна давайте давай хорошо окей приду буду смогу конечно точно определенно безусловно я это все спасибо пожалуйста на приеме вовремя у вас прийти подойти запись выбранную время врача отмену отмените отменяйте отменить отмени запишите записывайте записать перенесите переносите перенести и мне нам меня нас передайте передать заявку вызов вызовите кашель хрип температура температурой выезжайте ждем жду " +
			"к сожалению сожаление первый первое первая 1 второй второе вторая 2 вариант варианта варианте вариантом тот эта этот эту эти ну удобно удобен удобна удобнее можно устраивает слушаю так обязательно аппарат аппарата нет не неверно могу получится выйдет успею успеем никак",
	) {
		allowed[token] = true
	}
	return allowed
}()

func normalized(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.ReplaceAll(s, "ё", "е"))), " ")
}
func word(s, needle string) bool {
	for _, w := range wordsRE.FindAllString(normalized(s), -1) {
		if w == needle {
			return true
		}
	}
	return false
}

// Confirmation is a closed vocabulary: negation always wins, substrings never do.
func Confirmation(text string) Intent {
	s := normalized(text)
	if word(s, "нет") || word(s, "неверно") || containsAny(s, "не могу", "не смогу", "не получится", "не выйдет", "не успею", "никак не", "к сожалению не") || (hasWord(s, "не") && containsAny(s, "подходит", "надо", "нужно", "верно", "приду", "смогу", "могу", "буду", "получит", "выйдет", "успе", "удобн", "устраива", "соглас", "подтвержда", "отмен", "запис", "переноси", "мой", "тот", "эта", "это", "хочу")) {
		return IntentDeny
	}
	// Agreement inside a question, condition, or alternative is not consent.
	if informationQuestion(s) || containsAny(s, "повтор", "уточн", "не уверен", "не уверена", "не понял", "не поняла", "может быть", "второе время", "второй вариант", "другое время", "другой день", "другой вариант", "другого врача") || hasWord(s, "но", "если", "или", "наверное", "возможно", "только", "сначала", "позже") {
		return IntentUnknown
	}
	for _, token := range wordsRE.FindAllString(s, -1) {
		if !confirmationWords[token] {
			return IntentUnknown
		}
	}
	for _, w := range []string{"да", "ага", "угу", "верно", "правильно", "подтверждаю", "подходит", "согласен", "согласна", "давайте", "хорошо", "окей", "приду", "передайте", "передать", "конечно", "можно", "удобно", "удобен", "удобна", "удобнее", "устраивает", "буду", "смогу", "напомните", "напомнить", "слушаю"} {
		if word(s, w) {
			return IntentAffirm
		}
	}
	if containsAny(s, "смогу прийти", "смогу подойти", "буду на приеме", "буду вовремя", "буду у вас", "мне подходит", "нам подходит", "мне удобно", "нам удобно", "меня устраивает", "нас устраивает", "все устраивает", "так точно", "это я", "да это я", "у аппарата") {
		return IntentAffirm
	}
	return IntentUnknown
}

func ParseIntent(text string) Intent {
	s := normalized(text)
	tasks := requestedTasks(s)
	if len(tasks) > 1 {
		return IntentUnknown
	}
	switch {
	case containsAny(s, "когда я запис", "моя запись", "мои записи", "проверить запись", "проверьте запись", "на когда записан", "на когда записана") || (strings.Contains(s, "запис") && containsAny(s, "провер", "уточн", "узнать")):
		if len(tasks) == 1 {
			return tasks[0]
		}
		return IntentCheckStatus
	case len(tasks) == 1:
		return tasks[0]
	case containsAny(s, "какие врачи", "какие специалисты", "какие специальности", "список врач", "перечислите врач", "перечень врач", "какие профили"):
		return IntentDirectory
	case scheduleQuestion(s) || containsAny(s, "адрес", "анализ", "процедурн", "забор крови", "стоимост", "цена", "прикреп", "документ"):
		return IntentSchedule
	case containsAny(s, "талон", "к врачу", "попасть к"):
		return IntentBooking
	default:
		return Confirmation(s)
	}
}

// Count positive operation requests, not quoted/negated operation stems. The
// caller asks which task comes first rather than silently choosing a mutation.
func requestedTasks(text string) []Intent {
	s := normalized(text)
	if containsAny(s, "на дом", "домой", "надом", "надам") && (containsAny(s, "врач", "доктор", "терапевт", "педиатр", "вызов", "вызвать", "приехать") || word(s, "вызов") || word(s, "вызовите")) {
		return []Intent{IntentHomeVisit}
	}
	tokens := wordsRE.FindAllString(s, -1)
	found := map[Intent]bool{}
	var tasks []Intent
	for i, token := range tokens {
		if negatedOperation(tokens, i) {
			continue
		}
		var intent Intent
		switch {
		case containsPrefix(token, "перенес", "перенос"):
			intent = IntentReschedule
		case containsPrefix(token, "отмен", "вычеркн"):
			intent = IntentCancel
		case containsPrefix(token, "записат", "записыва", "запиш"):
			intent = IntentBooking
		}
		if intent != "" && !found[intent] {
			found[intent] = true
			tasks = append(tasks, intent)
		}
	}
	if len(tasks) == 0 {
		if containsAny(s, "другой день", "другое время", "попозже") {
			return []Intent{IntentReschedule}
		}
		if containsAny(s, "не приду", "не смогу") {
			return []Intent{IntentCancel}
		}
	}
	return tasks
}

func negatedOperation(tokens []string, index int) bool {
	// Prepositional: "не хочу отменять", "не надо переносить", "не буду записываться"
	for i := index - 1; i >= 0 && index-i <= 4; i-- {
		if tokens[i] == "не" {
			return true
		}
		if !hasWord(tokens[i], "надо", "нужно", "нужна", "хочу", "буду", "стоит", "следует", "собираюсь", "мне", "сейчас", "пожалуйста") {
			break
		}
	}
	// Postpositional: "отменять не буду", "переносить не надо", "записаться не хочу", "отменять не стану"
	for i := index + 1; i < len(tokens) && i-index <= 4; i++ {
		if tokens[i] == "не" {
			if i+1 < len(tokens) && hasWord(tokens[i+1], "буду", "будем", "хочу", "хотим", "надо", "нужно", "планирую", "собираюсь", "стану", "стоит") {
				return true
			}
			if i == len(tokens)-1 {
				return true
			}
		}
		if !hasWord(tokens[i], "я", "мы", "бы", "уже", "точно", "совсем") {
			if tokens[i] != "не" {
				break
			}
		}
	}
	return false
}

func containsPrefix(token string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(token, prefix) {
			return true
		}
	}
	return false
}

func needsSemanticReview(text string, intent Intent) bool {
	s := normalized(text)
	return isTaskIntent(intent) && (containsAny(s, "нужно ли", "можно ли", "возможно ли", "хочу узнать", "не знаю", "не уверен", "если ") || hasWord(s, "зачем", "почему"))
}

func ExtractSpecialty(text string) string {
	s := normalized(text)
	groups := []struct {
		keys []string
		spec string
	}{
		{[]string{"терапевт", "участков"}, "терапевт"}, {[]string{"педиатр"}, "педиатр"},
		{[]string{"стоматолог", "зубному"}, "стоматолог-терапевт"},
		{[]string{"отоларинголог", "оториноларинголог", "лору", "лор"}, "оториноларинголог"},
		{[]string{"офтальмолог", "окулист", "автальмолог", "акулист"}, "офтальмолог"}, {[]string{"гинеколог", "акушер"}, "акушер-гинеколог"},
		{[]string{"нейрохирург"}, "нейрохирург"}, {[]string{"хирург"}, "хирург"},
		{[]string{"дерматолог", "дерматовенеролог"}, "дерматовенеролог"}, {[]string{"травматолог", "ортопед"}, "травматолог-ортопед"},
		{[]string{"невролог"}, "невролог"}, {[]string{"кардиолог"}, "кардиолог"}, {[]string{"гастроэнтеролог"}, "гастроэнтеролог"},
		{[]string{"эндокринолог"}, "эндокринолог"}, {[]string{"уролог"}, "уролог"},
	}
	for _, g := range groups {
		for _, k := range g.keys {
			if strings.Contains(s, k) {
				return g.spec
			}
		}
	}
	return ""
}

func validBirth(d, m, y int) string {
	now := time.Now().In(models.ClinicLocation)
	if y < 100 {
		y += 2000
		if y > now.Year() {
			y -= 100
		}
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, models.ClinicLocation)
	if t.Year() != y || int(t.Month()) != m || t.Day() != d || y < now.Year()-120 || t.After(now) {
		return ""
	}
	return t.Format("02.01.2006")
}

func ExtractBirthDate(text string) string {
	if date, complete := labelledBirthDate(text); complete {
		return date
	}
	if m := numericDateRE.FindStringSubmatch(text); len(m) == 4 {
		d, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		y, _ := strconv.Atoi(m[3])
		return validBirth(d, mo, y)
	}
	if m := wordDateRE.FindStringSubmatch(strings.ToLower(text)); len(m) == 4 {
		d, _ := strconv.Atoi(m[1])
		y, _ := strconv.Atoi(m[3])
		return validBirth(d, monthToNum(m[2]), y)
	}
	return spokenBirthDate(text)
}

func ExtractFIO(text string) (last, first, middle string) {
	// Ordinary questions must never become a patient's surname and first name.
	if (informationQuestion(normalized(text)) && !isTaskIntent(ParseIntent(text))) || repeatRequest(normalized(text)) || purposeQuestion(normalized(text)) {
		return "", "", ""
	}
	s := strings.Join(strings.Fields(strings.ToLower(text)), " ")
	// Name preambles are removed as words, never as pieces of a person's name.
	for _, marker := range []string{"меня зовут ", "фамилия ", "пациент "} {
		if i := strings.Index(s, marker); i >= 0 {
			s = s[i+len(marker):]
			break
		}
	}
	if i := strings.Index(" "+s, " я "); i >= 0 {
		s = (" " + s)[i+3:]
	}
	stop := map[string]bool{}
	for _, w := range strings.Fields("здравствуйте добрый день дня число числа месяц месяца дата дату вечер утро меня зовут я фамилия имя отчество пациент запишите записаться записать хочу хотел хотела бы пожалуйста можно можете мне нужно еще также мою моего моей моему свою своего дочь дочку сына ребенка ребёнка маму папу жену мужа к врачу доктору терапевту терапевт педиатру хирургу лору офтальмологу окулисту неврологу кардиологу года году год рождения родился родилась запись прием на в и это спасибо") {
		stop[w] = true
	}
	for _, w := range wordsRE.FindAllString(s, -1) {
		if _, ok := numberToken(normalized(w)); ok {
			break
		}
		if _, err := strconv.Atoi(w); err == nil {
			break
		}
		if stop[w] || ExtractSpecialty(w) != "" {
			continue
		}
		if monthToNum(w) > 0 && containsAny(w, "январ", "феврал", "март", "апрел", "август", "сентябр", "октябр", "ноябр", "декабр") {
			break
		}
		if last == "" {
			last = capitalize(w)
		} else if first == "" {
			first = capitalize(w)
		} else {
			middle = capitalize(w)
			break
		}
	}
	return
}

func SlotPreference(text string) slotmgr.Query {
	s := normalized(text)
	q := slotmgr.Query{Limit: 2}
	if hour, _, _, ok := requestedClock(text); ok && !word(s, "не") {
		q.MinHour, q.MaxHour = hour, hour+1
	}
	if containsAny(s, "после обеда", "послеобед", "вечер") {
		q.MinHour = 13
	}
	if containsAny(s, "утро", "утром") {
		q.MaxHour = 12
	}
	for day, name := range map[time.Weekday]string{time.Monday: "понедельник", time.Tuesday: "вторник", time.Wednesday: "сред", time.Thursday: "четверг", time.Friday: "пятниц", time.Saturday: "суббот"} {
		if strings.Contains(s, name) {
			d := day
			if strings.Contains(s, "не в "+name) {
				q.ExcludedWeekday = &d
			} else {
				q.Weekday = &d
			}
			break
		}
	}
	if strings.Contains(s, "завтра") {
		n := time.Now().In(models.ClinicLocation).AddDate(0, 0, 1)
		q.After = time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, models.ClinicLocation)
		q.Before = q.After.AddDate(0, 0, 1)
	}
	return q
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
		time.August:    "августа",
		time.September: "сентября",
		time.October:   "октября",
		time.November:  "ноября",
		time.December:  "декабря",
	}

	return fmt.Sprintf("%s, %d %s в %s", days[t.Weekday()], t.Day(), months[t.Month()], t.Format("15:04"))
}
