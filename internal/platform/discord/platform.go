package discord

import (
	"context"
	"fmt"
	"log"
	"strconv"

	"github.com/bwmarrin/discordgo"

	"github.com/pickmoment/remode/internal/core"
)

// Platform implements core.ChatPlatform for Discord.
type Platform struct {
	session *discordgo.Session
}

// NewPlatform wraps an existing discordgo.Session.
func NewPlatform(s *discordgo.Session) *Platform {
	return &Platform{session: s}
}

// Send renders msg and sends it to the Discord channel with the given ID.
func (p *Platform) Send(_ context.Context, chatID int64, msg core.Message, sessionPrefix string) error {
	rendered := Render(msg, sessionPrefix)
	channelID := strconv.FormatInt(chatID, 10)
	for _, r := range rendered {
		params := &discordgo.MessageSend{Content: r.Content}
		if len(r.Components) > 0 {
			params.Components = r.Components
		}
		_, err := p.session.ChannelMessageSendComplex(channelID, params)
		if err != nil {
			log.Printf("discord send error (channel %s): %v", channelID, err)
			return fmt.Errorf("discord send: %w", err)
		}
	}
	return nil
}
