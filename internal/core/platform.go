package core

import "context"

// ChatPlatform abstracts a chat service (Telegram, Discord) for message delivery.
type ChatPlatform interface {
	Send(ctx context.Context, chatID int64, msg Message, sessionPrefix string) error
}
