package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"chat_bot/internal/storage/db"
	"chat_bot/internal/texts"
)

// Broadcast segments.
const (
	SegmentAll     = "all"      // everyone who has not blocked the bot
	SegmentNoPromo = "no_promo" // has not taken today's promo code
	SegmentNoEntry = "no_entry" // has not registered in today's draw
)

// MaxBroadcastText is Telegram's message length limit.
const MaxBroadcastText = 4096

var (
	// ErrRecipientBlocked means the user cannot be reached: they blocked the
	// bot or their account is gone. Senders wrap it.
	ErrRecipientBlocked = errors.New("recipient blocked the bot")
	// ErrBroadcastNotFound means no broadcast has that id.
	ErrBroadcastNotFound = errors.New("broadcast not found")
	// ErrBroadcastBadSegment means the segment name is unknown.
	ErrBroadcastBadSegment = errors.New("unknown broadcast segment")
	// ErrBroadcastBadText means the text is empty or too long.
	ErrBroadcastBadText = errors.New("broadcast text is empty or too long")
	// ErrSegmentUnavailable means a daily segment was requested while no
	// campaign day is active, so "today" has no meaning.
	ErrSegmentUnavailable = errors.New("segment needs an active campaign day")
	// ErrBroadcastState means the broadcast is not in the state the action needs.
	ErrBroadcastState = errors.New("broadcast is in the wrong state")
)

// RateLimitError is returned by a sender when Telegram answers 429.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited, retry after %s", e.RetryAfter)
}

// BroadcastSender delivers a plain text message. Implementations return an
// error wrapping ErrRecipientBlocked for unreachable users and a
// *RateLimitError when Telegram asks to slow down.
type BroadcastSender interface {
	SendText(ctx context.Context, telegramID int64, text string) error
}

// BroadcastConfig tunes the worker.
type BroadcastConfig struct {
	RatePerSecond int // messages per second; Telegram allows about 30
	MaxAttempts   int // tries per recipient for ordinary errors
}

// Broadcast is the push notification queue (spec 7): admins draft a broadcast,
// confirm it, and a rate-limited worker delivers it to the recipients stored in
// the database. State lives in broadcasts and broadcast_recipients, so a
// restart continues where it stopped.
type Broadcast struct {
	tx       TxRunner
	sender   BroadcastSender
	reporter Sender
	camp     *Campaign
	cfg      BroadcastConfig
	log      *slog.Logger

	// OnResult, if set, is called with the outcome of every delivery attempt:
	// sent, blocked, failed (gave up), retry (will be tried again) or rate_limited.
	OnResult func(result string)

	// pacing: next is the earliest time the next message may go out.
	interval time.Duration
	next     time.Time
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) error
}

// NewBroadcast builds the service. reporter delivers the final report to the
// admin who started the broadcast.
func NewBroadcast(tx TxRunner, sender BroadcastSender, reporter Sender, camp *Campaign, cfg BroadcastConfig, log *slog.Logger) *Broadcast {
	if cfg.RatePerSecond <= 0 {
		cfg.RatePerSecond = 25
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	return &Broadcast{
		tx: tx, sender: sender, reporter: reporter, camp: camp, cfg: cfg, log: log,
		interval: time.Second / time.Duration(cfg.RatePerSecond), now: time.Now, sleep: sleepCtx,
	}
}

func (b *Broadcast) report(result string) {
	if b.OnResult != nil {
		b.OnResult(result)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// BroadcastInfo describes a broadcast and who it reaches.
type BroadcastInfo struct {
	ID       int64
	Status   string
	Segment  string
	Text     string
	Audience int // users the segment reaches now (draft) or recipients queued (confirmed)
}

// BroadcastReport is the progress or final outcome of a broadcast.
type BroadcastReport struct {
	ID        int64
	Status    string
	Segment   string
	Total     int
	Pending   int // waiting or in flight
	Sent      int
	Failed    int
	Blocked   int
	StartedAt time.Time
	Finished  time.Time
	CreatedBy int64
}

// ValidSegment reports whether name is a segment.
func ValidSegment(name string) bool {
	switch name {
	case SegmentAll, SegmentNoPromo, SegmentNoEntry:
		return true
	}
	return false
}

// dayFor returns the id of today's campaign day for daily segments.
func (b *Broadcast) dayFor(ctx context.Context, segment string) (pgtype.Int8, error) {
	if segment == SegmentAll {
		return pgtype.Int8{}, nil
	}
	ad, err := b.camp.Active(ctx)
	if err != nil {
		if errors.Is(err, ErrCampaignNotStarted) || errors.Is(err, ErrCampaignFinished) || errors.Is(err, ErrNoCampaignDay) {
			return pgtype.Int8{}, fmt.Errorf("%w: %w", ErrSegmentUnavailable, err)
		}
		return pgtype.Int8{}, fmt.Errorf("resolve active day: %w", err)
	}
	return pgtype.Int8{Int64: ad.Day.ID, Valid: true}, nil
}

// Draft stores a broadcast awaiting confirmation and counts its audience.
// Nothing is sent to anyone.
func (b *Broadcast) Draft(ctx context.Context, adminID int64, segment, text string) (BroadcastInfo, error) {
	if !ValidSegment(segment) {
		return BroadcastInfo{}, ErrBroadcastBadSegment
	}
	text = strings.TrimSpace(text)
	if n := utf8.RuneCountInString(text); n == 0 || n > MaxBroadcastText {
		return BroadcastInfo{}, ErrBroadcastBadText
	}
	day, err := b.dayFor(ctx, segment)
	if err != nil {
		return BroadcastInfo{}, err
	}

	var info BroadcastInfo
	err = b.tx.InTx(ctx, func(q *db.Queries) error {
		n, err := q.CountSegment(ctx, db.CountSegmentParams{Segment: segment, DayID: day.Int64})
		if err != nil {
			return fmt.Errorf("count segment: %w", err)
		}
		row, err := q.CreateBroadcast(ctx, db.CreateBroadcastParams{
			Text: text, CreatedByTelegramID: adminID, Segment: segment, CampaignDayID: day,
		})
		if err != nil {
			return fmt.Errorf("create broadcast: %w", err)
		}
		info = BroadcastInfo{ID: row.ID, Status: row.Status, Segment: segment, Text: text, Audience: int(n)}
		return auditLog(ctx, q, adminID, "broadcast_draft", "broadcast", row.ID, map[string]any{"segment": segment, "audience": n})
	})
	return info, err
}

// Confirm queues a draft: the recipients of its segment are fixed now, users
// who blocked the bot are left out, and the worker starts sending. A second
// confirmation fails with ErrBroadcastState, so double clicks cannot send twice.
func (b *Broadcast) Confirm(ctx context.Context, adminID, id int64) (BroadcastInfo, error) {
	var info BroadcastInfo
	err := b.tx.InTx(ctx, func(q *db.Queries) error {
		row, err := q.LockBroadcast(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBroadcastNotFound
		}
		if err != nil {
			return fmt.Errorf("lock broadcast: %w", err)
		}
		if row.Status != "draft" {
			info = BroadcastInfo{ID: row.ID, Status: row.Status}
			return fmt.Errorf("%w: %s", ErrBroadcastState, row.Status)
		}
		if row, err = q.StartBroadcast(ctx, id); err != nil {
			return fmt.Errorf("start broadcast: %w", err)
		}
		n, err := q.AddSegmentRecipients(ctx, db.AddSegmentRecipientsParams{Bid: id, Segment: row.Segment, DayID: row.CampaignDayID.Int64})
		if err != nil {
			return fmt.Errorf("add recipients: %w", err)
		}
		info = BroadcastInfo{ID: id, Status: row.Status, Segment: row.Segment, Text: row.Text, Audience: int(n)}
		return auditLog(ctx, q, adminID, "broadcast_confirm", "broadcast", id, map[string]any{"segment": row.Segment, "recipients": n})
	})
	return info, err
}

// Cancel stops a draft or a running broadcast; messages not yet sent are never sent.
func (b *Broadcast) Cancel(ctx context.Context, adminID, id int64) (BroadcastReport, error) {
	var rep BroadcastReport
	err := b.tx.InTx(ctx, func(q *db.Queries) error {
		_, err := q.LockBroadcast(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBroadcastNotFound
		}
		if err != nil {
			return fmt.Errorf("lock broadcast: %w", err)
		}
		row, err := q.CancelBroadcast(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: already %s", ErrBroadcastState, lockedStatus(ctx, q, id))
		}
		if err != nil {
			return fmt.Errorf("cancel broadcast: %w", err)
		}
		if rep, err = reportOf(ctx, q, row); err != nil {
			return err
		}
		return auditLog(ctx, q, adminID, "broadcast_cancel", "broadcast", id, map[string]any{"sent": rep.Sent, "pending": rep.Pending})
	})
	return rep, err
}

func lockedStatus(ctx context.Context, q *db.Queries, id int64) string {
	row, err := q.GetBroadcast(ctx, id)
	if err != nil {
		return "finished"
	}
	return row.Status
}

// Report returns the progress of broadcast id, or of the latest one when id is 0.
func (b *Broadcast) Report(ctx context.Context, id int64) (BroadcastReport, error) {
	var rep BroadcastReport
	err := b.tx.InTx(ctx, func(q *db.Queries) error {
		var (
			row db.Broadcast
			err error
		)
		if id == 0 {
			row, err = q.GetLatestBroadcast(ctx)
		} else {
			row, err = q.GetBroadcast(ctx, id)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBroadcastNotFound
		}
		if err != nil {
			return fmt.Errorf("get broadcast: %w", err)
		}
		rep, err = reportOf(ctx, q, row)
		return err
	})
	return rep, err
}

func reportOf(ctx context.Context, q *db.Queries, row db.Broadcast) (BroadcastReport, error) {
	st, err := q.BroadcastStats(ctx, row.ID)
	if err != nil {
		return BroadcastReport{}, fmt.Errorf("broadcast stats: %w", err)
	}
	return BroadcastReport{
		ID: row.ID, Status: row.Status, Segment: row.Segment, Total: int(st.Total), Pending: int(st.Pending + st.InFlight),
		Sent: int(st.Sent), Failed: int(st.Failed), Blocked: int(st.Blocked),
		StartedAt: row.StartedAt.Time, Finished: row.FinishedAt.Time, CreatedBy: row.CreatedByTelegramID,
	}, nil
}

// Run processes running broadcasts until ctx is done.
func (b *Broadcast) Run(ctx context.Context) {
	for ctx.Err() == nil {
		n, err := b.Tick(ctx)
		if err != nil && ctx.Err() == nil {
			b.log.Error("broadcast worker", "err", err)
		}
		if n == 0 { // idle (or failing): look again in a second
			_ = b.sleep(ctx, time.Second)
		}
	}
}

// Tick sends one batch of every running broadcast and finishes those that are
// complete. It returns how many recipients it handled.
func (b *Broadcast) Tick(ctx context.Context) (int, error) {
	var running []db.Broadcast
	if err := b.tx.InTx(ctx, func(q *db.Queries) (err error) {
		running, err = q.ListSendingBroadcasts(ctx)
		return err
	}); err != nil {
		return 0, fmt.Errorf("list running broadcasts: %w", err)
	}

	handled := 0
	for _, bc := range running {
		n, err := b.process(ctx, bc)
		handled += n
		if err != nil {
			return handled, err
		}
	}
	return handled, nil
}

const claimBatch = 25

func (b *Broadcast) process(ctx context.Context, bc db.Broadcast) (int, error) {
	var batch []db.BroadcastRecipient
	if err := b.tx.InTx(ctx, func(q *db.Queries) error {
		if _, err := q.ReleaseStaleRecipients(ctx, bc.ID); err != nil {
			return fmt.Errorf("release stale recipients: %w", err)
		}
		var err error
		batch, err = q.ClaimRecipients(ctx, db.ClaimRecipientsParams{Bid: bc.ID, Batch: claimBatch})
		return err
	}); err != nil {
		return 0, fmt.Errorf("claim recipients: %w", err)
	}

	for i, r := range batch {
		// A cancellation takes effect between messages: give the rest back.
		if cancelled, err := b.isCancelled(ctx, bc.ID); err != nil {
			return i, err
		} else if cancelled {
			return i, b.release(ctx, batch[i:])
		}
		if err := b.deliver(ctx, bc, r); err != nil {
			rest := batch[i:]
			if relErr := b.release(context.WithoutCancel(ctx), rest); relErr != nil {
				b.log.Error("release claimed recipients", "err", relErr)
			}
			return i, err
		}
	}
	if len(batch) > 0 {
		return len(batch), nil
	}
	return 0, b.finishIfDone(ctx, bc)
}

func (b *Broadcast) isCancelled(ctx context.Context, id int64) (bool, error) {
	var status string
	err := b.tx.InTx(ctx, func(q *db.Queries) error {
		row, err := q.GetBroadcast(ctx, id)
		status = row.Status
		return err
	})
	if err != nil {
		return false, fmt.Errorf("get broadcast: %w", err)
	}
	return status != "sending", nil
}

// release returns claimed, unsent recipients to the queue without using up an attempt.
func (b *Broadcast) release(ctx context.Context, rs []db.BroadcastRecipient) error {
	return b.tx.InTx(ctx, func(q *db.Queries) error {
		for _, r := range rs {
			if err := q.RetryRecipient(ctx, db.RetryRecipientParams{ID: r.ID, Err: "", GiveBack: 1}); err != nil {
				return fmt.Errorf("release recipient: %w", err)
			}
		}
		return nil
	})
}

// pace blocks until the next message may be sent, keeping the rate at or
// below RatePerSecond.
func (b *Broadcast) pace(ctx context.Context) error {
	now := b.now()
	if wait := b.next.Sub(now); wait > 0 {
		if err := b.sleep(ctx, wait); err != nil {
			return err
		}
		now = b.next
	}
	b.next = now.Add(b.interval)
	return nil
}

const maxRateLimitRetries = 5

// deliver sends the broadcast to one recipient and records the outcome. Only
// a cancelled context makes it return an error; Telegram's errors are
// outcomes of the recipient.
func (b *Broadcast) deliver(ctx context.Context, bc db.Broadcast, r db.BroadcastRecipient) error {
	var err error
	for retry := 0; ; retry++ {
		if err := b.pace(ctx); err != nil {
			return err
		}
		err = b.sender.SendText(ctx, r.TelegramID, bc.Text)
		var rl *RateLimitError
		if !errors.As(err, &rl) {
			break
		}
		b.log.Warn("broadcast rate limited", "retry_after", rl.RetryAfter, "broadcast_id", bc.ID)
		b.report("rate_limited")
		if retry == maxRateLimitRetries {
			// Telegram keeps refusing: back off and let the next tick try again.
			if relErr := b.release(ctx, []db.BroadcastRecipient{r}); relErr != nil {
				return relErr
			}
			return b.sleep(ctx, rl.RetryAfter)
		}
		if err := b.sleep(ctx, rl.RetryAfter+250*time.Millisecond); err != nil {
			return err
		}
	}

	// The message went out (or was refused): record that even if the worker is
	// being stopped, otherwise the recipient would get it again after a restart.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return b.tx.InTx(ctx, func(q *db.Queries) error {
		switch {
		case err == nil:
			b.report("sent")
			return q.MarkRecipientSent(ctx, r.ID)
		case errors.Is(err, ErrRecipientBlocked):
			b.report("blocked")
			if e := q.SetUserBlocked(ctx, db.SetUserBlockedParams{TelegramID: r.TelegramID, IsBlocked: true}); e != nil {
				return fmt.Errorf("mark user blocked: %w", e)
			}
			return q.MarkRecipientBlocked(ctx, db.MarkRecipientBlockedParams{ID: r.ID, Err: err.Error()})
		case int(r.Attempts) < b.cfg.MaxAttempts:
			b.report("retry")
			b.log.Warn("broadcast send failed, will retry", "telegram_id", r.TelegramID, "attempt", r.Attempts, "err", err)
			return q.RetryRecipient(ctx, db.RetryRecipientParams{ID: r.ID, Err: err.Error()})
		default:
			b.report("failed")
			b.log.Error("broadcast send failed", "telegram_id", r.TelegramID, "attempts", r.Attempts, "err", err)
			return q.MarkRecipientFailed(ctx, db.MarkRecipientFailedParams{ID: r.ID, Err: err.Error()})
		}
	})
}

// finishIfDone completes a broadcast without pending recipients and sends the
// report to the admin who started it.
func (b *Broadcast) finishIfDone(ctx context.Context, bc db.Broadcast) error {
	var (
		rep      BroadcastReport
		finished bool
	)
	err := b.tx.InTx(ctx, func(q *db.Queries) error {
		st, err := q.BroadcastStats(ctx, bc.ID)
		if err != nil {
			return fmt.Errorf("broadcast stats: %w", err)
		}
		if st.Pending+st.InFlight > 0 {
			return nil
		}
		row, err := q.FinishBroadcast(ctx, bc.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // finished or cancelled by somebody else
		}
		if err != nil {
			return fmt.Errorf("finish broadcast: %w", err)
		}
		finished = true
		if rep, err = reportOf(ctx, q, row); err != nil {
			return err
		}
		return auditLog(ctx, q, SystemAdminID, "broadcast_done", "broadcast", bc.ID,
			map[string]any{"total": rep.Total, "sent": rep.Sent, "failed": rep.Failed, "blocked": rep.Blocked})
	})
	if err != nil || !finished {
		return err
	}
	b.log.Info("broadcast finished", "broadcast_id", rep.ID, "sent", rep.Sent, "failed", rep.Failed, "blocked", rep.Blocked)
	if err := b.reporter.Send(ctx, rep.CreatedBy, texts.BroadcastReport(texts.BroadcastReportData{
		ID: rep.ID, Status: rep.Status, Segment: rep.Segment, Total: rep.Total, Pending: rep.Pending,
		Sent: rep.Sent, Failed: rep.Failed, Blocked: rep.Blocked, StartedAt: rep.StartedAt, FinishedAt: rep.Finished,
	})); err != nil {
		b.log.Error("send broadcast report", "admin_id", rep.CreatedBy, "err", err)
	}
	return nil
}
