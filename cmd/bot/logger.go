package main

import (
	"io"
	"log/slog"

	"chat_bot/internal/secret"
)

// newLogger returns the JSON logger of the bot. Every string and error that
// goes through it is stripped of registered secrets (the bot token above all:
// Telegram client errors contain the request URL).
func newLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{ReplaceAttr: redactAttr}))
}

func redactAttr(_ []string, a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(secret.Redact(a.Value.String()))
	case slog.KindAny:
		switch v := a.Value.Any().(type) {
		case error:
			a.Value = slog.StringValue(secret.Redact(v.Error()))
		case interface{ String() string }:
			a.Value = slog.StringValue(secret.Redact(v.String()))
		}
	}
	return a
}
