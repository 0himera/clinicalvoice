package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

type Config struct {
	BotToken string
	ChatID   string
}

func LoadConfigFromEnv() Config {
	return Config{
		BotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		ChatID:   os.Getenv("TELEGRAM_CHAT_ID"),
	}
}

type Notifier struct {
	config     Config
	httpClient *http.Client
}

func NewNotifier(cfg Config) *Notifier {
	return &Notifier{
		config: cfg,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

func (n *Notifier) IsConfigured() bool {
	return n.config.BotToken != "" && n.config.ChatID != ""
}

// SendMessage отправляет HTML-сообщение в Telegram чат
func (n *Notifier) SendMessage(ctx context.Context, text string) error {
	if !n.IsConfigured() {
		log.Printf("[TELEGRAM MOCK] Чат не настроен. Сообщение:\n%s\n", text)
		return nil
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", n.config.BotToken)
	reqBody := map[string]interface{}{
		"chat_id":    n.config.ChatID,
		"text":       text,
		"parse_mode": "HTML",
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка отправки в Telegram: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Telegram API вернул статус %d", resp.StatusCode)
	}

	return nil
}

// NotifyCategoryA экстренное уведомление дежурному администратору о красном флаге (103/112)
func (n *Notifier) NotifyCategoryA(ctx context.Context, callID, callerNumber, triggerText string) error {
	msg := fmt.Sprintf(
		"🚨 <b>ЭКСТРЕННЫЙ ВЫЗОВ (Категория А / 103)</b>\n"+
			"━━━━━━━━━━━━━━━━━━━━\n"+
			"📞 <b>Абонент:</b> %s\n"+
			"🆔 <b>ID звонка:</b> <code>%s</code>\n"+
			"⚠️ <b>Фраза триггера:</b> <i>\"%s\"</i>\n"+
			"⏱ <b>Время:</b> %s\n"+
			"━━━━━━━━━━━━━━━━━━━━\n"+
			"Пациент перенаправлен на 103/112 и в кабинет неотложной помощи.",
		callerNumber,
		callID,
		triggerText,
		time.Now().Format("15:04:05 02.01.2006"),
	)
	return n.SendMessage(ctx, msg)
}

// NotifyComplaint уведомление старшему администратору о жалобе или конфликте (С-15)
func (n *Notifier) NotifyComplaint(ctx context.Context, callID, callerNumber, text string) error {
	msg := fmt.Sprintf(
		"⚠️ <b>ЖАЛОБА / ОБРАЩЕНИЕ ПАЦИЕНТА (С-15)</b>\n"+
			"━━━━━━━━━━━━━━━━━━━━\n"+
			"📞 <b>Абонент:</b> %s\n"+
			"🆔 <b>ID звонка:</b> <code>%s</code>\n"+
			"💬 <b>Суть обращения:</b> <i>\"%s\"</i>\n"+
			"⏱ <b>Время:</b> %s\n"+
			"━━━━━━━━━━━━━━━━━━━━\n"+
			"Звонок переведён на старшего администратора (доб. 5).",
		callerNumber,
		callID,
		text,
		time.Now().Format("15:04:05 02.01.2006"),
	)
	return n.SendMessage(ctx, msg)
}
