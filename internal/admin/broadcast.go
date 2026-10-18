package admin

import (
	"context"
	"errors"
	"strconv"
	"strings"

	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/service"
	"chat_bot/internal/texts"
)

// Broadcaster is the broadcast queue (implemented by *service.Broadcast).
type Broadcaster interface {
	Draft(ctx context.Context, adminID int64, segment, text string) (service.BroadcastInfo, error)
	Confirm(ctx context.Context, adminID, id int64) (service.BroadcastInfo, error)
	Cancel(ctx context.Context, adminID, id int64) (service.BroadcastReport, error)
	Report(ctx context.Context, id int64) (service.BroadcastReport, error)
}

// WithBroadcaster sets the broadcast queue behind /broadcast.
func (s *Service) WithBroadcaster(b Broadcaster) *Service {
	s.bcast = b
	return s
}

// Callback ids of the broadcast confirmation buttons; the data is the broadcast id.
const (
	uniqueBroadcastSend   = "bc_send"
	uniqueBroadcastCancel = "bc_cancel"
)

// ActionBroadcastStatus is the audit action of the status view.
const ActionBroadcastStatus = "broadcast_status"

// DraftBroadcast stores a broadcast awaiting confirmation.
func (s *Service) DraftBroadcast(ctx context.Context, adminID int64, segment, text string) (service.BroadcastInfo, error) {
	info, err := s.bcast.Draft(ctx, adminID, segment, text)
	return info, broadcastError(err, segment, 0)
}

// ConfirmBroadcast starts a drafted broadcast.
func (s *Service) ConfirmBroadcast(ctx context.Context, adminID, id int64) (service.BroadcastInfo, error) {
	info, err := s.bcast.Confirm(ctx, adminID, id)
	return info, broadcastError(err, "", id)
}

// CancelBroadcast cancels a draft or a running broadcast.
func (s *Service) CancelBroadcast(ctx context.Context, adminID, id int64) (service.BroadcastReport, error) {
	rep, err := s.bcast.Cancel(ctx, adminID, id)
	return rep, broadcastError(err, "", id)
}

// BroadcastReport returns the progress of a broadcast (the latest when id is 0).
func (s *Service) BroadcastReport(ctx context.Context, adminID, id int64) (service.BroadcastReport, error) {
	rep, err := s.bcast.Report(ctx, id)
	if err != nil {
		return rep, broadcastError(err, "", id)
	}
	err = s.Audit(ctx, adminID, ActionBroadcastStatus, map[string]any{"broadcast_id": rep.ID})
	return rep, err
}

func broadcastError(err error, segment string, id int64) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, service.ErrBroadcastBadSegment):
		return inputErr(texts.AdminBroadcastBadSegment(segment))
	case errors.Is(err, service.ErrBroadcastBadText):
		return inputErr(texts.AdminBroadcastBadText(service.MaxBroadcastText))
	case errors.Is(err, service.ErrSegmentUnavailable):
		return inputErr(texts.AdminBroadcastSegmentUnavailable(segment, segmentReason(err)))
	case errors.Is(err, service.ErrBroadcastNotFound):
		return inputErr(texts.AdminBroadcastNotFound(id))
	case errors.Is(err, service.ErrBroadcastState):
		detail := err.Error()
		if i := strings.LastIndex(detail, ": "); i >= 0 {
			detail = detail[i+2:]
		}
		return inputErr(texts.AdminBroadcastState(id, detail))
	}
	return err
}

// commandText returns the whole text of a command message after the command
// itself. Unlike Payload it keeps line breaks, which broadcasts need.
func commandText(c tele.Context) string {
	text := strings.TrimSpace(c.Text())
	if strings.HasPrefix(text, "/") {
		if i := strings.IndexAny(text, " \t\r\n"); i >= 0 {
			return strings.TrimSpace(text[i:])
		}
		return ""
	}
	return text
}

// parseBroadcastArgs parses "segment | text".
func parseBroadcastArgs(raw string) (segment, text string, err error) {
	seg, body, found := strings.Cut(raw, "|")
	segment = strings.ToLower(strings.TrimSpace(seg))
	text = strings.TrimSpace(body)
	switch {
	case !found:
		return "", "", inputErr(texts.AdminFieldCount("/broadcast", 1, 2, 2, texts.UsageBroadcast) + "\n\nПример: " + texts.ExampleBroadcast)
	case !service.ValidSegment(segment):
		return "", "", inputErr(texts.AdminBroadcastBadSegment(strings.TrimSpace(seg)))
	case text == "":
		return "", "", inputErr(texts.AdminBroadcastBadText(service.MaxBroadcastText))
	}
	return segment, text, nil
}

func (h *handlers) broadcast(c tele.Context) error {
	raw := commandText(c)
	if raw == "" {
		return c.Send(texts.AdminUsage(texts.UsageBroadcast, texts.ExampleBroadcast), tele.ModeHTML)
	}
	segment, text, err := parseBroadcastArgs(raw)
	if err != nil {
		return h.fail(c, "broadcast", err)
	}
	info, err := h.Backend.DraftBroadcast(ctxOf(c), adminID(c), segment, text)
	if err != nil {
		return h.fail(c, "broadcast", err)
	}

	// The preview goes to the admin only: the text exactly as recipients will get it.
	if err := c.Send(info.Text); err != nil {
		return err
	}
	id := strconv.FormatInt(info.ID, 10)
	m := &tele.ReplyMarkup{}
	m.Inline(m.Row(m.Data(texts.BtnBroadcastSend, uniqueBroadcastSend, id), m.Data(texts.BtnBroadcastCancel, uniqueBroadcastCancel, id)))
	return c.Send(texts.AdminBroadcastDraft(info.ID, info.Segment, info.Audience), tele.ModeHTML, m)
}

func broadcastIDOf(data string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(data), 10, 64)
	return id, err == nil && id > 0
}

// respond acknowledges a button press so the client stops showing a spinner.
func respond(c tele.Context) {
	_ = c.Respond()
}

func (h *handlers) broadcastSend(c tele.Context) error {
	respond(c)
	id, ok := broadcastIDOf(c.Data())
	if !ok {
		return c.Send(texts.AdminError("Кнопка устарела: не удалось определить рассылку."))
	}
	info, err := h.Backend.ConfirmBroadcast(ctxOf(c), adminID(c), id)
	if err != nil {
		return h.fail(c, "broadcast_confirm", err)
	}
	return c.Send(texts.AdminBroadcastStarted(info.ID, info.Audience), tele.ModeHTML)
}

func (h *handlers) broadcastCancelButton(c tele.Context) error {
	respond(c)
	id, ok := broadcastIDOf(c.Data())
	if !ok {
		return c.Send(texts.AdminError("Кнопка устарела: не удалось определить рассылку."))
	}
	return h.cancelBroadcast(c, id)
}

func (h *handlers) broadcastCancelCommand(c tele.Context) error {
	id, ok := broadcastIDOf(c.Message().Payload)
	if !ok {
		return c.Send(texts.AdminUsage(texts.UsageBroadcastCancel, "/broadcast_cancel 3"), tele.ModeHTML)
	}
	return h.cancelBroadcast(c, id)
}

func (h *handlers) cancelBroadcast(c tele.Context, id int64) error {
	rep, err := h.Backend.CancelBroadcast(ctxOf(c), adminID(c), id)
	if err != nil {
		return h.fail(c, "broadcast_cancel", err)
	}
	return c.Send(texts.AdminBroadcastCancelled(rep.ID, rep.Sent, rep.Pending), tele.ModeHTML)
}

func (h *handlers) broadcastStatus(c tele.Context) error {
	var id int64
	if payload := strings.TrimSpace(c.Message().Payload); payload != "" {
		var ok bool
		if id, ok = broadcastIDOf(payload); !ok {
			return h.fail(c, ActionBroadcastStatus, inputErr(texts.AdminBadNumber("id рассылки", payload)))
		}
	}
	rep, err := h.Backend.BroadcastReport(ctxOf(c), adminID(c), id)
	if err != nil {
		return h.fail(c, ActionBroadcastStatus, err)
	}
	return c.Send(texts.BroadcastReport(texts.BroadcastReportData{
		ID: rep.ID, Status: rep.Status, Segment: rep.Segment, Total: rep.Total, Pending: rep.Pending,
		Sent: rep.Sent, Failed: rep.Failed, Blocked: rep.Blocked, StartedAt: rep.StartedAt, FinishedAt: rep.Finished,
	}), tele.ModeHTML)
}

// segmentReason says in Russian why a daily segment has no day to refer to.
func segmentReason(err error) string {
	switch {
	case errors.Is(err, service.ErrCampaignNotStarted):
		return "акция ещё не началась"
	case errors.Is(err, service.ErrCampaignFinished):
		return "акция уже завершилась"
	case errors.Is(err, service.ErrNoCampaignDay):
		return "на сегодня не назначен партнёр"
	}
	return "день акции не определён"
}
