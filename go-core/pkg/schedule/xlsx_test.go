package schedule

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestOrganizerWorkbook(t *testing.T) {
	paths, err := filepath.Glob("../../../data/*.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Skip("organizer workbook not available")
	}
	s, err := LoadXLSX(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Doctors) != 85 || len(s.Entries) != 136973 {
		t.Fatalf("workbook rows lost: %d doctors, %d slots", len(s.Doctors), len(s.Entries))
	}
	doctors := append(s.Doctors[:0:0], s.Doctors...)
	for i := range doctors {
		doctors[i].ID += 1000
	}
	if err = s.MatchDoctors(doctors); err != nil {
		t.Fatal(err)
	}
	for _, e := range s.Entries {
		if e.Slot.DoctorID < 1000 {
			t.Fatal("workbook ID leaked into MIS mapping")
		}
	}
	if !s.Holidays["2026-11-04"] || !s.Holidays["2026-12-31"] {
		t.Fatal("holidays were not imported")
	}
}

func TestMalformedWorkbookFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.xlsx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	part, err := z.Create("xl/workbook.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte(`<workbook><sheets/></workbook>`))
	_ = z.Close()
	_ = f.Close()
	if _, err = LoadXLSX(path); err == nil {
		t.Fatal("missing slot and doctor sheets accepted")
	}
}
