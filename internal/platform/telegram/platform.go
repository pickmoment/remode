package telegram

import (
	"context"
	"fmt"
	"log"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/pickmoment/remode/internal/core"
)

// Platform implements core.ChatPlatform for Telegram.
type Platform struct {
	bot *tgbotapi.BotAPI
}

// NewPlatform wraps an existing BotAPI instance.
func NewPlatform(bot *tgbotapi.BotAPI) *Platform {
	return &Platform{bot: bot}
}

// Send renders msg and delivers it, retrying on transient errors.
func (p *Platform) Send(ctx context.Context, chatID int64, msg core.Message, sessionPrefix string) error {
	rendered := Render(msg, sessionPrefix)
	for _, r := range rendered {
		if err := p.sendOne(ctx, chatID, r); err != nil {
			return err
		}
	}
	return nil
}

func (p *Platform) sendOne(ctx context.Context, chatID int64, r TelegramMessage) error {
	send := tgbotapi.NewMessage(chatID, r.Text)
	send.ParseMode = r.ParseMode
	if r.ReplyMarkup != nil {
		send.ReplyMarkup = r.ReplyMarkup
	}

	for attempt := 0; attempt < 5; attempt++ {
		_, err := p.bot.Send(send)
		if err == nil {
			return nil
		}

		// Telegram API error codes
		apiErr, ok := err.(tgbotapi.Error)
		if ok && apiErr.Code == 429 {
			// RetryAfter — parse seconds from message or use exponential backoff
			wait := time.Duration(apiErr.RetryAfter+1) * time.Second
			log.Printf("telegram rate limit, retrying in %v (attempt %d)", wait, attempt+1)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		if isTransient(err) {
			wait := time.Duration(1<<attempt) * time.Second
			log.Printf("telegram transient error, retrying in %v (attempt %d): %v", wait, attempt+1, err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		log.Printf("telegram send_message failed (chat_id=%d): %v", chatID, err)
		return nil // non-retryable: log and discard
	}
	return fmt.Errorf("telegram: exhausted retries for chat %d", chatID)
}

func isTransient(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return contains(s, "network") || contains(s, "timeout") || contains(s, "connection")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
