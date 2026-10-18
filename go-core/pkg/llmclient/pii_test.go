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
	}

	for _, tt := range tests {
		actual := MaskPII(tt.input)
		if strings.TrimSpace(actual) != strings.TrimSpace(tt.expected) {
			t.Errorf("MaskPII(%q) = %q; want %q", tt.input, actual, tt.expected)
		}
	}
}
