package schedule

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/himera/tochka-voice/go-core/pkg/models"
)

func createSyntheticXLSX(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "synthetic.xlsx")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()

	wbXML := `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets>
    <sheet name="Врачи" sheetId="1" r:id="rId1"/>
    <sheet name="Слоты" sheetId="2" r:id="rId2"/>
    <sheet name="Календарь" sheetId="3" r:id="rId3"/>
  </sheets>
</workbook>`
	w, _ := zw.Create("xl/workbook.xml")
	_, _ = w.Write([]byte(wbXML))

	relsXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/>
  <Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet3.xml"/>
</Relationships>`
	w, _ = zw.Create("xl/_rels/workbook.xml.rels")
	_, _ = w.Write([]byte(relsXML))

	sheet1XML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <sheetData>
    <row r="1">
      <c r="A1" t="inlineStr"><is><t>ID</t></is></c>
      <c r="B1" t="inlineStr"><is><t>ФИО</t></is></c>
      <c r="C1" t="inlineStr"><is><t>СНИЛС</t></is></c>
      <c r="D1" t="inlineStr"><is><t>Специальность</t></is></c>
      <c r="E1" t="inlineStr"><is><t>Аудитория</t></is></c>
      <c r="F1" t="inlineStr"><is><t>Кабинет</t></is></c>
      <c r="G1" t="inlineStr"><is><t>Статус</t></is></c>
    </row>
    <row r="2">
      <c r="A2" t="inlineStr"><is><t>D1</t></is></c>
      <c r="B2" t="inlineStr"><is><t>Иванов Иван Иванович</t></is></c>
      <c r="C2" t="inlineStr"><is><t>111-222-333 44</t></is></c>
      <c r="D2" t="inlineStr"><is><t>терапевт</t></is></c>
      <c r="E2" t="inlineStr"><is><t>взрослые</t></is></c>
      <c r="F2" t="inlineStr"><is><t>101</t></is></c>
      <c r="G2" t="inlineStr"><is><t>работает</t></is></c>
    </row>
  </sheetData>
</worksheet>`
	w, _ = zw.Create("xl/worksheets/sheet1.xml")
	_, _ = w.Write([]byte(sheet1XML))

	sheet2XML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <sheetData>
    <row r="1">
      <c r="A1" t="inlineStr"><is><t>ID слота</t></is></c>
      <c r="B1" t="inlineStr"><is><t>Дата</t></is></c>
      <c r="C1" t="inlineStr"><is><t>День</t></is></c>
      <c r="D1" t="inlineStr"><is><t>Врач_ID</t></is></c>
      <c r="E1" t="inlineStr"><is><t>ФИО</t></is></c>
      <c r="F1" t="inlineStr"><is><t>Специальность</t></is></c>
      <c r="G1" t="inlineStr"><is><t>Участок</t></is></c>
      <c r="H1" t="inlineStr"><is><t>Кабинет</t></is></c>
      <c r="I1" t="inlineStr"><is><t>Начало</t></is></c>
      <c r="J1" t="inlineStr"><is><t>Конец</t></is></c>
      <c r="K1" t="inlineStr"><is><t>Длительность</t></is></c>
      <c r="L1" t="inlineStr"><is><t>Статус</t></is></c>
    </row>
    <row r="2">
      <c r="A2" t="inlineStr"><is><t>S1</t></is></c>
      <c r="B2" t="inlineStr"><is><t>2026-11-10</t></is></c>
      <c r="C2" t="inlineStr"><is><t>Вторник</t></is></c>
      <c r="D2" t="inlineStr"><is><t>D1</t></is></c>
      <c r="E2" t="inlineStr"><is><t>Иванов Иван Иванович</t></is></c>
      <c r="F2" t="inlineStr"><is><t>терапевт</t></is></c>
      <c r="G2" t="inlineStr"><is><t>взрослые</t></is></c>
      <c r="H2" t="inlineStr"><is><t>101</t></is></c>
      <c r="I2" t="inlineStr"><is><t>10:00</t></is></c>
      <c r="J2" t="inlineStr"><is><t>10:20</t></is></c>
      <c r="K2" t="inlineStr"><is><t>20</t></is></c>
      <c r="L2" t="inlineStr"><is><t>Свободен</t></is></c>
    </row>
  </sheetData>
</worksheet>`
	w, _ = zw.Create("xl/worksheets/sheet2.xml")
	_, _ = w.Write([]byte(sheet2XML))

	sheet3XML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <sheetData>
    <row r="1">
      <c r="A1" t="inlineStr"><is><t>ID</t></is></c>
      <c r="B1" t="inlineStr"><is><t>ФИО</t></is></c>
      <c r="C1" t="inlineStr"><is><t>Специальность</t></is></c>
      <c r="D1" t="inlineStr"><is><t>2026-11-04</t></is></c>
    </row>
    <row r="2">
      <c r="A2" t="inlineStr"><is><t>D1</t></is></c>
      <c r="B2" t="inlineStr"><is><t>Иванов Иван Иванович</t></is></c>
      <c r="C2" t="inlineStr"><is><t>терапевт</t></is></c>
      <c r="D2" t="inlineStr"><is><t>П</t></is></c>
    </row>
  </sheetData>
</worksheet>`
	w, _ = zw.Create("xl/worksheets/sheet3.xml")
	_, _ = w.Write([]byte(sheet3XML))

	return p
}

func TestSyntheticWorkbook(t *testing.T) {
	path := createSyntheticXLSX(t)
	s, err := LoadXLSX(path)
	if err != nil {
		t.Fatalf("LoadXLSX failed on synthetic workbook: %v", err)
	}
	if len(s.Doctors) != 1 || s.Doctors[0].FullName != "Иванов Иван Иванович" {
		t.Fatalf("expected 1 doctor, got: %v", s.Doctors)
	}
	if len(s.Entries) != 1 || !s.Entries[0].Free {
		t.Fatalf("expected 1 free slot entry, got: %v", s.Entries)
	}
	if !s.Holidays["2026-11-04"] {
		t.Fatal("expected holiday 2026-11-04 not found")
	}

	misDoctors := []models.Doctor{{ID: 501, FullName: "Иванов Иван Иванович", Specialty: "терапевт", IsActive: true}}
	if err := s.MatchDoctors(misDoctors); err != nil {
		t.Fatalf("MatchDoctors failed: %v", err)
	}
	if s.Entries[0].Slot.DoctorID != 501 {
		t.Fatalf("expected slot DoctorID=501, got: %d", s.Entries[0].Slot.DoctorID)
	}
}

func TestOrganizerWorkbook(t *testing.T) {
	paths, err := filepath.Glob("../../../data/*.xlsx")
	if err != nil || len(paths) == 0 {
		paths, err = filepath.Glob("data/*.xlsx")
	}
	if err != nil || len(paths) == 0 {
		t.Skip("organizer workbook not available, skipping optional integration check")
	}
	s, err := LoadXLSX(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Doctors) < 80 || len(s.Entries) < 100000 {
		t.Fatalf("unexpected workbook size: %d doctors, %d slots", len(s.Doctors), len(s.Entries))
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
