package dialogue

import (
	"testing"
)

func TestConfirmationAffirmations(t *testing.T) {
	affirms := []string{
		"Конечно",
		"Да, можно",
		"Смогу",
		"Буду",
		"Мне удобно",
		"Меня устраивает",
		"Мне подходит",
		"Мне эта запись подходит",
		"Мне тот вариант подходит",
		"Ну мне подходит",
		"Да, подтверждаю",
		"Давайте",
		"Хорошо, спасибо",
		"Так точно",
		"У аппарата",
		"Это я",
		"Да, напомните, пожалуйста",
	}

	for _, text := range affirms {
		if got := Confirmation(text); got != IntentAffirm {
			t.Errorf("Confirmation(%q) = %v; want %v", text, got, IntentAffirm)
		}
	}
}

func TestConfirmationDenials(t *testing.T) {
	denies := []string{
		"Нет",
		"Не подходит",
		"Не надо",
		"Не нужно",
		"Не могу",
		"Не смогу",
		"Не получится",
		"К сожалению, не получится",
		"К сожалению, не смогу",
		"Никак не смогу",
		"Не приду",
		"Не буду",
		"Не хочу",
		"Неверно",
	}

	for _, text := range denies {
		if got := Confirmation(text); got != IntentDeny {
			t.Errorf("Confirmation(%q) = %v; want %v", text, got, IntentDeny)
		}
	}
}

func TestPostpositionalNegationInParseIntent(t *testing.T) {
	tests := []struct {
		input string
		want  Intent
	}{
		{"Отменять не буду", IntentDeny},
		{"Переносить не буду", IntentDeny},
		{"Записаться не хочу", IntentDeny},
		{"Отменять не надо", IntentDeny},
		{"Записываться не собираюсь", IntentDeny},
		{"Не отменяйте, а перенесите", IntentReschedule},
		{"Можно записаться к терапевту?", IntentBooking},
	}

	for _, tt := range tests {
		got := ParseIntent(tt.input)
		if got != tt.want {
			t.Errorf("ParseIntent(%q) = %v; want %v", tt.input, got, tt.want)
		}
	}
}

func TestConfirmationAlternativesAreUnknown(t *testing.T) {
	alternatives := []string{
		"Подходит первое время",
		"Мне подходит первое время",
		"Мне первый вариант",
		"Мне подходит первый вариант.",
		"Подходит второе время",
		"Мне подходит второе время",
		"Мне второй вариант",
		"Давайте в другой день",
		"Лучше другое время",
		"Можно",
		"Слушаю",
		"Удобно",
		"Устраивает",
	}
	for _, text := range alternatives {
		if got := Confirmation(text); got != IntentUnknown {
			t.Errorf("Confirmation(%q) = %v; want %v", text, got, IntentUnknown)
		}
	}
}

func TestPoliteConfirmationsAndAffirm(t *testing.T) {
	affirms := []string{
		"Записывайте",
		"Да, спасибо большое, записывайте",
		"Отлично, записывайте",
		"Да, отлично",
		"Согласен, записывайте",
	}

	for _, text := range affirms {
		if got := Confirmation(text); got != IntentAffirm {
			t.Errorf("Confirmation(%q) = %v; want %v", text, got, IntentAffirm)
		}
	}
}

func TestExtractFIOPatientka(t *testing.T) {
	last, first, middle := ExtractFIO("пациентка Иванова Анна Ивановна")
	if last != "Иванова" || first != "Анна" || middle != "Ивановна" {
		t.Errorf("ExtractFIO failed: got %s %s %s, want Иванова Анна Ивановна", last, first, middle)
	}

	last, first, _ = ExtractFIO("пациентка Смирнова Елена 15 мая 1990")
	if last != "Смирнова" || first != "Елена" {
		t.Errorf("ExtractFIO failed: got %s %s, want Смирнова Елена", last, first)
	}
}
