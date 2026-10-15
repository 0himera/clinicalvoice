package safety

import (
	"regexp"
	"strings"

	"github.com/himera/tochka-voice/go-core/pkg/models"
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
		return "104"
	case models.FlagComplaint:
		return "105"
	case models.FlagOperatorReq:
		return "101"
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
	categoryARegex   *regexp.Regexp
	categoryBRegex   *regexp.Regexp
	operatorReqRegex *regexp.Regexp
	complaintRegex   *regexp.Regexp
}

func NewFastGate() *FastGate {
	// Категория А: Угроза жизни (Регламент 7.4, Сценарий С-14)
	catAPatterns := []string{
		`бол[ьиея].*в.*груд[ие]`,
		`дав[ия]т.*(за.*грудин|в.*груд|сердц)`,
		`жж[её]т.*в.*груд`,
		`отда[её]т.*в.*(лев|руку)`,
		`сердечн.*приступ`,
		`инфаркт`,
		`(не.*мож[ее]т|трудн|тяжел|нечем).*дышать`,
		`не.*могу.*дышать`,
		`не.*дышит`,
		`задыха`,
		`не.*дышу`,
		`не.*хватает.*воздух`,
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
	// Внимание: в Go regexp \b не поддерживает кириллицу, используем границы слов для кириллицы
	operatorPatterns := []string{
		`(^|[^а-яё])оператор`,
		`(^|[^а-яё])человек`,
		`(^|[^а-яё])сотрудник`,
		`(^|[^а-яё])регистратор`,
		`(^|[^а-яё])жив(ой|ого|ому|ым|ую)`,
		`(^|[^а-яё])перевед(и|ите)`,
		`(^|[^а-яё])девушк`,
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

	return &FastGate{
		categoryARegex:   regexp.MustCompile(`(?i)` + strings.Join(catAPatterns, "|")),
		categoryBRegex:   regexp.MustCompile(`(?i)` + strings.Join(catBPatterns, "|")),
		operatorReqRegex: regexp.MustCompile(`(?i)` + strings.Join(operatorPatterns, "|")),
		complaintRegex:   regexp.MustCompile(`(?i)` + strings.Join(complaintPatterns, "|")),
	}
}

// Check проверяет входящий текст реплики пациента
func (fg *FastGate) Check(text string) SafetyResult {
	norm := strings.TrimSpace(strings.ToLower(text))
	if norm == "" {
		return SafetyResult{Level: models.FlagNone}
	}

	// 1. Проверка Категории А (наивысший приоритет P1, прерывает диалог!)
	if match := fg.categoryARegex.FindString(norm); match != "" {
		return SafetyResult{
			Level:       models.FlagCategoryA,
			MatchedText: match,
			ActionCode:  "R-01",
			RouteTarget: "M0", // 103/112
			BotResponse: "Немедленно звоните сто три или сто двенадцать! Соединяю с дежурным сотрудником поликлиники.",
		}
	}

	// 2. Требование человека (R-02)
	if match := fg.operatorReqRegex.FindString(norm); match != "" {
		return SafetyResult{
			Level:       models.FlagOperatorReq,
			MatchedText: match,
			ActionCode:  "R-02",
			RouteTarget: "M2", // Регистратор (доб. 101-105)
			BotResponse: "Соединяю с сотрудником регистратуры, пожалуйста, оставайтесь на линии.",
		}
	}

	// 3. Категория Б (Острое состояние, R-11)
	if match := fg.categoryBRegex.FindString(norm); match != "" {
		return SafetyResult{
			Level:       models.FlagCategoryB,
			MatchedText: match,
			ActionCode:  "R-11",
			RouteTarget: "M4-Н", // Кабинет неотложной помощи / регистратор
			BotResponse: "Соединяю с регистратором, он организует срочную помощь. Если станет хуже — сразу звоните сто три.",
		}
	}

	// 4. Жалоба (R-03, С-15)
	if match := fg.complaintRegex.FindString(norm); match != "" {
		return SafetyResult{
			Level:       models.FlagComplaint,
			MatchedText: match,
			ActionCode:  "R-03",
			RouteTarget: "M3", // Старший администратор (доб. 5 / 105)
			BotResponse: "Мне жаль, что так вышло. Соединяю вас со старшим администратором.",
		}
	}

	return SafetyResult{Level: models.FlagNone}
}
