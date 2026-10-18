package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/secret"
	"chat_bot/internal/service"
	"chat_bot/internal/storage/db"
	"chat_bot/internal/texts"
)

// maxUploadBytes bounds an uploaded promo file (100k codes fit comfortably).
const maxUploadBytes = 4 << 20

// maxMessageRunes keeps messages under Telegram's 4096-character limit.
const maxMessageRunes = 3800

// Backend is what the handlers need from the admin service.
type Backend interface {
	Audit(ctx context.Context, adminID int64, action string, details map[string]any) error
	AddPartner(ctx context.Context, adminID int64, in PartnerInput) (db.Partner, error)
	ListPartners(ctx context.Context, adminID int64) ([]db.Partner, error)
	EditPartner(ctx context.Context, adminID int64, e PartnerEdit) (PartnerEditResult, error)
	SetDay(ctx context.Context, adminID int64, in DayInput) (DaySetResult, error)
	UploadPromo(ctx context.Context, adminID, partnerID int64, parsed *PromoParse) (PromoReport, error)
	PromoStats(ctx context.Context, adminID int64) ([]db.ListPromoStatsRow, error)
	Export(ctx context.Context, adminID int64, req ExportRequest) (*ExportFile, error)
	RunDraw(ctx context.Context, adminID int64, date time.Time, force bool) (service.DrawResult, error)
	ConfirmDraw(ctx context.Context, adminID int64, date time.Time) (service.ConfirmResult, error)
	RunFinal(ctx context.Context, adminID int64, force bool) (service.FinalResult, error)
	ConfirmFinal(ctx context.Context, adminID int64) (service.FinalConfirmResult, error)
	Winners(ctx context.Context, adminID int64, req WinnersRequest) (texts.AdminWinnersView, error)
	PurgeContacts(ctx context.Context, adminID int64, confirm bool) (service.PurgeResult, error)
	DraftBroadcast(ctx context.Context, adminID int64, segment, text string) (service.BroadcastInfo, error)
	ConfirmBroadcast(ctx context.Context, adminID, id int64) (service.BroadcastInfo, error)
	CancelBroadcast(ctx context.Context, adminID, id int64) (service.BroadcastReport, error)
	BroadcastReport(ctx context.Context, adminID, id int64) (service.BroadcastReport, error)
	Stats(ctx context.Context, adminID int64, csv bool) (service.StatsReport, error)
}

// Deps are the collaborators of the admin handlers.
type Deps struct {
	Log      *slog.Logger
	Admins   []int64
	Backend  Backend
	Location *time.Location
}

type handlers struct {
	Deps
	// open downloads an uploaded document; replaced in tests.
	open func(c tele.Context, doc *tele.Document) (io.ReadCloser, error)
}

// Register installs the admin commands on the bot (see Binder). Every handler sits behind
// Guard, so only ADMIN_IDS can reach it. Call it after the global middleware
// has been installed with bot.Use.
// Binder registers routes; *tele.Bot and the instrumented router of package obs
// satisfy it. Register takes the interface so that tests can see every route.
type Binder interface {
	Handle(endpoint interface{}, h tele.HandlerFunc, m ...tele.MiddlewareFunc)
}

func Register(b Binder, deps Deps) {
	h := &handlers{Deps: deps, open: func(c tele.Context, doc *tele.Document) (io.ReadCloser, error) {
		return c.Bot().File(&doc.File)
	}}
	guard := Guard(deps.Admins, deps.Log)

	b.Handle("/admin", h.menu, guard)
	b.Handle("/partner_add", h.partnerAdd, guard)
	b.Handle("/partner_edit", h.partnerEdit, guard)
	b.Handle("/partner_list", h.partnerList, guard)
	b.Handle("/day_set", h.daySet, guard)
	b.Handle("/promo_upload", h.promoUploadHint, guard)
	b.Handle(tele.OnDocument, h.promoUpload, guard)
	b.Handle("/promo_stats", h.promoStats, guard)
	b.Handle("/export", h.export, guard)
	b.Handle("/draw_run", h.drawRun, guard)
	b.Handle("/draw_confirm", h.drawConfirm, guard)
	b.Handle("/final_run", h.finalRun, guard)
	b.Handle("/final_confirm", h.finalConfirm, guard)
	b.Handle("/winners", h.winners, guard)
	b.Handle("/purge_contacts", h.purgeContacts, guard)
	b.Handle("/broadcast", h.broadcast, guard)
	b.Handle("/stats", h.stats, guard)
	b.Handle("/broadcast_status", h.broadcastStatus, guard)
	b.Handle("/broadcast_cancel", h.broadcastCancelCommand, guard)
	send := (&tele.ReplyMarkup{}).Data(texts.BtnBroadcastSend, uniqueBroadcastSend)
	cancel := (&tele.ReplyMarkup{}).Data(texts.BtnBroadcastCancel, uniqueBroadcastCancel)
	b.Handle(&send, h.broadcastSend, guard)
	b.Handle(&cancel, h.broadcastCancelButton, guard)
}

func adminID(c tele.Context) int64 { return c.Sender().ID }

func ctxOf(c tele.Context) context.Context {
	if ctx, ok := c.Get("ctx").(context.Context); ok {
		return ctx
	}
	return context.Background()
}

// fail tells the admin what went wrong. Input problems carry their own
// explanation; anything else is logged and shown with its cause.
func (h *handlers) fail(c tele.Context, action string, err error) error {
	var in *InputError
	if errors.As(err, &in) {
		return c.Send(texts.AdminError(in.Msg), tele.ModeHTML)
	}
	h.Log.Error("admin action failed", "action", action, "admin_id", adminID(c), "err", err)
	if sendErr := c.Send(texts.AdminInternalError(secret.Wrap(err)), tele.ModeHTML); sendErr != nil {
		return sendErr
	}
	return nil
}

func (h *handlers) menu(c tele.Context) error {
	if err := h.Backend.Audit(ctxOf(c), adminID(c), ActionMenu, nil); err != nil {
		return h.fail(c, ActionMenu, err)
	}
	return c.Send(texts.AdminMenu(), tele.ModeHTML)
}

func (h *handlers) partnerAdd(c tele.Context) error {
	payload := c.Message().Payload
	if strings.TrimSpace(payload) == "" {
		return c.Send(texts.AdminUsage(texts.UsagePartnerAdd, texts.ExamplePartnerAdd), tele.ModeHTML)
	}
	in, err := parsePartnerArgs(payload, h.Location)
	if err != nil {
		return h.fail(c, ActionPartnerAdd, err)
	}
	p, err := h.Backend.AddPartner(ctxOf(c), adminID(c), in)
	if err != nil {
		return h.fail(c, ActionPartnerAdd, err)
	}
	return c.Send(texts.AdminPartnerAdded(p.ID, p.Name), tele.ModeHTML)
}

func (h *handlers) partnerEdit(c tele.Context) error {
	payload := c.Message().Payload
	if strings.TrimSpace(payload) == "" {
		return c.Send(texts.AdminUsage(texts.UsagePartnerEdit, texts.ExamplePartnerEdit), tele.ModeHTML)
	}
	e, err := parseEditArgs(payload, h.Location)
	if err != nil {
		return h.fail(c, ActionPartnerEdit, err)
	}
	res, err := h.Backend.EditPartner(ctxOf(c), adminID(c), e)
	if err != nil {
		return h.fail(c, ActionPartnerEdit, err)
	}
	if !res.Changed {
		return c.Send(texts.AdminPartnerNoChange(e.ID, e.Field, res.New), tele.ModeHTML)
	}
	return c.Send(texts.AdminPartnerEdited(res.Partner.ID, res.Partner.Name, e.Field, res.Old, res.New), tele.ModeHTML)
}

func (h *handlers) partnerList(c tele.Context) error {
	ps, err := h.Backend.ListPartners(ctxOf(c), adminID(c))
	if err != nil {
		return h.fail(c, ActionPartnerList, err)
	}
	lines := make([]texts.AdminPartnerLine, len(ps))
	for i, p := range ps {
		l := texts.AdminPartnerLine{ID: p.ID, Name: p.Name, URL: p.Url, Terms: p.PromoTerms}
		if p.ChatID.Valid {
			id := p.ChatID.Int64
			l.ChatID = &id
		}
		if p.PromoValidUntil.Valid {
			t := p.PromoValidUntil.Time
			l.ValidUntil = &t
		}
		lines[i] = l
	}
	return sendLong(c, texts.AdminPartnerList(lines, h.Location))
}

func (h *handlers) daySet(c tele.Context) error {
	payload := c.Message().Payload
	if strings.TrimSpace(payload) == "" {
		return c.Send(texts.AdminUsage(texts.UsageDaySet, texts.ExampleDaySet), tele.ModeHTML)
	}
	in, err := parseDayArgs(payload)
	if err != nil {
		return h.fail(c, ActionDaySet, err)
	}
	res, err := h.Backend.SetDay(ctxOf(c), adminID(c), in)
	if err != nil {
		return h.fail(c, ActionDaySet, err)
	}
	return c.Send(texts.AdminDaySet(res.Created, res.Day.ID, in.Day, res.Partner.Name, in.Winners, in.Prizes), tele.ModeHTML)
}

// promoUploadHint answers a bare /promo_upload: the file must come with the command as its caption.
func (h *handlers) promoUploadHint(c tele.Context) error {
	return c.Send(texts.AdminPromoNeedCaption(""), tele.ModeHTML)
}

// promoUpload handles a document sent with the caption "/promo_upload <partner id>".
func (h *handlers) promoUpload(c tele.Context) error {
	msg := c.Message()
	partnerID, ok := parseUploadCaption(msg.Caption)
	if !ok || msg.Document == nil {
		return c.Send(texts.AdminPromoNeedCaption(msg.Caption), tele.ModeHTML)
	}
	if msg.Document.FileSize > maxUploadBytes {
		return h.fail(c, ActionPromoUpload, inputErr(texts.AdminPromoFileTooBig(msg.Document.FileSize, maxUploadBytes)))
	}

	rc, err := h.open(c, msg.Document)
	if err != nil {
		return h.fail(c, ActionPromoUpload, fmt.Errorf("download file: %w", err))
	}
	defer rc.Close()
	parsed, err := ParsePromoCSV(io.LimitReader(rc, maxUploadBytes+1))
	if err != nil {
		return h.fail(c, ActionPromoUpload, err)
	}

	rep, err := h.Backend.UploadPromo(ctxOf(c), adminID(c), partnerID, parsed)
	if err != nil {
		return h.fail(c, ActionPromoUpload, err)
	}
	return sendLong(c, texts.AdminPromoUploaded(promoReportText(rep)))
}

func promoReportText(r PromoReport) texts.AdminPromoReport {
	out := texts.AdminPromoReport{
		PartnerID: r.Partner.ID, PartnerName: r.Partner.Name, Rows: r.Rows, Loaded: r.Loaded,
		AlreadyInDB: r.AlreadyInDB, DupInFile: r.DupInFile, HeaderSkipped: r.HeaderSkipped,
	}
	for _, is := range r.Invalid {
		out.Invalid = append(out.Invalid, texts.AdminPromoIssue{Line: is.Line, Value: is.Value, Reason: is.Reason})
	}
	return out
}

func (h *handlers) promoStats(c tele.Context) error {
	rows, err := h.Backend.PromoStats(ctxOf(c), adminID(c))
	if err != nil {
		return h.fail(c, ActionPromoStats, err)
	}
	lines := make([]texts.AdminPromoStatsLine, len(rows))
	for i, r := range rows {
		lines[i] = texts.AdminPromoStatsLine{ID: r.ID, Name: r.Name, Free: r.Free, Total: r.Total}
	}
	return sendLong(c, texts.AdminPromoStats(lines))
}

func (h *handlers) export(c tele.Context) error {
	req, err := parseExportArgs(c.Message().Payload)
	if err != nil {
		return h.fail(c, ActionExport, err)
	}
	file, err := h.Backend.Export(ctxOf(c), adminID(c), req)
	if err != nil {
		return h.fail(c, ActionExport, err)
	}
	doc := &tele.Document{
		File:     tele.FromReader(strings.NewReader(string(file.Data))),
		FileName: file.Name,
		Caption:  texts.AdminExportDone(string(req.Kind), file.Rows),
	}
	return c.Send(doc)
}

// sendLong sends text in several messages when it exceeds Telegram's limit,
// splitting between lines.
func sendLong(c tele.Context, text string) error {
	for _, part := range splitMessage(text, maxMessageRunes) {
		if err := c.Send(part, tele.ModeHTML); err != nil {
			return err
		}
	}
	return nil
}

func splitMessage(text string, limit int) []string {
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}
	var parts []string
	var cur strings.Builder
	n := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		l := utf8.RuneCountInString(line)
		if n+l > limit && n > 0 {
			parts = append(parts, cur.String())
			cur.Reset()
			n = 0
		}
		cur.WriteString(line)
		n += l
	}
	if n > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

func (h *handlers) drawRun(c tele.Context) error {
	if strings.TrimSpace(c.Message().Payload) == "" {
		return c.Send(texts.AdminUsage(texts.UsageDrawRun, "/draw_run 2026-09-05"), tele.ModeHTML)
	}
	date, force, err := parseDrawArgs(c.Message().Payload, "/draw_run", texts.UsageDrawRun, true)
	if err != nil {
		return h.fail(c, "draw_run", err)
	}
	res, err := h.Backend.RunDraw(ctxOf(c), adminID(c), date, force)
	if err != nil {
		return h.fail(c, "draw_run", err)
	}
	return sendLong(c, texts.AdminDrawPreview(drawView(res), h.Location))
}

func (h *handlers) drawConfirm(c tele.Context) error {
	if strings.TrimSpace(c.Message().Payload) == "" {
		return c.Send(texts.AdminUsage(texts.UsageDrawConfirm, "/draw_confirm 2026-09-05"), tele.ModeHTML)
	}
	date, _, err := parseDrawArgs(c.Message().Payload, "/draw_confirm", texts.UsageDrawConfirm, false)
	if err != nil {
		return h.fail(c, "draw_confirm", err)
	}
	res, err := h.Backend.ConfirmDraw(ctxOf(c), adminID(c), date)
	if err != nil {
		return h.fail(c, "draw_confirm", err)
	}
	failed := make([]texts.AdminDrawFailure, len(res.Failed))
	for i, f := range res.Failed {
		failed[i] = texts.AdminDrawFailure{TelegramID: f.TelegramID, Reason: f.Err.Error()}
	}
	return sendLong(c, texts.AdminDrawConfirmed(date, res.Partner.Name, res.Winners, res.Sent, res.Already, failed))
}

func (h *handlers) finalRun(c tele.Context) error {
	force, err := parseFinalArgs(c.Message().Payload)
	if err != nil {
		return h.fail(c, "final_run", err)
	}
	res, err := h.Backend.RunFinal(ctxOf(c), adminID(c), force)
	if err != nil {
		return h.fail(c, "final_run", err)
	}
	return sendLong(c, texts.AdminFinalPreview(finalView(res, h.Location), h.Location))
}

func (h *handlers) finalConfirm(c tele.Context) error {
	res, err := h.Backend.ConfirmFinal(ctxOf(c), adminID(c))
	if err != nil {
		return h.fail(c, "final_confirm", err)
	}
	failed := make([]texts.AdminDrawFailure, len(res.Failed))
	for i, f := range res.Failed {
		failed[i] = texts.AdminDrawFailure{TelegramID: f.TelegramID, Reason: f.Err.Error()}
	}
	return sendLong(c, texts.AdminFinalConfirmed(res.Winners, res.Sent, res.Already, failed))
}
