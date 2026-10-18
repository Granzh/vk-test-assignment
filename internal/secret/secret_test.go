package secret

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"
)

const token = "123456789:AAH-tokenValue_abcdef"

func setup(t *testing.T) {
	t.Helper()
	Reset()
	Add(token)
	t.Cleanup(Reset)
}

func TestRedactHidesTheTokenWhereverItAppears(t *testing.T) {
	setup(t)
	in := `Post "https://api.telegram.org/bot` + token + `/getUpdates": dial tcp: lookup api.telegram.org: no such host`
	out := Redact(in)
	if strings.Contains(out, token) || !strings.Contains(out, "bot***/getUpdates") {
		t.Errorf("Redact = %q", out)
	}
	if got := Redact(token + " and " + token); strings.Contains(got, token) {
		t.Errorf("only the first occurrence was replaced: %q", got)
	}
	if Redact("nothing to hide") != "nothing to hide" {
		t.Error("text without secrets must stay unchanged")
	}
}

func TestAddIgnoresEmptyAndShortValues(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	Add("")
	Add("   ")
	Add("abc")
	if got := Redact("abc 123"); got != "abc 123" {
		t.Errorf("a short value was treated as a secret: %q", got)
	}
	Add("longer-secret")
	Add("longer-secret")
	if len(secrets) != 1 {
		t.Errorf("secrets = %v, duplicates must be ignored", secrets)
	}
}

func TestWrapKeepsTheChainButHidesTheMessage(t *testing.T) {
	setup(t)
	inner := &url.Error{Op: "Post", URL: "https://api.telegram.org/bot" + token + "/sendMessage", Err: io.ErrUnexpectedEOF}
	err := Wrap(fmt.Errorf("send: %w", inner))
	if strings.Contains(err.Error(), token) {
		t.Fatalf("token leaked: %v", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Error("errors.Is must still find the cause")
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		t.Error("errors.As must still find the original error")
	}
	if !IsRedacted(err) {
		t.Error("IsRedacted = false")
	}
}

func TestWrapLeavesCleanErrorsAlone(t *testing.T) {
	setup(t)
	orig := errors.New("plain failure")
	if Wrap(orig) != orig {
		t.Error("an error without secrets must be returned unchanged")
	}
	if Wrap(nil) != nil {
		t.Error("Wrap(nil) must be nil")
	}
}
