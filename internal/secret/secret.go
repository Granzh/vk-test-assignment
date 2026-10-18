// Package secret keeps credentials out of places where text is shown or stored.
//
// Telegram client errors contain the request URL, and the URL contains the bot
// token (".../bot<TOKEN>/getUpdates"). Left alone, such an error would end up in
// the logs, in the chat of an admin who sees an error message, or in the
// database (broadcast_recipients.last_error). Every secret registered here is
// replaced by a placeholder wherever Redact or Wrap is applied.
package secret

import (
	"errors"
	"strings"
	"sync"
)

const placeholder = "***"

// minLength ignores values too short to be real secrets: replacing "1" or "ab"
// everywhere would only mangle the text.
const minLength = 6

var (
	mu      sync.RWMutex
	secrets []string
)

// Add registers a secret to be hidden. Empty and very short values are ignored.
func Add(s string) {
	s = strings.TrimSpace(s)
	if len(s) < minLength {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	for _, have := range secrets {
		if have == s {
			return
		}
	}
	secrets = append(secrets, s)
}

// Reset forgets all secrets (tests).
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	secrets = nil
}

// Redact replaces every registered secret in s.
func Redact(s string) string {
	mu.RLock()
	defer mu.RUnlock()
	for _, sec := range secrets {
		if strings.Contains(s, sec) {
			s = strings.ReplaceAll(s, sec, placeholder)
		}
	}
	return s
}

// redactedError shows a redacted message but still unwraps to the original
// error, so errors.Is and errors.As keep working.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// Wrap returns err with its message redacted. An error that contains no
// secret is returned as it is.
func Wrap(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if clean := Redact(msg); clean != msg {
		return &redactedError{msg: clean, err: err}
	}
	return err
}

// IsRedacted reports whether err went through Wrap and had something to hide.
func IsRedacted(err error) bool {
	var r *redactedError
	return errors.As(err, &r)
}
