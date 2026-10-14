package campaign

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/misclient"
	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/slotmgr"
)

type ReminderTask struct {
	VisitID       int
	ClientID      int
	PatientName   string
	Phone         string
	DoctorName    string
	Specialty     string
	Room          string
	ScheduledAt   string
	AttemptCount  int
	LastAttemptAt time.Time
	Status        string // PENDING, CONFIRMED, RESCHEDULED, CANCELLED, NO_ANSWER
}

type ReminderCampaign struct {
	mu            sync.Mutex
	misClient     misclient.MISClient
	slotMgr       *slotmgr.SlotManager
	tasks         map[int]*ReminderTask
	maxConcurrent int
	activeCalls   int
}

func NewReminderCampaign(mc misclient.MISClient, sm *slotmgr.SlotManager, maxConcurrent int) *ReminderCampaign {
	if maxConcurrent <= 0 {
		maxConcurrent = 2 // Не более 2 исходящих линий одновременно
	}
	return &ReminderCampaign{
		misClient:     mc,
		slotMgr:       sm,
		tasks:         make(map[int]*ReminderTask),
		maxConcurrent: maxConcurrent,
	}
}

// IsAllowedCallingHour проверяет разрешенные часы по регламенту С-10
// Будни 10:00–19:00, сб 10:00–15:00, вс и праздники — запрещено
func IsAllowedCallingHour(t time.Time) bool {
	weekday := t.Weekday()
	if weekday == time.Sunday {
		return false
	}
	h := t.Hour()
	if weekday == time.Saturday {
		return h >= 10 && h < 15
	}
	return h >= 10 && h < 19
}

// AddTask добавляет визит в очередь обзвона
func (rc *ReminderCampaign) AddTask(task ReminderTask) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	task.Status = "PENDING"
	rc.tasks[task.VisitID] = &task
}

// GetPendingTasks возвращает задачи, готовые к звонку
func (rc *ReminderCampaign) GetPendingTasks() []*ReminderTask {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	now := time.Now()
	if !IsAllowedCallingHour(now) {
		return nil
	}

	var ready []*ReminderTask
	for _, t := range rc.tasks {
		if t.Status == "PENDING" && t.AttemptCount < 2 {
			// Интервал не менее 2 часов между попытками
			if t.AttemptCount == 0 || now.Sub(t.LastAttemptAt) >= 2*time.Hour {
				ready = append(ready, t)
				if len(ready) >= rc.maxConcurrent {
					break
				}
			}
		}
	}
	return ready
}

// FormatReminderGreeting формирует начальную реплику для С-10
func (rc *ReminderCampaign) FormatReminderGreeting(task *ReminderTask) string {
	nameParts := strings.Fields(task.PatientName)
	greetingName := task.PatientName
	if len(nameParts) >= 2 {
		// "Иван Иванович" без фамилии по регламенту п. 3.5
		greetingName = strings.Join(nameParts[1:], " ")
	}

	return fmt.Sprintf("Здравствуйте, %s! Регистратура поликлиники номер семь. Напоминаем о вашей записи к %s %s завтра в %s, кабинет %s. Сможете подойти?",
		greetingName, task.Specialty, task.DoctorName, task.ScheduledAt, task.Room)
}

// HandlePatientResponse обрабатывает ответ пациента на звонок-напоминание
func (rc *ReminderCampaign) HandlePatientResponse(ctx context.Context, task *ReminderTask, responseText string) (string, bool) {
	norm := strings.ToLower(responseText)

	if strings.Contains(norm, "да") || strings.Contains(norm, "приду") || strings.Contains(norm, "буду") || strings.Contains(norm, "смогу") {
		rc.mu.Lock()
		task.Status = "CONFIRMED"
		rc.mu.Unlock()
		return "Отлично! Ждём вас завтра. Не забудьте паспорт и полис ОМС. Всего доброго!", true
	}

	if strings.Contains(norm, "нет") || strings.Contains(norm, "не смогу") || strings.Contains(norm, "отменит") {
		// Отмена записи в МИС и освобождение слота
		cancelStatus := "Отменена"
		_, _ = rc.misClient.UpdateVisit(ctx, task.VisitID, models.VisitUpdate{
			Status: &cancelStatus,
		})

		rc.mu.Lock()
		task.Status = "CANCELLED"
		rc.mu.Unlock()
		return "Хорошо, я отменила запись. Если понадобится новое время, перезвоните нам в любое время. Будьте здоровы!", true
	}

	return "Пожалуйста, подскажите: вы сможете подойти на приём завтра?", false
}
