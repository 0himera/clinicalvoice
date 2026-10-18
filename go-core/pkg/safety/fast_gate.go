package safety

import (
	"context"
	"regexp"
	"strings"

	"github.com/himera/tochka-voice/go-core/pkg/models"
)

const (
	ActionEmergency   = "R-01"
	ActionOperatorReq = "R-02"
	ActionComplaint   = "R-03"
	ActionAcute       = "R-11"
	ActionDental      = "R-09"

	ExtensionEmergency = "104"
	ExtensionOperator  = "101"
	ExtensionComplaint = "105"
	ExtensionDental    = "102"

	SpeechEmergency = "Немедленно звоните сто три или сто двенадцать! Соединяю с дежурным сотрудником поликлиники."
	SpeechOperator  = "Соединяю с сотрудником регистратуры, пожалуйста, оставайтесь на линии."
	SpeechAcute     = "Соединяю с регистратором, он организует срочную помощь. Если станет хуже — сразу звоните сто три."
	SpeechComplaint = "Мне жаль, что так вышло. Соединяю вас со старшим администратором."
	SpeechDental    = "Соединяю со стоматологическим отделением для срочной помощи."
)

type SafetyResult struct {
	Level       models.RedFlagLevel
	MatchedText string
	ActionCode  string // R-01, R-02, R-03, R-09, R-11
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
	case models.FlagDental:
		return ExtensionDental
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
	case models.FlagComplaint, models.FlagDental:
		return 1
	}
	return 0
}

type gatePattern struct {
	expression *regexp.Regexp
	intrinsic  bool // The negative predicate itself denotes the emergency.
}

type FastGate struct {
	catAPatternRegexes     []gatePattern
	categoryBRegexes       []*regexp.Regexp
	operatorReqRegex       *regexp.Regexp
	operatorNegationRegex  *regexp.Regexp
	complaintRegex         *regexp.Regexp
	complaintNegationRegex *regexp.Regexp
}

func NewFastGate() *FastGate {
	// Категория А: Угроза жизни (Регламент 7.4, Сценарий С-14)
	catAPatterns := []struct {
		expression string
		intrinsic  bool
	}{
		{`(бол[ьиея].*груд[ьи]|груд[ьи].*бол[ьиея])`, false},
		{`((дав[ия]т.*(за.*грудин|в.*груд|сердц))|((за.*грудин|в.*груд|сердц).*дав[ия]т))`, false},
		{`((жж|печ)[её]т.*в.*груд|в.*груд.*(жж|печ)[её]т)`, false},
		{`(отда[её]т.*в.*(лев|руку)|(лев|руку).*отда[её]т)`, false},
		{`сердечн.*приступ`, false},
		{`инфаркт`, false},
		{`удушь`, false},
		{`((не\s+мож[ееё]т|трудн|тяжел|нечем).*дышать|дышать.*(не\s+мож[ееё]т|трудн|тяжел|нечем))`, true},
		{`(не\s+могу.*дышать|дышать.*не\s+могу)`, true},
		{`не\s+дышит`, true},
		{`задыха`, false},
		{`не\s+дышу`, true},
		{`(не\s+хватает.*воздух|воздух.*не\s+хватает)`, true},
		{`бол(?:ь|и|ит|ело|ят).*сердц|сердц.*бол(?:ь|и|ит|ело|ят)`, false},
		{`тряс[её]т.*не.*останавлива`, true},
		{`кровь.*изо.*рта`, false},
		{`сильн.*ударил.*голов`, false},
		{`ожог.*больш`, false},
		{`отравил`, false},
		{`(выпил|проглотил).*?(хими|таблет)`, false},
		{`укусила.*змея`, false},
		{`не\s+может.*глотать`, true},
		{`младен.*(вял|не.*просыпа)`, true},
		{`грудн.*реб[её]н.*температур`, false},
		{`реб[её]нок.*(посинел|проглотил)`, false},
		{`беремен.*кровотечен`, false},
		{`кровотечен.*беремен`, false},
		{`отошли.*воды`, false},
		{`сильн.*бол.*беремен`, false},
		{`начались.*схватк`, false},
		{`не\s+хочу.*жить`, true},
		{`(?:хочу|хочет|хотим|хотят|собираюсь|собирается)\s+(?:умереть|умирать|убить\s+себя)`, false},
		{`угрожа.*убить`, false},
		{`задыха[ею]т`, false},
		{`син[ее]ет`, false},
		{`хрип[ия]т`, false},
		{`подавилс[яя]`, false},
		{`потер[яя]л.*сознани`, false},
		{`без.*сознани`, true},
		{`не\s+приход[ия]т.*в.*себя`, true},
		{`не\s+могу.*разбудить`, true},
		{`упал.*в.*обморок`, false},
		{`перекосил.*лиц`, false},
		{`отнял[аи]сь.*(рука|нога)`, false},
		{`не\s+может.*говорить`, true},
		{`речь.*невнятн`, false},
		{`онемел.*половин.*тел`, false},
		{`инсульт`, false},
		{`судорог`, false},
		{`припадок`, false},
		{`сильн.*кровотечени`, false},
		{`кровь.*не.*останавлива`, true},
		{`рвот.*кров`, false},
		{`дтп`, false},
		{`удар.*головой`, false},
		{`упал.*с.*высот`, false},
		{`отека[ею]т.*(горл|лиц)`, false},
		{`анафилактическ`, false},
		{`покончить.*с.*собой`, false},
		{`наглотал.*таблет`, false},
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
		`^\s*(?:пожалуйста\s+)?(?:соедините|соедини)(?:\s+(?:меня|нас|пожалуйста)){0,2}\s*$`,
		`^\s*(человек|девушка)(\s+пожалуйста)?\s*$`,
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
		`оператор\p{L}*\s+не\s+(нужен|нужна|надо)`,
	}
	complaintNegPatterns := []string{
		`(^|[^а-яё])не\s+(буду\s+жаловат|жалоба)`,
		`(^|[^а-яё])без\s+жалоб`,
		`(^|[^а-яё])(жалоб\s+нет|нет\s+жалоб|жаловаться\s+не\s+буду)`,
	}

	catARegexes := make([]gatePattern, len(catAPatterns))
	for i, p := range catAPatterns {
		catARegexes[i] = gatePattern{regexp.MustCompile(`(?i)` + p.expression), p.intrinsic}
	}

	catBRegexes := make([]*regexp.Regexp, len(catBPatterns))
	for i, p := range catBPatterns {
		catBRegexes[i] = regexp.MustCompile(`(?i)` + p)
	}

	return &FastGate{
		catAPatternRegexes:     catARegexes,
		categoryBRegexes:       catBRegexes,
		operatorReqRegex:       regexp.MustCompile(`(?i)` + strings.Join(operatorPatterns, "|")),
		operatorNegationRegex:  regexp.MustCompile(`(?i)` + strings.Join(operatorNegPatterns, "|")),
		complaintRegex:         regexp.MustCompile(`(?i)` + strings.Join(complaintPatterns, "|")),
		complaintNegationRegex: regexp.MustCompile(`(?i)` + strings.Join(complaintNegPatterns, "|")),
	}
}

// Negation is attached to a symptom predicate, never to an arbitrary nearby
// word. In particular "не знаю, инфаркт" and "не могу терпеть боль в груди"
// must retain the emergency. Intrinsic negatives ("не могу дышать") are threats.
var symptomTerm = regexp.MustCompile(`бол(?:ь|и|ью|ей|ит|ят|ело)\p{L}*|дав[ия]т|жжет|печет|отдает|инфаркт\p{L}*|инсульт\p{L}*|судорог\p{L}*|кровотечен\p{L}*|задыха\p{L}*|припадок\p{L}*|беремен\p{L}*`)
var directNegation = regexp.MustCompile(`(?:^|\s)(?:не|нет|без)\s+$|(?:^|\s)(?:нет|без)\s+(?:никакой|никаких|сильной|острой|невыносимой)\s+$`)
var followingNegation = regexp.MustCompile(`^\p{L}*\s+(?:у\s+меня\s+)?(?:нет|не\s+было)(?:$|\s)`)
var pregnant = regexp.MustCompile(`беремен\p{L}*`)
var bleeding = regexp.MustCompile(`кровотечен\p{L}*`)
var metaphor = regexp.MustCompile(`сердце\s+(?:кровью\s+обливается|болит\s+за\s+(?:команду|клуб|родину))`)

func matchAffirmative(re *regexp.Regexp, clause string) string {
	for _, loc := range re.FindAllStringIndex(clause, -1) {
		match := clause[loc[0]:loc[1]]
		if (strings.HasPrefix(match, "остр") || strings.HasPrefix(match, "невыносим") || strings.HasPrefix(match, "сильн")) && directNegation.MatchString(clause[:loc[0]]) {
			continue
		}
		terms := symptomTerm.FindAllStringIndex(match, -1)
		if len(terms) == 0 {
			if directNegation.MatchString(clause[:loc[0]]) || followingNegation.MatchString(clause[loc[1]:]) {
				continue
			}
			return match
		}
		for _, term := range terms {
			start, end := loc[0]+term[0], loc[0]+term[1]
			if !directNegation.MatchString(clause[:start]) && !followingNegation.MatchString(clause[end:]) {
				return match
			}
		}
	}
	return ""
}

// Check проверяет входящий текст реплики пациента
func (fg *FastGate) Check(text string) SafetyResult {
	return fg.CheckWithContext(context.Background(), text)
}

var clauseSplitRegex = regexp.MustCompile(`[,.;!?]+|\s+(?:а|но|и|однако)\s+`)
var dentalPain = regexp.MustCompile(`(?:остр\p{L}*|сильн\p{L}*|невыносим\p{L}*)\s+(?:зубн\p{L}*\s+)?бол\p{L}*|травм\p{L}*`)
var dentalSubject = regexp.MustCompile(`зуб\p{L}*|десн\p{L}*|щек\p{L}*`)
var dentalSwelling = regexp.MustCompile(`отек\p{L}*|опух\p{L}*`)

// CheckWithContext checks the utterance locally; no model or network call is made.
// Normalizes 'ё' -> 'е' and guarantees fail-safe priority for unsuppressed Category A triggers.
func (fg *FastGate) CheckWithContext(ctx context.Context, text string) SafetyResult {
	norm := strings.TrimSpace(strings.ReplaceAll(strings.ToLower(text), "ё", "е"))
	if norm == "" {
		return SafetyResult{Level: models.FlagNone}
	}

	if matchAffirmative(pregnant, norm) != "" && matchAffirmative(bleeding, norm) != "" {
		return SafetyResult{Level: models.FlagCategoryA, ActionCode: ActionEmergency, RouteTarget: "M0", BotResponse: SpeechEmergency}
	}
	clauses := clauseSplitRegex.Split(norm, -1)
	for _, clause := range clauses {
		clause = metaphor.ReplaceAllString(clause, " ")
		for _, rule := range fg.catAPatternRegexes {
			match := rule.expression.FindString(clause)
			if !rule.intrinsic {
				match = matchAffirmative(rule.expression, clause)
			}
			if match != "" {
				return SafetyResult{Level: models.FlagCategoryA, MatchedText: match,
					ActionCode: ActionEmergency, RouteTarget: "M0", BotResponse: SpeechEmergency}
			}
		}
	}

	// 2. Требование человека (R-02)
	for _, clause := range clauses {
		if match := fg.operatorReqRegex.FindString(clause); match != "" {
			if fg.operatorNegationRegex == nil || !fg.operatorNegationRegex.MatchString(clause) {
				return SafetyResult{
					Level:       models.FlagOperatorReq,
					MatchedText: match,
					ActionCode:  ActionOperatorReq,
					RouteTarget: "M2", // Регистратор (доб. 101-105)
					BotResponse: SpeechOperator,
				}
			}
		}

	}

	// 3. Категория Б (Острое состояние, R-11)
	// R-09 is an organizational route from the regulation, not a diagnosis.
	// Category A and an explicit request for a human keep their priority.
	if dentalSubject.MatchString(norm) && (matchAffirmative(dentalPain, norm) != "" || matchAffirmative(dentalSwelling, norm) != "") {
		return SafetyResult{Level: models.FlagDental, ActionCode: ActionDental, RouteTarget: "M4-С", BotResponse: SpeechDental}
	}
	for _, clause := range clauses {
		for _, re := range fg.categoryBRegexes {
			if match := matchAffirmative(re, clause); match != "" {
				return SafetyResult{Level: models.FlagCategoryB, MatchedText: match,
					ActionCode: ActionAcute, RouteTarget: "M4-Н", BotResponse: SpeechAcute}
			}
		}
	}

	// 4. Жалоба (R-03, С-15)
	for _, clause := range clauses {
		if match := fg.complaintRegex.FindString(clause); match != "" {
			if fg.complaintNegationRegex == nil || !fg.complaintNegationRegex.MatchString(clause) {
				return SafetyResult{
					Level:       models.FlagComplaint,
					MatchedText: match,
					ActionCode:  ActionComplaint,
					RouteTarget: "M3", // Старший администратор (доб. 5 / 105)
					BotResponse: SpeechComplaint,
				}
			}
		}

	}

	return SafetyResult{Level: models.FlagNone}
}
