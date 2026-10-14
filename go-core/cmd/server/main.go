package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/himera/tochka-voice/go-core/pkg/campaign"
	"github.com/himera/tochka-voice/go-core/pkg/dialogue"
	"github.com/himera/tochka-voice/go-core/pkg/llmclient"
	"github.com/himera/tochka-voice/go-core/pkg/misclient"
	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/safety"
	"github.com/himera/tochka-voice/go-core/pkg/slotmgr"
)

func main() {
	log.Println("[SERVER] Запуск ядра голосового ассистента поликлиники № 7...")

	// 1. Инициализация локального менеджера расписания (25 врачей, слоты 20/30 мин)
	slotMgr := slotmgr.NewSlotManager()
	log.Printf("[SLOT-MGR] Инициализирован справочник: %d врачей, сетка приёма 08:00-20:00\n", len(slotMgr.ListDoctors()))

	// 2. Инициализация клиента МИС (Mock или Real по наличию токена)
	token := os.Getenv("MIS_API_TOKEN")
	var misClient misclient.MISClient

	if token != "" {
		log.Println("[MIS-CLIENT] Режим: РЕАЛЬНЫЙ СТЕНД (crm-med-mock.dbi-testing.ru), Rate-Limiter: 10 req/min")
		misClient = misclient.NewRealMISClient("https://crm-med-mock.dbi-testing.ru/api/v1", token)
	} else {
		log.Println("[MIS-CLIENT] Режим: ВНУТРЕННИЙ MOCK (автономный режим без токена)")
		misClient = misclient.NewMockMISClient()
	}

	// 3. Инициализация Fast-Gate безопасности (0 пропущенных красных флагов)
	fastGate := safety.NewFastGate()
	_ = fastGate
	log.Println("[SAFETY] Fast-Gate активирован: проверка Категории А (103/112), Категории Б и перевод на человека (R-02)")

	// 4. Инициализация FSM Контроллера диалога и Universal LLM клиента
	fsmController := dialogue.NewFSMController(slotMgr, misClient)
	llmCfg := llmclient.LoadConfigFromEnv()
	llmClient := llmclient.NewUniversalLLMClient(llmCfg)
	if llmClient.IsEnabled() {
		log.Printf("[LLM] Универсальный LLM клиент активен: провайдер=%s, модель=%s, timeout=%s\n", llmCfg.Provider, llmCfg.Model, llmCfg.Timeout)
		fsmController.SetLLMClient(llmClient)
	} else {
		log.Println("[LLM] LLM клиент не настроен (активен локальный детерминированный FSM/Regex режим)")
	}
	_ = fsmController

	// 5. Инициализация модуля исходящих напоминаний (Сценарий С-10)
	campaignMgr := campaign.NewReminderCampaign(misClient, slotMgr, 2)
	log.Println("[CAMPAIGN] Модуль исходящих напоминаний (С-10) готов. Окно обзвона: 10:00–19:00 (будни), 10:00–15:00 (сб)")

	// Запуск фонового тикера для проверки исходящих напоминаний
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if campaign.IsAllowedCallingHour(now) {
					pending := campaignMgr.GetPendingTasks()
					if len(pending) > 0 {
						log.Printf("[CAMPAIGN] Запуск пачки напоминаний накануне: %d звонков\n", len(pending))
					}
				}
			}
		}
	}()

	// 6. HTTP Health & Status эндпоинт
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","doctors":%d,"timestamp":"%s"}`, len(slotMgr.ListDoctors()), time.Now().Format(time.RFC3339))
	})

	mux.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"active_doctors":25,"target_missed_calls_pct":15,"target_no_shows_pct":12}`)
	})

	var activeSessions sync.Map

	// Старт новой голосовой сессии (приветствие бота)
	mux.HandleFunc("/api/dialogue/start", func(w http.ResponseWriter, r *http.Request) {
		callID := uuid.New().String()
		sess := &models.CallSession{
			CallID:       callID,
			CallerNumber: "+7 (999) 000-00-00",
			ChannelID:    "voice-channel-" + callID,
			CurrentState: "STATE_MAIN_MENU",
			Slots:        make(map[string]interface{}),
			CreatedAt:    time.Now(),
		}
		initRes := fsmController.InitialGreeting()
		sess.CurrentState = initRes.NextState
		activeSessions.Store(callID, sess)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"call_id":         callID,
			"bot_speech":      initRes.BotSpeech,
			"state":           initRes.NextState,
			"should_hangup":   initRes.ShouldHangup,
			"transfer_target": initRes.TransferTarget,
		})
	})

	// Шаг диалога (обработка распознанной реплики пользователя)
	mux.HandleFunc("/api/dialogue/turn", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			CallID string `json:"call_id"`
			Text   string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if req.CallID == "" {
			req.CallID = uuid.New().String()
		}

		val, ok := activeSessions.Load(req.CallID)
		var sess *models.CallSession
		if !ok {
			sess = &models.CallSession{
				CallID:       req.CallID,
				CallerNumber: "+7 (999) 000-00-00",
				ChannelID:    "voice-channel-" + req.CallID,
				CurrentState: "STATE_MAIN_MENU",
				Slots:        make(map[string]interface{}),
				CreatedAt:    time.Now(),
			}
			activeSessions.Store(req.CallID, sess)
		} else {
			sess = val.(*models.CallSession)
		}

		// 1. Проверка Fast-Gate (Красные флаги и оператор)
		gateRes := fastGate.Check(req.Text)
		if gateRes.Level != models.FlagNone {
			resp := map[string]interface{}{
				"call_id":         sess.CallID,
				"bot_speech":      gateRes.BotResponse,
				"state":           "STATE_TRANSFER",
				"should_hangup":   gateRes.Level == models.FlagCategoryA,
				"transfer_target": gateRes.RouteTarget,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		// 2. Шаг FSM
		stepRes := fsmController.ProcessInput(r.Context(), sess, req.Text)
		if stepRes.ShouldHangup {
			activeSessions.Delete(req.CallID)
		}

		resp := map[string]interface{}{
			"call_id":         sess.CallID,
			"bot_speech":      stepRes.BotSpeech,
			"state":           stepRes.NextState,
			"scenario":        sess.CurrentScenario,
			"should_hangup":   stepRes.ShouldHangup,
			"transfer_target": stepRes.TransferTarget,
			"llm_used":        stepRes.LLMUsed,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	server := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	go func() {
		log.Println("[SERVER] HTTP API доступен на http://0.0.0.0:8080")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[SERVER] Ошибка HTTP сервера: %v\n", err)
		}
	}()

	// Graceful Shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("[SERVER] Завершение работы...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	server.Shutdown(shutdownCtx)
	log.Println("[SERVER] Остановлен корректно.")
}
