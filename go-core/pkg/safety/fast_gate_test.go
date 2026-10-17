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

