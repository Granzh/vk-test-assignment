package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"chat_bot/internal/config"
	"chat_bot/internal/secret"
)

const testToken = "123456789:AAH-realLookingTokenValue"

func TestLoggerNeverPrintsTheBotToken(t *testing.T) {
	secret.Reset()
	t.Cleanup(secret.Reset)
	secret.Add(testToken)

	var buf bytes.Buffer
	log := newLogger(&buf)

	// the shape of a Telegram client error: the URL carries the token
	urlErr := &url.Error{Op: "Post", URL: "https://api.telegram.org/bot" + testToken + "/getUpdates", Err: io.ErrUnexpectedEOF}
	log.Error("bot error", "err", urlErr)
	log.Error("wrapped", "err", fmt.Errorf("poll: %w", urlErr))
	log.Info("as text", "detail", "token is "+testToken)
	log.Info("nested", slog.Group("g", slog.String("x", testToken)))

	out := buf.String()
	if strings.Contains(out, testToken) {
		t.Fatalf("the token reached the log:\n%s", out)
	}
	if !strings.Contains(out, "bot***/getUpdates") {
		t.Errorf("the error was not kept in a readable, redacted form:\n%s", out)
	}
	if !strings.Contains(out, `"msg":"bot error"`) {
		t.Errorf("log lines are not JSON as before:\n%s", out)
	}
}

func TestRegisterSecretsCoversTokenWebhookSecretAndDatabasePassword(t *testing.T) {
	secret.Reset()
	t.Cleanup(secret.Reset)
	registerSecrets(&config.Config{
		BotToken:      testToken,
		WebhookSecret: "hook-secret-123",
		DatabaseURL:   "postgres://bot:Sup3rS3cretPw@db:5432/bot?sslmode=disable",
	})
	for _, s := range []string{testToken, "hook-secret-123", "Sup3rS3cretPw"} {
		if got := secret.Redact("value " + s + " end"); strings.Contains(got, s) {
			t.Errorf("%q is not hidden: %q", s, got)
		}
	}
	// the user name of the database is not a secret and stays readable
	if got := secret.Redact("role bot"); got != "role bot" {
		t.Errorf("over-redaction: %q", got)
	}
}

func TestJobRestartsAfterAPanic(t *testing.T) {
	var buf bytes.Buffer
	j := newJobs(slog.New(slog.NewTextHandler(&buf, nil)))
	j.delay = time.Millisecond

	var runs atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	j.Go(ctx, "flaky", func(ctx context.Context) {
		if runs.Add(1) < 3 {
			panic("boom")
		}
		<-ctx.Done() // the third run is healthy and waits for shutdown
	})

	deadline := time.Now().Add(2 * time.Second)
	for runs.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if runs.Load() != 3 {
		t.Fatalf("job ran %d times, want it restarted until healthy", runs.Load())
	}
	cancel()
	waited := make(chan struct{})
	go func() { j.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after the context was cancelled")
	}
	if strings.Count(buf.String(), "background job panicked") != 2 {
		t.Errorf("panics were not logged:\n%s", buf.String())
	}
}

func TestJobThatReturnsIsNotRestarted(t *testing.T) {
	j := newJobs(slog.New(slog.NewTextHandler(io.Discard, nil)))
	j.delay = time.Millisecond
	var runs atomic.Int32
	j.Go(context.Background(), "once", func(context.Context) { runs.Add(1) })
	j.Wait()
	time.Sleep(20 * time.Millisecond)
	if runs.Load() != 1 {
		t.Errorf("a job that finished normally ran %d times", runs.Load())
	}
}

func TestHealthcheckOffAndUnreachable(t *testing.T) {
	t.Setenv("METRICS_LISTEN", "off")
	if code := healthcheck(); code != 0 {
		t.Errorf("disabled health server: exit code %d, want 0", code)
	}
	t.Setenv("METRICS_LISTEN", "127.0.0.1:1") // nothing listens there
	if code := healthcheck(); code != 1 {
		t.Errorf("nothing listening: exit code %d, want 1", code)
	}
}
