package admin

import (
	"log/slog"
	"strings"

	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/texts"
)

// IsAdmin reports whether telegramID is listed in ADMIN_IDS.
func IsAdmin(admins []int64, telegramID int64) bool {
	for _, id := range admins {
		if id == telegramID {
			return true
		}
	}
	return false
}

// Guard lets only ADMIN_IDS through. Everyone else is ignored without any
// reply, so the existence of the admin commands is not revealed; the attempt
// is logged. Admin commands also work only in a private chat, so that
// exports and promo codes are never posted into a group.
func Guard(admins []int64, log *slog.Logger) tele.MiddlewareFunc {
	allowed := make(map[int64]bool, len(admins))
	for _, id := range admins {
		allowed[id] = true
	}
	return func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) error {
			u := c.Sender()
			if u == nil || !allowed[u.ID] {
				// Only the command word is logged: the rest of the message is the
				// stranger's own text and may contain personal data.
				attrs := []any{"command", commandWord(c.Text())}
				if u != nil {
					attrs = append(attrs, "user_id", u.ID)
				}
				log.Warn("admin command denied", attrs...)
				return nil
			}
			if chat := c.Chat(); chat != nil && chat.Type != tele.ChatPrivate {
				return c.Send(texts.AdminError("Админские команды работают только в личной переписке с ботом."))
			}
			return next(c)
		}
	}
}

// commandWord returns the leading "/command" of a message, or "" for other text.
func commandWord(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return ""
	}
	word := strings.Fields(text)[0]
	if r := []rune(word); len(r) > 32 {
		word = string(r[:32])
	}
	return word
}
