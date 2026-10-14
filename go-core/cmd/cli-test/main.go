package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/himera/tochka-voice/go-core/pkg/dialogue"
	"github.com/himera/tochka-voice/go-core/pkg/llmclient"
	"github.com/himera/tochka-voice/go-core/pkg/misclient"
	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/safety"
	"github.com/himera/tochka-voice/go-core/pkg/slotmgr"
	"github.com/himera/tochka-voice/go-core/pkg/telegram"
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorPurple = "\033[35m"
	colorCyan   = "\033[36m"
)

func main() {
	fmt.Println(colorCyan + "==========================================================" + colorReset)
	fmt.Println(colorCyan + "   ГОЛОСОВОЙ АССИСТЕНТ ПОЛИКЛИНИКИ № 7 (CLI TEST RUNNER)  " + colorReset)
	fmt.Println(colorCyan + "==========================================================" + colorReset)
	fmt.Println("Интерактивный симулятор телефонного звонка.")
	fmt.Println("Для завершения звонка введите 'exit' или 'hangup'.")

	// Инициализация сервисов
	slotMgr := slotmgr.NewSlotManager()
	mockMIS := misclient.NewMockMISClient()
	fastGate := safety.NewFastGate()
	fsm := dialogue.NewFSMController(slotMgr, mockMIS)

	llmCfg := llmclient.LoadConfigFromEnv()
	llmClient := llmclient.NewUniversalLLMClient(llmCfg)
	if llmClient.IsEnabled() {
		fmt.Printf("%s[LLM] Включен провайдер %s (%s)%s\n", colorGreen, llmCfg.Provider, llmCfg.Model, colorReset)
		fsm.SetLLMClient(llmClient)
	} else {
		fmt.Printf("%s[LLM] Офлайн режим (локальный FSM & Regex)%s\n", colorYellow, colorReset)
	}

	tgNotifier := telegram.NewNotifier(telegram.LoadConfigFromEnv())
	if tgNotifier.IsConfigured() {
		fmt.Printf("%s[TELEGRAM] Оповещения активированы (Категория А -> 103, Жалобы -> ст. администратор)%s\n", colorGreen, colorReset)
	}

	ctx := context.Background()
	reader := bufio.NewReader(os.Stdin)

	// Создаем сессию звонка
	session := &models.CallSession{
		CallID:       uuid.New().String(),
		CallerNumber: "+7 (999) 123-45-67",
		ChannelID:    "cli-test-channel",
		CurrentState: "STATE_MAIN_MENU",
		Slots:        make(map[string]interface{}),
		CreatedAt:    time.Now(),
	}

	// 1. Приветствие
	initResult := fsm.InitialGreeting()
	fmt.Printf("%s[БОТ]: %s%s\n\n", colorGreen, initResult.BotSpeech, colorReset)
	session.CurrentState = initResult.NextState

	for {
		fmt.Printf("%s[ВЫ (Пациент)]: %s", colorYellow, colorReset)
		input, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}

		if input == "exit" || input == "hangup" || input == "пока" {
			fmt.Println(colorRed + "\n[ТЕЛЕФОНИЯ]: Звонок завершён (Hangup)." + colorReset)
			break
		}

		// 1. Проверка FAST-GATE на Красные Флаги (103/112 и Оператор)
		safetyCheck := fastGate.Check(input)
		if safetyCheck.Level != models.FlagNone {
			fmt.Printf("%s[FAST-GATE ТРИГГЕР]: %s (Уровень: %s, Маршрут: %s)%s\n",
				colorPurple, safetyCheck.ActionCode, safetyCheck.Level, safetyCheck.RouteTarget, colorReset)
			fmt.Printf("%s[БОТ]: %s%s\n\n", colorRed, safetyCheck.BotResponse, colorReset)

			if safetyCheck.Level == models.FlagCategoryA {
				_ = tgNotifier.NotifyCategoryA(ctx, session.CallID, session.CallerNumber, input)
				fmt.Printf("%s[ТЕЛЕФОНИЯ]: Прерывание сценария. Экстренный перевод на 103/112 (кабинет %s)...%s\n\n",
					colorBlue, safetyCheck.RouteTarget, colorReset)
				break
			}
			if safetyCheck.Level == models.FlagOperatorReq {
				_ = tgNotifier.NotifyComplaint(ctx, session.CallID, session.CallerNumber, input)
				fmt.Printf("%s[ТЕЛЕФОНИЯ]: Перевод звонка на сотрудника %s...%s\n\n",
					colorBlue, safetyCheck.RouteTarget, colorReset)
				break
			}
			continue
		}

		// 2. Обработка шага FSM
		res := fsm.ProcessInput(ctx, session, input)
		fmt.Printf("%s[БОТ]: %s%s\n", colorGreen, res.BotSpeech, colorReset)
		fmt.Printf("%s[STATE]: %s (Сценарий: %s)%s\n\n",
			colorBlue, res.NextState, session.CurrentScenario, colorReset)

		session.CurrentState = res.NextState

		if res.TransferTarget != "" {
			fmt.Printf("%s[ТЕЛЕФОНИЯ]: Перевод звонка на оператора (доб. %s)%s\n\n",
				colorBlue, res.TransferTarget, colorReset)
			break
		}

		if res.ShouldHangup {
			fmt.Println(colorRed + "[ТЕЛЕФОНИЯ]: Ассистент положил трубку. До свидания!" + colorReset)
			break
		}
	}
}
