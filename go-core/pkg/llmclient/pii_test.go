package llmclient

import (
	"strings"
	"testing"
)

func TestMaskPII(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "Мой телефон +7 (999) 123-45-67, запишите меня",
			expected: "Мой телефон [ТЕЛЕФОН], запишите меня",
		},
		{
			input:    "Звоните на 89161234567 в любое время",
			expected: "Звоните на [ТЕЛЕФОН] в любое время",
		},
		{
			input:    "Паспорт серии 4510 123456 выдан в Москве",
			expected: "Паспорт серии [ПАСПОРТ] выдан в Москве",
		},
		{
			input:    "СНИЛС 123-456-789 01",
			expected: "СНИЛС [СНИЛС]",
		},
		{
			input:    "Полис 1234567890123456",
			expected: "Полис [ОМС]",
		},
		{
			input:    "Карта 4276-1234-5678-9012 для оплаты",
			expected: "Карта [КАРТА] для оплаты",
		},
		{
			input:    "Запишите меня к терапевту на завтра",
			expected: "Запишите меня к терапевту на завтра",
		},
		{
			input:    "Иванов Иван Иванович 12.03.1985",
			expected: "[ИМЯ] [ДАТА]",
		},
		{
			input:    "Дата рождения 05/11/1990",
			expected: "Дата рождения [ДАТА]",
		},
		{
			input:    "Родился 15-08-2002 года",
			expected: "Родился [ДАТА] года",
		},
		{
			input:    "Мой день рождения 1.2.95",
			expected: "Мой день рождения [ДАТА]",
		},
	}

	for _, tt := range tests {
		actual := MaskPII(tt.input)
		if strings.TrimSpace(actual) != strings.TrimSpace(tt.expected) {
			t.Errorf("MaskPII(%q) = %q; want %q", tt.input, actual, tt.expected)
		}
	}
}

func TestRemoteSpeechRejectsPIIInEveryState(t *testing.T) {
	for _, state := range []string{"STATE_INIT", "STATE_MAIN_MENU", "STATE_FINAL", "STATE_AWAIT_SPECIALTY"} {
		for _, text := range []string{
			"Иванов Иван Иванович, запишите к врачу", "иванов иван иванович",
			"Оганесян Арам Ашотович", "Ли Вэй", "Jean-Luc Picard",
			"Родился двенадцатого марта тысяча девятьсот восемьдесят пятого",
			"Дата рождения 12.03.1985", "Паспорт 4510 123456", "на улице Ленина дом пять",
			"8 999 123 45 67", "patient@example.com",
		} {
			if _, err := RemoteSpeech(text, state); err == nil {
				t.Errorf("private speech accepted: state=%s, %q", state, text)
			}
		}
	}
	if text, err := RemoteSpeech("К кому вообще можно у вас обратиться?", "STATE_MAIN_MENU"); err != nil || text != "к кому вообще можно у вас обратиться" {
		t.Fatalf("safe query rejected: %q %v", text, err)
	}
	if _, err := RemoteSpeech("Сориентируйте, к кому вообще можно обратиться?", "STATE_INIT"); err != nil {
		t.Fatalf("catalogue query rejected: %v", err)
	}
}
