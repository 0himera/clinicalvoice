package models

import (
	"testing"
	"time"
)

func TestSpecialtyAliasesKeepPediatricProfilesSeparate(t *testing.T) {
	if SpecialtyKey("ЛОР (отоларинголог)") != SpecialtyKey("оториноларинголог") {
		t.Fatal("LOR alias not resolved")
	}
	if SpecialtyKey("детский отоларинголог") == SpecialtyKey("оториноларинголог") {
		t.Fatal("child and adult profiles were merged")
	}
	if PediatricSpecialty("оториноларинголог") != "детский оториноларинголог" {
		t.Fatal("pediatric counterpart missing")
	}
}
func TestAgeAndAudienceBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, ClinicLocation)
	age, err := AgeOn("16.10.2008", now)
	if err != nil || age != 17 {
		t.Fatalf("age=%d err=%v", age, err)
	}
	if (Doctor{Audience: "Взрослые 18+"}).AcceptsAge(age) {
		t.Fatal("child accepted by adult-only doctor")
	}
	if !(Doctor{Audience: "Дети 0–17 лет"}).AcceptsAge(age) {
		t.Fatal("child rejected by pediatric doctor")
	}
	if (Doctor{Audience: "Дети до 1 года"}).AcceptsAge(1) {
		t.Fatal("infant age limit ignored")
	}
}
