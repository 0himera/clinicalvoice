package safety

import (
	"testing"

	"github.com/himera/tochka-voice/go-core/pkg/models"
)

func TestFastGateCategoryA(t *testing.T) {
	gate := NewFastGate()

	testPhrases := []string{
		"У меня очень сильно давит за грудиной и болит в груди",
		"Человек без сознания, не могу разбудить!",
		"Кажется, у дедушки инсульт, перекосило лицо и отнялась рука",
		"Ребёнок задыхается и синеет!",
		"У него сердечный приступ, вызывайте скорую",
		"Сильное кровотечение после травмы, кровь не останавливается",
		"Я задыхаюсь",
		"Не хватает воздуха",
		"Ребёнок проглотил монету",
		"Беременная, кровотечение",
		"Отошли воды",
		"Не хочу жить",
		"Сильно ударился головой",
		"У грудного ребёнка температура",
		"Укусила змея",
		"Кровь изо рта",
	}

	for _, p := range testPhrases {
		res := gate.Check(p)
		if res.Level != models.FlagCategoryA {
			t.Errorf("Expected Category A for '%s', got %v", p, res.Level)
		}
		if res.RouteTarget != "M0" {
			t.Errorf("Expected Route M0, got %s", res.RouteTarget)
		}
	}
}

func TestFastGateOperatorRequest(t *testing.T) {
	gate := NewFastGate()

	testPhrases := []string{
		"Переведите на оператора пожалуйста",
		"Мне нужен живой человек",
		"Девушка, соедините с сотрудником",
		"Дайте мне регистратора",
	}

	for _, p := range testPhrases {
		res := gate.Check(p)
		if res.Level != models.FlagOperatorReq {
			t.Errorf("Expected OperatorReq for '%s', got %v", p, res.Level)
		}
	}
}

func TestFastGateCategoryB(t *testing.T) {
	gate := NewFastGate()

	testPhrases := []string{
		"У меня температура сорок и не сбивается",
		"Невыносимая боль в животе",
		"Резко поднялось давление, не могу встать",
	}

	for _, p := range testPhrases {
		res := gate.Check(p)
		if res.Level != models.FlagCategoryB {
			t.Errorf("Expected Category B for '%s', got %v", p, res.Level)
		}
	}
}

func TestFastGateNormalSpeech(t *testing.T) {
	gate := NewFastGate()

	normalPhrases := []string{
		"Здравствуйте, запишите меня к терапевту",
		"Иванов Иван Иванович, 12 марта 1980 года",
		"Какой график работы у окулиста?",
		"Я хочу отменить свою запись на пятницу",
	}

	for _, p := range normalPhrases {
		res := gate.Check(p)
		if res.Level != models.FlagNone {
			t.Errorf("Expected None for '%s', got %v", p, res.Level)
		}
	}
}

func TestFastGateWordOrderAndYo(t *testing.T) {
	gate := NewFastGate()

	phrases := []string{
		"У меня в груди болит",
		"В груди давит",
		"В груди жжет",
		"В груди жжёт",
		"В левую руку отдает",
		"Воздух не хватает",
		"Дышать не могу",
		"Дышать не может",
	}

	for _, p := range phrases {
		res := gate.Check(p)
		if res.Level != models.FlagCategoryA {
			t.Errorf("Expected Category A for word-order phrase %q, got level %v", p, res.Level)
		}
	}
}

func TestFastGateLocalNegationsAndFailSafe(t *testing.T) {
	gate := NewFastGate()

	// Pure local negations should NOT trigger Category A
	negatedPhrases := []string{
		"У меня не болит в груди",
		"В груди не болит",
		"Нет боли в груди",
		"В груди нет боли",
		"Без боли в груди",
		"В груди не давит",
		"В груди не жжет",
	}

	for _, p := range negatedPhrases {
		res := gate.Check(p)
		if res.Level == models.FlagCategoryA {
			t.Errorf("Expected phrase %q to be suppressed by local negation, but got Category A", p)
		}
	}

	// Fail-safe invariant: unsuppressed positive marker in any clause MUST trigger Category A
	positivePhrases := []string{
		"У меня не желудок болит, а сильно печет в груди",
		"У меня не болит в груди, а трудно дышать",
		"Не болит живот, а болит в груди",
	}

	for _, p := range positivePhrases {
		res := gate.Check(p)
		if res.Level != models.FlagCategoryA {
			t.Errorf("Expected Fail-Safe Category A for %q, got level %v", p, res.Level)
		}
	}
}

func TestFastGateOperatorFalsePositivesAndComplaintNegations(t *testing.T) {
	gate := NewFastGate()

	// These should NOT trigger operator or complaint
	safePhrases := []string{
		"запишите мою девушку",
		"я пожилой человек",
		"запишите девушку к врачу",
		"у меня жалоб нет",
		"нет жалоб",
		"жаловаться не буду",
	}

	for _, p := range safePhrases {
		res := gate.Check(p)
		if res.Level == models.FlagOperatorReq || res.Level == models.FlagComplaint {
			t.Errorf("Expected phrase %q not to trigger operator or complaint, got level %v", p, res.Level)
		}
	}

	// Legitimate operator requests with verbs should trigger
	operatorPhrases := []string{
		"Позовите человека",
		"Девушка, переведите на сотрудника",
		"Соедините с человеком",
	}

	for _, p := range operatorPhrases {
		res := gate.Check(p)
		if res.Level != models.FlagOperatorReq {
			t.Errorf("Expected OperatorReq for %q, got level %v", p, res.Level)
		}
	}
}

func TestNegationsCannotHideIndependentEmergencies(t *testing.T) {
	gate := NewFastGate()
	for _, phrase := range []string{
		"не знаю, инфаркт ли это", "не болит живот не могу дышать", "нет боли в груди не могу дышать", "не уверен что это инфаркт",
		"не могу терпеть боль в груди", "нет сил, задыхаюсь",
		"не хочу оператора, я не могу дышать", "нет, не могу дышать",
		"не болит живот и болит в груди", "инфаркта нет, но задыхаюсь",
		"не жалуюсь, в груди сильная боль", "У меня болит грудь",
	} {
		if got := gate.Check(phrase); got.Level != models.FlagCategoryA {
			t.Errorf("%q: %s", phrase, got.Level)
		}
	}
	for _, phrase := range []string{
		"инфаркта нет", "инсульта нет", "судорог нет", "не задыхаюсь",
		"не острая боль", "нет невыносимой боли", "нет сильной боли в груди",
		"сердце болит за команду", "сердце кровью обливается",
	} {
		if got := gate.Check(phrase); got.Level == models.FlagCategoryA || got.Level == models.FlagCategoryB {
			t.Errorf("%q: %s", phrase, got.Level)
		}
	}
}

func BenchmarkFastGate(b *testing.B) {
	gate := NewFastGate()
	for i := 0; i < b.N; i++ {
		gate.Check("Не болит живот, но в груди сильная боль и не хватает воздуха")
	}
}

func TestIndependentHumanRequestSurvivesNegatedClause(t *testing.T) {
	gate := NewFastGate()
	for _, phrase := range []string{"оператор не нужен, позовите человека", "не переводите на оператора, а соедините с регистратором", "человек пожалуйста"} {
		if got := gate.Check(phrase); got.Level != models.FlagOperatorReq {
			t.Errorf("human request lost: %q %+v", phrase, got)
		}
	}
}

func TestZer0daysEmergencyAndShortHumanRequests(t *testing.T) {
	gate := NewFastGate()
	for _, phrase := range []string{"Хочу умереть", "я хочу умереть", "она хочет умереть", "не нужен оператор, хочу умереть"} {
		if got := gate.Check(phrase); got.Level != models.FlagCategoryA || got.Extension() != "104" {
			t.Errorf("emergency missed for %q: %+v", phrase, got)
		}
	}
	for _, phrase := range []string{"Соедините", "Соедините, пожалуйста", "Пожалуйста соедините меня", "соедини меня"} {
		if got := gate.Check(phrase); got.Level != models.FlagOperatorReq || got.Extension() != "101" {
			t.Errorf("human request missed for %q: %+v", phrase, got)
		}
	}
	for _, phrase := range []string{"Не хочу умереть", "не соединяйте меня", "соедините эти документы"} {
		if got := gate.Check(phrase); got.Level != models.FlagNone {
			t.Errorf("negation/unrelated request %q: %+v", phrase, got)
		}
	}
}

func TestDentalRouteDoesNotOverrideEmergencyOrHuman(t *testing.T) {
	gate := NewFastGate()
	for _, phrase := range []string{"Сильно болит зуб, отёк", "Зуб сильно болит", "Острая зубная боль", "отекла щека", "Травма зуба"} {
		if got := gate.Check(phrase); got.ActionCode != ActionDental || got.Extension() != "102" {
			t.Errorf("dental route missed for %q: %+v", phrase, got)
		}
	}
	if got := gate.Check("Зуб болит, отёк, не могу дышать"); got.Level != models.FlagCategoryA {
		t.Errorf("dental condition hid emergency: %+v", got)
	}
	if got := gate.Check("Соедините с оператором, сильно болит зуб"); got.Level != models.FlagOperatorReq {
		t.Errorf("dental condition hid human request: %+v", got)
	}
	for _, phrase := range []string{"зуб не болит, отека нет", "болит зуб несильно", "не сильная боль в зубе"} {
		if got := gate.Check(phrase); got.Level != models.FlagNone {
			t.Errorf("non-acute/negated dental phrase %q: %+v", phrase, got)
		}
	}
}
