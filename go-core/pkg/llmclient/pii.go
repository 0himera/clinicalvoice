package llmclient

import (
	"regexp"
)

var (
	// Phone numbers: +7..., 8..., 7... with standard formatting or loose digits
	rePhone = regexp.MustCompile(`(?:\+7|8|7)[\s\-\(]*[0-9]{3}[\s\-\)]*[0-9]{3}[\s\-]*[0-9]{2}[\s\-]*[0-9]{2}`)
	// Passport: 4 digits series and 6 digits number
	rePassport = regexp.MustCompile(`\b\d{4}\s+\d{6}\b`)
	// SNILS: 11 digits (3-3-3 2 or 3-3-3-2)
	reSNILS = regexp.MustCompile(`\b\d{3}[-\s]\d{3}[-\s]\d{3}[-\s]\d{2}\b`)
	// OMS Policy: 16 consecutive digits
	reOMS = regexp.MustCompile(`\b\d{16}\b`)
	// Bank card: 16 digits (4-4-4-4)
	reBankCard = regexp.MustCompile(`\b\d{4}[-\s]\d{4}[-\s]\d{4}[-\s]\d{4}\b`)
)

// MaskPII sanitizes sensitive personal data (phones, passports, SNILS, OMS, cards)
// before passing user speech to LLM classification endpoints, guaranteeing 152-FZ compliance.
func MaskPII(text string) string {
	if text == "" {
		return ""
	}
	s := rePhone.ReplaceAllString(text, "[ТЕЛЕФОН]")
	s = rePassport.ReplaceAllString(s, "[ПАСПОРТ]")
	s = reSNILS.ReplaceAllString(s, "[СНИЛС]")
	s = reOMS.ReplaceAllString(s, "[ОМС]")
	s = reBankCard.ReplaceAllString(s, "[КАРТА]")
	return s
}
