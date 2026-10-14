package models

import "time"

// Doctor представляет врача в МИС и локальном справочнике
type Doctor struct {
	ID        int    `json:"id"`
	FullName  string `json:"full_name"`
	Specialty string `json:"specialty"`
	IsActive  bool   `json:"is_active"`
	Room      string `json:"room,omitempty"` // Кабинет приёма (по регламенту)
}

// Client представляет карточку пациента в МИС
type Client struct {
	ID         int    `json:"id"`
	LastName   string `json:"last_name"`
	FirstName  string `json:"first_name"`
	MiddleName string `json:"middle_name,omitempty"`
	BirthDate  string `json:"birth_date"` // ДД.ММ.ГГГГ
	Comment    string `json:"comment,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
}

// ClientCreate запрос на регистрацию клиента
type ClientCreate struct {
	LastName   string  `json:"last_name"`
	FirstName  string  `json:"first_name"`
	BirthDate  string  `json:"birth_date"`
	MiddleName *string `json:"middle_name,omitempty"`
	Comment    *string `json:"comment,omitempty"`
}

// Visit карточка записи на приём
type Visit struct {
	ID          int    `json:"id"`
	ClientID    int    `json:"client_id"`
	Doctor      Doctor `json:"doctor"`
	ScheduledAt string `json:"scheduled_at"` // ДД.ММ.ГГГГ ЧЧ:ММ (GMT+4)
	Status      string `json:"status"`       // Запланирована, Отменена, Завершена
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// VisitCreate запрос на создание записи
type VisitCreate struct {
	ClientID    int    `json:"client_id"`
	DoctorID    int    `json:"doctor_id"`
	ScheduledAt string `json:"scheduled_at"` // ДД.ММ.ГГГГ ЧЧ:ММ
}

// VisitUpdate запрос на перенос, смену врача или отмену
type VisitUpdate struct {
	DoctorID    *int    `json:"doctor_id,omitempty"`
	ScheduledAt *string `json:"scheduled_at,omitempty"`
	Status      *string `json:"status,omitempty"` // Запланирована, Отменена, Завершена
}

// Slot представляет свободный слот времени у врача
type Slot struct {
	DoctorID  int       `json:"doctor_id"`
	Doctor    string    `json:"doctor_name"`
	Specialty string    `json:"specialty"`
	Room      string    `json:"room"`
	StartTime time.Time `json:"start_time"`
	Duration  int       `json:"duration_minutes"` // 20 или 30 минут
	HeldBy    string    `json:"held_by,omitempty"` // session_id если заблокирован
	HeldUntil time.Time `json:"held_until,omitempty"`
}

// RedFlagLevel уровень экстренности
type RedFlagLevel string

const (
	FlagNone        RedFlagLevel = "NONE"
	FlagCategoryA   RedFlagLevel = "CATEGORY_A" // Угроза жизни -> 103/112
	FlagCategoryB   RedFlagLevel = "CATEGORY_B" // Острое состояние -> неотложка P1
	FlagOperatorReq RedFlagLevel = "OPERATOR_REQ" // Просьба человека -> оператор R-02
	FlagComplaint   RedFlagLevel = "COMPLAINT"    // Жалоба -> старший администратор R-03
)

// ScenarioCode код текущего диалогового сценария
type ScenarioCode string

const (
	ScenarioNone            ScenarioCode = "NONE"
	ScenarioBookingDirect   ScenarioCode = "C-01" // Прямая запись
	ScenarioBookingReferral ScenarioCode = "C-02" // По направлению
	ScenarioReschedule      ScenarioCode = "C-03" // Перенос записи
	ScenarioCancel          ScenarioCode = "C-04" // Отмена записи
	ScenarioCheckStatus     ScenarioCode = "C-05" // Проверка своей записи
	ScenarioScheduleInfo    ScenarioCode = "C-07" // График работы
	ScenarioOutboundRemind  ScenarioCode = "C-10" // Напоминание накануне
	ScenarioEmergency       ScenarioCode = "C-14" // Экстренная ситуация
	ScenarioComplaint       ScenarioCode = "C-15" // Жалоба
)

// CallSession состояние сессии звонка
type CallSession struct {
	CallID           string                 `json:"call_id"`
	CallerNumber     string                 `json:"caller_number"`
	ChannelID        string                 `json:"channel_id"`
	CurrentScenario  ScenarioCode           `json:"current_scenario"`
	CurrentState     string                 `json:"current_state"`
	FailCount        int                    `json:"fail_count"` // Непонимания подряд
	Patient          *Client                `json:"patient,omitempty"`
	SelectedDoctor   *Doctor                `json:"selected_doctor,omitempty"`
	SelectedSlot     *Slot                  `json:"selected_slot,omitempty"`
	ActiveVisits     []Visit                `json:"active_visits,omitempty"`
	SlotHeldKey      string                 `json:"slot_held_key,omitempty"`
	Slots            map[string]interface{} `json:"slots"` // Собранные сущности
	IsRecordingAgreed bool                  `json:"is_recording_agreed"`
	CreatedAt        time.Time              `json:"created_at"`
}
