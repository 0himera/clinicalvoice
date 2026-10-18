package safety

import (
	"context"
	"regexp"
	"strings"

	"github.com/himera/tochka-voice/go-core/pkg/models"
)

type DecisionThreatDecider interface {
	DecideVitalThreat(ctx context.Context, utterance string) (isThreat bool, pTrue float64, err error)
}

const (
	ActionEmergency   = "R-01"
	ActionOperatorReq = "R-02"
	ActionComplaint   = "R-03"
	ActionAcute       = "R-11"

	ExtensionEmergency = "104"
	ExtensionOperator  = "101"
	ExtensionComplaint = "105"

	SpeechEmergency = "Немедленно звоните сто три или сто двенадцать! Соединяю с дежурным сотрудником поликлиники."
	SpeechOperator  = "Соединяю с сотрудником регистратуры, пожалуйста, оставайтесь на линии."
	SpeechAcute     = "Соединяю с регистратором, он организует срочную помощь. Если станет хуже — сразу звоните сто три."
	SpeechComplaint = "Мне жаль, что так вышло. Соединяю вас со старшим администратором."
)

type SafetyResult struct {
	Level       models.RedFlagLevel
	MatchedText string
	ActionCode  string // R-01, R-02, R-03, R-11
	BotResponse string // Реплика бота по регламенту
	RouteTarget string // M0 (103/112), M2 (регистратор), M3 (старший админ)
}

func (r SafetyResult) Extension() string {
	switch r.Level {
	case models.FlagCategoryA, models.FlagCategoryB:
		return ExtensionEmergency
	case models.FlagComplaint:
		return ExtensionComplaint
	case models.FlagOperatorReq:
		return ExtensionOperator
	}
	return ""
}
func (r SafetyResult) Priority() int {
	switch r.Level {
	case models.FlagCategoryA:
		return 4
	case models.FlagCategoryB:
		return 3
	case models.FlagOperatorReq:
		return 2
	case models.FlagComplaint:
		return 1
	}
	return 0
}

type FastGate struct {
	catAPatternRegexes     []*regexp.Regexp
	categoryARegex         *regexp.Regexp
	categoryBRegex         *regexp.Regexp
	operatorReqRegex       *regexp.Regexp
	operatorNegationRegex  *regexp.Regexp
	complaintRegex         *regexp.Regexp
	complaintNegationRegex *regexp.Regexp
	threatDecider          DecisionThreatDecider
}

func (fg *FastGate) SetThreatDecider(d DecisionThreatDecider) {
	fg.threatDecider = d
}

func NewFastGate() *FastGate {
	// Категория А: Угроза жизни (Регламент 7.4, Сценарий С-14)
	catAPatterns := []string{
		`(бол[ьиея].*в.*груд[ие]|в.*груд[ие].*бол[ьиея])`,
		`((дав[ия]т.*(за.*грудин|в.*груд|сердц))|((за.*грудин|в.*груд|сердц).*дав[ия]т))`,
		`((жж|печ)[её]т.*в.*груд|в.*груд.*(жж|печ)[её]т)`,
		`(отда[её]т.*в.*(лев|руку)|(лев|руку).*отда[её]т)`,
		`сердечн.*приступ`,
		`инфаркт`,
		`((не.*мож[ееё]т|трудн|тяжел|нечем).*дышать|дышать.*(не.*мож[ееё]т|трудн|тяжел|нечем))`,
		`(не.*могу.*дышать|дышать.*не.*могу)`,
		`не.*дышит`,
		`задыха`,
		`не.*дышу`,
		`(не.*хватает.*воздух|воздух.*не.*хватает)`,
		`бол.*сердц|сердц.*бол`,
		`тряс[её]т.*не.*останавлива`,
		`кровь.*изо.*рта`,
		`сильн.*ударил.*голов`,
		`ожог.*больш`,
		`отравил`,
		`(выпил|проглотил).*?(хими|таблет)`,
		`укусила.*змея`,
		`не.*может.*глотать`,
		`младен.*(вял|не.*просыпа)`,
		`грудн.*реб[её]н.*температур`,
		`реб[её]нок.*(посинел|проглотил)`,
		`беремен.*кровотечен`,
		`кровотечен.*беремен`,
		`отошли.*воды`,
		`сильн.*бол.*беремен`,
		`начались.*схватк`,
		`не.*хочу.*жить`,
		`угрожа.*убить`,
		`задыха[ею]т`,
		`син[ее]ет`,
		`хрип[ия]т`,
		`подавилс[яя]`,
		`потер[яя]л.*сознани`,
		`без.*сознани`,
		`не.*приход[ия]т.*в.*себя`,
		`не.*могу.*разбудить`,
		`упал.*в.*обморок`,
		`перекосил.*лиц`,
		`отнял[аи]сь.*(рука|нога)`,
		`не.*может.*говорить`,
		`речь.*невнятн`,
		`онемел.*половин.*тел`,
		`инсульт`,
		`судорог`,
		`припадок`,
		`сильн.*кровотечени`,
		`кровь.*не.*останавлива`,
		`рвот.*кров`,
		`дтп`,
		`удар.*головой`,
		`упал.*с.*высот`,
		`отека[ею]т.*(горл|лиц)`,
		`анафилактическ`,
		`покончить.*с.*собой`,
		`наглотал.*таблет`,
	}

	// Категория Б: Острое состояние (Регламент 7.4)
	catBPatterns := []string{
		`очень.*сильн.*бол`,
		`невыносим.*бол`,
		`бол.*усилива`,
		`температур.*сорок`,
		`температур.*не.*сбива`,
		`температур.*40`,
		`резко.*поднял.*давлен`,
		`не.*могу.*встать`,
		`остр.*бол`,
		`невыносим.*болит`,
		`стало.*намного.*хуже`,
		`температур.*третий.*день.*хуже`,
		`подвернул.*ног.*распух`,
		`порезал.*глубок`,
	}

	// Требование живого оператора (Регламент 7.3, R-02)
	// Для человек и девушк требуются глаголы обращения/соединения, чтобы «запишите мою девушку», «я пожилой человек» не срывали диалог
	operatorPatterns := []string{
		`(^|[^а-яё])оператор`,
		`(^|[^а-яё])сотрудник`,
		`(^|[^а-яё])регистратор`,
		`(^|[^а-яё])жив(ой|ого|ому|ым|ую)`,
		`(^|[^а-яё])перевед(и|ите)`,
		`(^|[^а-яё])((позов|соедин|перевед)[а-яё]*\s+(\S+\s+){0,2}человек|человек[а-яё]*\s+(\S+\s+){0,2}(позов|соедин|перевед)[а-яё]*)`,
		`(^|[^а-яё])((позов|соедин|перевед)[а-яё]*\s+(\S+\s+){0,2}девушк|девушк[а-яё]*\s+(\S+\s+){0,2}(позов|соедин|перевед)[а-яё]*)`,
	}

	// Жалоба / Конфликт (Регламент 7.3, R-03, С-15)
	complaintPatterns := []string{
		`(^|[^а-яё])жалоб`,
		`(^|[^а-яё])пожаловат`,
		`(^|[^а-яё])безобрази`,
		`(^|[^а-яё])нагрубил`,
		`(^|[^а-яё])хамств`,
		`(^|[^а-яё])в.*министерств`,
		`(^|[^а-яё])главврач`,
		`(^|[^а-яё])прокуратур`,
	}

	operatorNegPatterns := []string{
		`(^|[^а-яё])не\s+(нуж[ее]н|перевод|соедин|надо\s+оператор)`,
		`(^|[^а-яё])без\s+оператор`,
	}
	complaintNegPatterns := []string{
		`(^|[^а-яё])не\s+(буду\s+жаловат|жалоба)`,
		`(^|[^а-яё])без\s+жалоб`,
		`(^|[^а-яё])(жалоб\s+нет|нет\s+жалоб|жаловаться\s+не\s+буду)`,
	}

	catARegexes := make([]*regexp.Regexp, len(catAPatterns))
	for i, p := range catAPatterns {
		catARegexes[i] = regexp.MustCompile(`(?i)` + p)
	}

	return &FastGate{
		catAPatternRegexes:     catARegexes,
		categoryARegex:         regexp.MustCompile(`(?i)` + strings.Join(catAPatterns, "|")),
		categoryBRegex:         regexp.MustCompile(`(?i)` + strings.Join(catBPatterns, "|")),
		operatorReqRegex:       regexp.MustCompile(`(?i)` + strings.Join(operatorPatterns, "|")),
		operatorNegationRegex:  regexp.MustCompile(`(?i)` + strings.Join(operatorNegPatterns, "|")),
		complaintRegex:         regexp.MustCompile(`(?i)` + strings.Join(complaintPatterns, "|")),
		complaintNegationRegex: regexp.MustCompile(`(?i)` + strings.Join(complaintNegPatterns, "|")),
	}
}

// isNegated checks if the trigger word at byte position pos is negated in a narrow local window (1-2 words).
// It stops scanning when crossing punctuation or conjunctions 'а', 'но'.
func isNegated(text string, pos int) bool {
	if pos <= 0 || pos > len(text) {
		return false
	}
	runes := []rune(text[:pos])
	i := len(runes) - 1
	// Skip trailing whitespace
	for i >= 0 && (runes[i] == ' ' || runes[i] == '\t' || runes[i] == '\n' || runes[i] == '\r') {
		i--
	}
	wordCount := 0
	for i >= 0 && wordCount < 2 {
		r := runes[i]
		// Punctuation stops the narrow local window
		if r == ',' || r == '.' || r == '!' || r == '?' || r == ';' || r == ':' || r == '-' || r == '—' || r == '(' || r == ')' {
			break
		}
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			i--
			continue
		}
		// Read word backwards
		wordEnd := i + 1
		for i >= 0 && runes[i] != ' ' && runes[i] != '\t' && runes[i] != '\n' && runes[i] != '\r' &&
			runes[i] != ',' && runes[i] != '.' && runes[i] != '!' && runes[i] != '?' && runes[i] != ';' && runes[i] != ':' && runes[i] != '-' && runes[i] != '—' && runes[i] != '(' && runes[i] != ')' {
			i--
		}
		word := string(runes[i+1 : wordEnd])
		// Conjunctions "а", "но" stop the narrow window
		if word == "а" || word == "но" {
			break
		}
		if word == "не" || word == "нет" || word == "без" {
			return true
		}
		wordCount++
	}
	return false
}

// Check проверяет входящий текст реплики пациента
func (fg *FastGate) Check(text string) SafetyResult {
	return fg.CheckWithContext(context.Background(), text)
}

var clauseSplitRegex = regexp.MustCompile(`[,.;!?]+|\s+(?:а|но)\s+`)

func (fg *FastGate) matchUnsuppressedCatA(text string) (SafetyResult, bool) {
	symptomKeywords := []string{"бол", "дав", "жж", "печ", "отда"}
	for _, re := range fg.catAPatternRegexes {
		locs := re.FindAllStringIndex(text, -1)
		for _, loc := range locs {
			matchStr := text[loc[0]:loc[1]]
			negated := isNegated(text, loc[0])
			if !negated {
				for _, kw := range symptomKeywords {
					if idx := strings.Index(matchStr, kw); idx >= 0 {
						if isNegated(text, loc[0]+idx) {
							negated = true
							break
						}
					}
				}
			}
			if !negated {
				return SafetyResult{
					Level:       models.FlagCategoryA,
					MatchedText: matchStr,
					ActionCode:  ActionEmergency,
					RouteTarget: "M0", // 103/112
					BotResponse: SpeechEmergency,
				}, true
			}
		}
	}
	return SafetyResult{}, false
}

// CheckWithContext checks the utterance locally and instantaneously (< 1 ms).
// Normalizes 'ё' -> 'е' and guarantees fail-safe priority for unsuppressed Category A triggers.
func (fg *FastGate) CheckWithContext(ctx context.Context, text string) SafetyResult {
	norm := strings.TrimSpace(strings.ToLower(strings.ReplaceAll(text, "ё", "е")))
	if norm == "" {
		return SafetyResult{Level: models.FlagNone}
	}

	// 1. Проверка Категории А (наивысший приоритет P1, безусловный триггер перевода!)
	if fg.categoryARegex.MatchString(norm) {
		// First check the whole text
		if res, ok := fg.matchUnsuppressedCatA(norm); ok {
			return res
		}
		// If whole-text was suppressed by cross-boundary negation (e.g. "не болит живот, а болит в груди"), evaluate per clause
		clauses := clauseSplitRegex.Split(norm, -1)
		for _, clause := range clauses {
			clause = strings.TrimSpace(clause)
			if clause != "" && fg.categoryARegex.MatchString(clause) {
				if res, ok := fg.matchUnsuppressedCatA(clause); ok {
					return res
				}
			}
		}
	}

	// 2. Требование человека (R-02)
	if match := fg.operatorReqRegex.FindString(norm); match != "" {
		if fg.operatorNegationRegex == nil || !fg.operatorNegationRegex.MatchString(norm) {
			return SafetyResult{
				Level:       models.FlagOperatorReq,
				MatchedText: match,
				ActionCode:  ActionOperatorReq,
				RouteTarget: "M2", // Регистратор (доб. 101-105)
				BotResponse: SpeechOperator,
			}
		}
	}

	// 3. Категория Б (Острое состояние, R-11)
	if match := fg.categoryBRegex.FindString(norm); match != "" {
		return SafetyResult{
			Level:       models.FlagCategoryB,
			MatchedText: match,
			ActionCode:  ActionAcute,
			RouteTarget: "M4-Н", // Кабинет неотложной помощи / регистратор
			BotResponse: SpeechAcute,
		}
	}

	// 4. Жалоба (R-03, С-15)
	if match := fg.complaintRegex.FindString(norm); match != "" {
		if fg.complaintNegationRegex == nil || !fg.complaintNegationRegex.MatchString(norm) {
			return SafetyResult{
				Level:       models.FlagComplaint,
				MatchedText: match,
				ActionCode:  ActionComplaint,
				RouteTarget: "M3", // Старший администратор (доб. 5 / 105)
				BotResponse: SpeechComplaint,
			}
		}
	}

	return SafetyResult{Level: models.FlagNone}
}
