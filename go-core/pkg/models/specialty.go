package models

import (
	"strings"
	"time"
)

func AgeOn(birthDate string, now time.Time) (int, error) {
	birth, err := time.ParseInLocation("02.01.2006", birthDate, ClinicLocation)
	if err != nil {
		return 0, err
	}
	now = now.In(ClinicLocation)
	age := now.Year() - birth.Year()
	if now.Month() < birth.Month() || (now.Month() == birth.Month() && now.Day() < birth.Day()) {
		age--
	}
	return age, nil
}

func (d Doctor) AcceptsAge(age int) bool {
	switch d.Audience {
	case "Взрослые 18+":
		return age >= 18
	case "Дети 0–17 лет":
		return age >= 0 && age < 18
	case "Женщины 16+":
		return age >= 16
	case "Дети до 1 года":
		return age == 0
	default:
		return true
	}
}

func PediatricSpecialty(s string) string {
	switch SpecialtyKey(s) {
	case "терапевт":
		return "педиатр"
	case "оториноларинголог":
		return "детский оториноларинголог"
	case "офтальмолог", "кардиолог", "невролог", "эндокринолог", "хирург":
		return "детский " + s
	case "стоматолог-терапевт":
		return "детский стоматолог"
	case "ортопед", "травматолог", "травматолог-ортопед":
		return "детский ортопед-травматолог"
	case "уролог":
		return "детский уролог-андролог"
	}
	return s
}

// SpecialtyKey reconciles established synonyms without merging distinct profiles
// such as an adult and a pediatric specialist.
func SpecialtyKey(value string) string {
	s := strings.Join(strings.Fields(strings.ReplaceAll(strings.ToLower(value), "ё", "е")), " ")
	switch s {
	case "лор", "лор (отоларинголог)", "отоларинголог", "оториноларинголог":
		return "оториноларинголог"
	case "детский отоларинголог", "детский лор":
		return "детский оториноларинголог"
	case "окулист":
		return "офтальмолог"
	case "стоматолог":
		return "стоматолог-терапевт"
	}
	return s
}
