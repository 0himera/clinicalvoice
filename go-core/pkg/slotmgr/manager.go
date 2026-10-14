package slotmgr

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/models"
)

type SlotManager struct {
	mu           sync.RWMutex
	doctors      map[int]models.Doctor
	bookedSlots  map[string]bool      // key: "docID:YYYY-MM-DD-HH:MM"
	heldSlots    map[string]slotHold  // key: "docID:YYYY-MM-DD-HH:MM"
}

type slotHold struct {
	sessionID string
	expiresAt time.Time
}

func NewSlotManager() *SlotManager {
	sm := &SlotManager{
		doctors:     make(map[int]models.Doctor),
		bookedSlots: make(map[string]bool),
		heldSlots:   make(map[string]slotHold),
	}
	sm.initDefaultDoctors()
	return sm
}

// initDefaultDoctors загружает 25 врачей поликлиники № 7 по регламенту
func (sm *SlotManager) initDefaultDoctors() {
	defaultDocs := []models.Doctor{
		// Терапевтическое отделение (слот 20 мин)
		{ID: 1, FullName: "Васильева Татьяна Геннадьевна", Specialty: "терапевт", IsActive: true, Room: "210"},
		{ID: 2, FullName: "Кузнецов Андрей Павлович", Specialty: "терапевт", IsActive: true, Room: "212"},
		{ID: 3, FullName: "Орлова Марина Сергеевна", Specialty: "терапевт", IsActive: true, Room: "214"},
		{ID: 4, FullName: "Смирнова Елена Викторовна", Specialty: "терапевт", IsActive: true, Room: "216"},
		// Педиатрическое отделение (слот 20 мин)
		{ID: 5, FullName: "Белова Ольга Николаевна", Specialty: "педиатр", IsActive: true, Room: "101"},
		{ID: 6, FullName: "Громов Илья Александрович", Specialty: "педиатр", IsActive: true, Room: "102"},
		{ID: 7, FullName: "Попова Дарья Андреевна", Specialty: "педиатр", IsActive: true, Room: "103"},
		// ЛОР (слот 20 мин)
		{ID: 8, FullName: "Волков Артём Сергеевич", Specialty: "оториноларинголог", IsActive: true, Room: "301"},
		{ID: 9, FullName: "Медведева Алина Игоревна", Specialty: "оториноларинголог", IsActive: true, Room: "302"},
		{ID: 10, FullName: "Никитина Ирина Владимировна", Specialty: "оториноларинголог", IsActive: true, Room: "303"},
		// Офтальмологи (слот 20 мин)
		{ID: 11, FullName: "Егоров Максим Андреевич", Specialty: "офтальмолог", IsActive: true, Room: "305"},
		{ID: 12, FullName: "Морозова Светлана Петровна", Specialty: "офтальмолог", IsActive: true, Room: "306"},
		// Дерматовенеролог (слот 20 мин)
		{ID: 13, FullName: "Тихонова Екатерина Олеговна", Specialty: "дерматовенеролог", IsActive: true, Room: "401"},
		// Акушеры-гинекологи (слот 30 мин)
		{ID: 14, FullName: "Ковалёва Юлия Дмитриевна", Specialty: "акушер-гинеколог", IsActive: true, Room: "201"},
		{ID: 15, FullName: "Семёнова Виктория Алексеевна", Specialty: "акушер-гинеколог", IsActive: true, Room: "202"},
		// Хирург (слот 30 мин)
		{ID: 16, FullName: "Макаров Павел Сергеевич", Specialty: "хирург", IsActive: true, Room: "405"},
		// Травматологи (слот 30 мин)
		{ID: 17, FullName: "Зайцев Константин Ильич", Specialty: "травматолог-ортопед", IsActive: true, Room: "407"},
		{ID: 18, FullName: "Степанов Григорий Львович", Specialty: "травматолог-ортопед", IsActive: true, Room: "408"},
		// Неврологи (слот 30 мин, по направлению)
		{ID: 19, FullName: "Захаров Дмитрий Олегович", Specialty: "невролог", IsActive: true, Room: "501"},
		{ID: 20, FullName: "Фёдорова Наталья Юрьевна", Specialty: "невролог", IsActive: true, Room: "502"},
		// Кардиологи (слот 30 мин, по направлению)
		{ID: 21, FullName: "Киселёв Алексей Романович", Specialty: "кардиолог", IsActive: true, Room: "505"},
		{ID: 22, FullName: "Лебедев Сергей Игоревич", Specialty: "кардиолог", IsActive: true, Room: "506"},
		// Гастроэнтеролог (слот 30 мин, по направлению)
		{ID: 23, FullName: "Павлов Роман Евгеньевич", Specialty: "гастроэнтеролог", IsActive: true, Room: "508"},
		// Уролог (слот 30 мин, по направлению)
		{ID: 24, FullName: "Андреев Николай Васильевич", Specialty: "уролог", IsActive: true, Room: "510"},
		// Эндокринолог (слот 30 мин, по направлению)
		{ID: 25, FullName: "Соколова Анна Михайловна", Specialty: "эндокринолог", IsActive: true, Room: "512"},
	}

	for _, d := range defaultDocs {
		sm.doctors[d.ID] = d
	}
}

// IsDirectBookingAllowed проверяет, доступна ли прямая самозапись без направления (Регламент 2.3)
func (sm *SlotManager) IsDirectBookingAllowed(specialty string) bool {
	s := strings.ToLower(strings.TrimSpace(specialty))
	switch {
	case strings.Contains(s, "терапевт"):
		return true
	case strings.Contains(s, "педиатр"):
		return true
	case strings.Contains(s, "хирург") && !strings.Contains(s, "сосудист"):
		return true
	case strings.Contains(s, "офтальмолог") || strings.Contains(s, "окулист"):
		return true
	case strings.Contains(s, "оториноларинголог") || strings.Contains(s, "лор"):
		return true
	case strings.Contains(s, "гинеколог") || strings.Contains(s, "акушер"):
		return true
	case strings.Contains(s, "стоматолог"):
		return true
	case strings.Contains(s, "дерматолог") || strings.Contains(s, "дерматовенеролог"):
		return true
	default:
		// Кардиолог, невролог, эндокринолог, гастроэнтеролог, уролог - ТОЛЬКО по направлению
		return false
	}
}

// GetSlotDuration возвращает длительность приёма врача в минутах
func (sm *SlotManager) GetSlotDuration(specialty string) int {
	s := strings.ToLower(specialty)
	if strings.Contains(s, "терапевт") || strings.Contains(s, "педиатр") ||
		strings.Contains(s, "лор") || strings.Contains(s, "оториноларинголог") ||
		strings.Contains(s, "офтальмолог") || strings.Contains(s, "дермато") {
		return 20
	}
	return 30
}

// FindAvailableSlots ищет ближайшие свободные слоты для специальности или врача
func (sm *SlotManager) FindAvailableSlots(specialty string, doctorID int, limit int) []models.Slot {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.cleanupExpiredHolds()

	var result []models.Slot
	now := time.Now()
	// Часовой пояс GMT+4 (по ТЗ стенда)
	loc := time.FixedZone("GMT+4", 4*3600)
	currentDay := now.In(loc)

	// Перебираем дни на горизонт 14 дней
	for dayOffset := 0; dayOffset < 14; dayOffset++ {
		date := currentDay.AddDate(0, 0, dayOffset)
		// Воскресенье поликлиника не работает
		if date.Weekday() == time.Sunday {
			continue
		}

		startHour := 8
		endHour := 20
		if date.Weekday() == time.Saturday {
			startHour = 9
			endHour = 15
		}

		for _, doc := range sm.doctors {
			if !doc.IsActive {
				continue
			}
			if doctorID > 0 && doc.ID != doctorID {
				continue
			}
			if doctorID == 0 && specialty != "" && !strings.Contains(strings.ToLower(doc.Specialty), strings.ToLower(specialty)) {
				continue
			}

			stepMin := sm.GetSlotDuration(doc.Specialty)

			for h := startHour; h < endHour; h++ {
				for m := 0; m < 60; m += stepMin {
					slotTime := time.Date(date.Year(), date.Month(), date.Day(), h, m, 0, 0, loc)

					// Не предлагаем слоты в прошлом (+ запас 30 мин по регламенту)
					if slotTime.Before(now.Add(30 * time.Minute)) {
						continue
					}

					key := sm.formatSlotKey(doc.ID, slotTime)
					if sm.bookedSlots[key] {
						continue
					}
					if hold, ok := sm.heldSlots[key]; ok && hold.expiresAt.After(now) {
						continue
					}

					result = append(result, models.Slot{
						DoctorID:  doc.ID,
						Doctor:    doc.FullName,
						Specialty: doc.Specialty,
						Room:      doc.Room,
						StartTime: slotTime,
						Duration:  stepMin,
					})

					if len(result) >= limit {
						return result
					}
				}
			}
		}
	}

	return result
}

// HoldSlot ставит временную блокировку на слот на время диалога
func (sm *SlotManager) HoldSlot(doctorID int, slotTime time.Time, sessionID string, ttl time.Duration) (string, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.cleanupExpiredHolds()

	key := sm.formatSlotKey(doctorID, slotTime)
	now := time.Now()

	if sm.bookedSlots[key] {
		return "", fmt.Errorf("слот уже занят")
	}

	if hold, ok := sm.heldSlots[key]; ok && hold.expiresAt.After(now) && hold.sessionID != sessionID {
		return "", fmt.Errorf("слот временно заблокирован другим пациентом")
	}

	sm.heldSlots[key] = slotHold{
		sessionID: sessionID,
		expiresAt: now.Add(ttl),
	}

	return key, nil
}

// ReleaseSlot освобождает временную блокировку слота
func (sm *SlotManager) ReleaseSlot(key string, sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if hold, ok := sm.heldSlots[key]; ok && hold.sessionID == sessionID {
		delete(sm.heldSlots, key)
	}
}

// CommitBooking окончательно фиксирует запись в локальной сетке
func (sm *SlotManager) CommitBooking(doctorID int, slotTime time.Time) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	key := sm.formatSlotKey(doctorID, slotTime)
	sm.bookedSlots[key] = true
	delete(sm.heldSlots, key)
}

// CancelBooking освобождает слот при отмене записи
func (sm *SlotManager) CancelBooking(doctorID int, slotTime time.Time) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	key := sm.formatSlotKey(doctorID, slotTime)
	delete(sm.bookedSlots, key)
	delete(sm.heldSlots, key)
}

func (sm *SlotManager) cleanupExpiredHolds() {
	now := time.Now()
	for k, h := range sm.heldSlots {
		if h.expiresAt.Before(now) {
			delete(sm.heldSlots, k)
		}
	}
}

func (sm *SlotManager) formatSlotKey(docID int, t time.Time) string {
	return fmt.Sprintf("%d:%s", docID, t.Format("2006-01-02-15:04"))
}

func (sm *SlotManager) GetDoctor(id int) (models.Doctor, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	d, ok := sm.doctors[id]
	return d, ok
}

func (sm *SlotManager) ListDoctors() []models.Doctor {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	list := make([]models.Doctor, 0, len(sm.doctors))
	for _, d := range sm.doctors {
		list = append(list, d)
	}
	return list
}
