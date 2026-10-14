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
