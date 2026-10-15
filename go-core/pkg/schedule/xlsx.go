// Package schedule reads the organizer's workbook without evaluating formulas.
// Cached Excel summary values are never used to decide slot availability.
package schedule

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/models"
)

type Entry struct {
	Slot models.Slot
	Free bool
}
type Snapshot struct {
	Doctors  []models.Doctor
	Entries  []Entry
	Holidays map[string]bool
}

type cell struct {
	Ref    string `xml:"r,attr"`
	Type   string `xml:"t,attr"`
	Value  string `xml:"v"`
	Inline string `xml:"is>t"`
}
type row struct {
	Cells []cell `xml:"c"`
}

func column(ref string) int {
	n := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		n = n*26 + int(r-'A'+1)
	}
	return n - 1
}

func rows(z *zip.ReadCloser, file string, shared []string, visit func([]string) error) error {
	var f *zip.File
	for _, entry := range z.File {
		if entry.Name == file {
			f = entry
			break
		}
	}
	if f == nil {
		return fmt.Errorf("missing workbook part %s", file)
	}
	if f.UncompressedSize64 > 200<<20 {
		return errors.New("workbook sheet exceeds 200 MiB")
	}
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	d := xml.NewDecoder(io.LimitReader(r, 200<<20))
	for {
		token, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "row" {
			continue
		}
		var rr row
		if err = d.DecodeElement(&rr, &start); err != nil {
			return err
		}
		values := []string{}
		for _, c := range rr.Cells {
			i := column(c.Ref)
			if i < 0 || i > 2048 {
				return errors.New("invalid cell reference")
			}
			for len(values) <= i {
				values = append(values, "")
			}
			v := c.Value
			if c.Type == "s" {
				j, err := strconv.Atoi(v)
				if err != nil || j < 0 || j >= len(shared) {
					return errors.New("invalid shared string")
				}
				v = shared[j]
			}
			if c.Type == "inlineStr" {
				v = c.Inline
			}
			values[i] = strings.TrimSpace(v)
		}
		if err = visit(values); err != nil {
			return err
		}
	}
}

func LoadXLSX(filename string) (Snapshot, error) {
	z, err := zip.OpenReader(filename)
	if err != nil {
		return Snapshot{}, err
	}
	defer z.Close()
	var shared []string
	sheets := map[string]string{}
	rels := map[string]string{}
	var workbook struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			ID   string `xml:"id,attr"`
		} `xml:"sheets>sheet"`
	}
	for _, f := range z.File {
		if f.UncompressedSize64 > 200<<20 {
			return Snapshot{}, errors.New("oversized workbook part")
		}
		switch f.Name {
		case "xl/sharedStrings.xml":
			r, e := f.Open()
			if e != nil {
				return Snapshot{}, e
			}
			d := xml.NewDecoder(io.LimitReader(r, 200<<20))
			for {
				t, e := d.Token()
				if e == io.EOF {
					break
				}
				if e != nil {
					_ = r.Close()
					return Snapshot{}, e
				}
				if start, ok := t.(xml.StartElement); ok && start.Name.Local == "si" {
					var item struct {
						Text string `xml:"t"`
						Runs []struct {
							Text string `xml:"t"`
						} `xml:"r"`
					}
					if e = d.DecodeElement(&item, &start); e != nil {
						_ = r.Close()
						return Snapshot{}, e
					}
					v := item.Text
					for _, run := range item.Runs {
						v += run.Text
					}
					shared = append(shared, v)
				}
			}
			_ = r.Close()
		case "xl/workbook.xml":
			r, e := f.Open()
			if e != nil {
				return Snapshot{}, e
			}
			e = xml.NewDecoder(r).Decode(&workbook)
			_ = r.Close()
			if e != nil {
				return Snapshot{}, e
			}
		case "xl/_rels/workbook.xml.rels":
			var relationships struct {
				Items []struct {
					ID     string `xml:"Id,attr"`
					Target string `xml:"Target,attr"`
				} `xml:"Relationship"`
			}
			r, e := f.Open()
			if e != nil {
				return Snapshot{}, e
			}
			e = xml.NewDecoder(r).Decode(&relationships)
			_ = r.Close()
			if e != nil {
				return Snapshot{}, e
			}
			for _, rel := range relationships.Items {
				rels[rel.ID] = path.Join("xl", strings.TrimPrefix(rel.Target, "/xl/"))
			}
		}
	}
	for _, s := range workbook.Sheets {
		sheets[s.Name] = rels[s.ID]
	}
	if sheets["Врачи"] == "" || sheets["Слоты"] == "" {
		return Snapshot{}, errors.New("expected sheets Врачи and Слоты")
	}
	out := Snapshot{Holidays: map[string]bool{}}
	doctors := map[string]models.Doctor{}
	names := map[string]bool{}
	err = rows(z, sheets["Врачи"], shared, func(r []string) error {
		if len(r) == 0 {
			return nil
		}
		if r[0] == "ID" {
			return nil
		}
		if len(r) < 7 {
			return fmt.Errorf("incomplete doctor row")
		}
		if r[1] == "" || r[3] == "" {
			return errors.New("doctor name and specialty are required")
		}
		key := NameKey(r[1])
		if names[key] {
			return fmt.Errorf("duplicate doctor name %s", r[1])
		}
		names[key] = true
		localID, err := strconv.Atoi(strings.TrimPrefix(r[0], "D"))
		if err != nil || localID <= 0 {
			return fmt.Errorf("invalid workbook doctor ID %q", r[0])
		}
		if _, ok := doctors[r[0]]; ok {
			return fmt.Errorf("duplicate workbook doctor ID %q", r[0])
		}
		d := models.Doctor{ID: localID, FullName: r[1], Specialty: strings.ToLower(r[3]), Room: r[5], Audience: r[4], IsActive: true}
		doctors[r[0]] = d
		out.Doctors = append(out.Doctors, d)
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	seen := map[string]bool{}
	err = rows(z, sheets["Слоты"], shared, func(r []string) error {
		if len(r) == 0 {
			return nil
		}
		if r[0] == "ID слота" {
			return nil
		}
		if len(r) < 12 {
			return fmt.Errorf("incomplete slot row")
		}
		d, ok := doctors[r[3]]
		if !ok {
			return fmt.Errorf("unknown workbook doctor %s", r[3])
		}
		if NameKey(d.FullName) != NameKey(r[4]) || NameKey(d.Specialty) != NameKey(r[5]) {
			return fmt.Errorf("inconsistent doctor in slot %s", r[0])
		}
		date, err := excelDate(r[1])
		if err != nil {
			return err
		}
		mins, err := excelMinutes(r[8])
		if err != nil {
			return err
		}
		duration, err := strconv.Atoi(r[10])
		if err != nil || duration <= 0 || duration > 180 {
			return fmt.Errorf("invalid duration in %s", r[0])
		}
		end, err := excelMinutes(r[9])
		if err != nil || end-mins != duration {
			return fmt.Errorf("invalid end time in %s", r[0])
		}
		start := date.Add(time.Duration(mins) * time.Minute)
		key := fmt.Sprintf("%d:%s", d.ID, start.Format(time.RFC3339))
		if seen[key] {
			return fmt.Errorf("duplicate slot %s", r[0])
		}
		seen[key] = true
		switch r[11] {
		case "Свободен", "Записан", "Приём состоялся", "Неявка", "Отменён пациентом":
		default:
			return fmt.Errorf("unknown slot status %q", r[11])
		}
		out.Entries = append(out.Entries, Entry{Slot: models.Slot{DoctorID: d.ID, Doctor: d.FullName, Specialty: d.Specialty, Room: r[7], StartTime: start, Duration: duration}, Free: r[11] == "Свободен"})
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	if sheets["Календарь"] != "" {
		var dates []string
		err = rows(z, sheets["Календарь"], shared, func(r []string) error {
			if len(r) < 4 {
				return nil
			}
			if len(r) > 3 && r[0] == "ID" {
				for _, v := range r[3:] {
					d, e := excelDate(v)
					if e != nil {
						return e
					}
					dates = append(dates, d.Format("2006-01-02"))
				}
				return nil
			}
			if _, ok := doctors[r[0]]; ok {
				for i, v := range r[3:] {
					if v == "П" && i < len(dates) {
						out.Holidays[dates[i]] = true
					}
				}
			}
			return nil
		})
		if err != nil {
			return Snapshot{}, err
		}
	}
	if len(out.Doctors) == 0 || len(out.Entries) == 0 {
		return Snapshot{}, errors.New("empty schedule")
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i].Slot, out.Entries[j].Slot
		if a.StartTime.Equal(b.StartTime) {
			return a.DoctorID < b.DoctorID
		}
		return a.StartTime.Before(b.StartTime)
	})
	return out, nil
}

func NameKey(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(strings.ToLower(s), "ё", "е")), " ")
}

// MatchDoctors deliberately ignores workbook IDs; only MIS IDs may be sent to its API.
func (s *Snapshot) MatchDoctors(actual []models.Doctor) error {
	byName := map[string]models.Doctor{}
	for _, d := range actual {
		key := NameKey(d.FullName)
		if _, ok := byName[key]; ok {
			return fmt.Errorf("ambiguous MIS doctor %s", d.FullName)
		}
		byName[key] = d
	}
	mapping := map[int]models.Doctor{}
	for i, old := range s.Doctors {
		d, ok := byName[NameKey(old.FullName)]
		if !ok || models.SpecialtyKey(d.Specialty) != models.SpecialtyKey(old.Specialty) {
			return fmt.Errorf("doctor not matched to MIS: %s (%s)", old.FullName, old.Specialty)
		}
		d.Room = old.Room
		d.Audience = old.Audience
		s.Doctors[i] = d
		mapping[old.ID] = d
	}
	for i := range s.Entries {
		d := mapping[s.Entries[i].Slot.DoctorID]
		s.Entries[i].Slot.DoctorID = d.ID
	}
	return nil
}

func excelDate(s string) (time.Time, error) {
	if t, e := time.ParseInLocation("02.01.2006", s, models.ClinicLocation); e == nil {
		return t, nil
	}
	n, e := strconv.ParseFloat(s, 64)
	if e != nil || n < 61 || n > 100000 {
		return time.Time{}, fmt.Errorf("invalid Excel date %q", s)
	}
	return time.Date(1899, 12, 30, 0, 0, 0, 0, models.ClinicLocation).AddDate(0, 0, int(n)), nil
}
func excelMinutes(s string) (int, error) {
	if t, e := time.Parse("15:04", s); e == nil {
		return t.Hour()*60 + t.Minute(), nil
	}
	n, e := strconv.ParseFloat(s, 64)
	if e != nil || n < 0 || n >= 1 {
		return 0, fmt.Errorf("invalid Excel time %q", s)
	}
	return int(n*1440 + 0.5), nil
}
