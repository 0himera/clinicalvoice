package safety

import (
	"context"
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

type mockThreatDecider struct {
	isThreat bool
	pTrue    float64
	err      error
}

func (m *mockThreatDecider) DecideVitalThreat(ctx context.Context, utterance string) (bool, float64, error) {
	return m.isThreat, m.pTrue, m.err
}

func TestFastGateWithThreatDecider(t *testing.T) {
	gate := NewFastGate()

	// 1. Metaphor filtered out (P < 0.15, isThreat = false)
	gate.SetThreatDecider(&mockThreatDecider{isThreat: false, pTrue: 0.02})
	res := gate.Check("У меня сердце кровью обливается, что не могу записаться к Богдановой")
	if res.Level != models.FlagNone {
		t.Fatalf("expected metaphor to be filtered, got level %v", res.Level)
	}

	// 2. Real emergency (isThreat = true)
	gate.SetThreatDecider(&mockThreatDecider{isThreat: true, pTrue: 0.98})
	res = gate.Check("У меня болит в груди и трудно дышать")
	if res.Level != models.FlagCategoryA {
		t.Fatalf("expected Category A for real threat, got level %v", res.Level)
	}

	// 3. Fail-closed on error (err != nil -> treated as emergency)
	gate.SetThreatDecider(&mockThreatDecider{isThreat: true, pTrue: 1.0, err: context.DeadlineExceeded})
	res = gate.Check("Кажется, инфаркт")
	if res.Level != models.FlagCategoryA {
		t.Fatalf("expected fail-closed Category A on error, got level %v", res.Level)
	}
}

func TestFastGateNegatedOperatorAndComplaint(t *testing.T) {
	gate := NewFastGate()

	negatedPhrases := []string{
		"Мне не нужен оператор, я сам разберусь",
		"Не переводите на человека",
		"Я без оператора запишусь",
		"Не надо оператора",
		"Я не буду жаловаться",
		"Это не жалоба, просто уточняю",
	}

	for _, p := range negatedPhrases {
		res := gate.Check(p)
		if res.Level == models.FlagOperatorReq || res.Level == models.FlagComplaint {
			t.Errorf("Expected phrase %q not to trigger operator or complaint, got level %v", p, res.Level)
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

