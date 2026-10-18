package admin

import (
	"strings"

	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/texts"
)

func (h *handlers) winners(c tele.Context) error {
	if strings.TrimSpace(c.Message().Payload) == "" {
		return c.Send(texts.AdminUsage(texts.UsageWinners, "/winners final"), tele.ModeHTML)
	}
	req, err := parseWinnersArgs(c.Message().Payload)
	if err != nil {
		return h.fail(c, ActionWinners, err)
	}
	view, err := h.Backend.Winners(ctxOf(c), adminID(c), req)
	if err != nil {
		return h.fail(c, ActionWinners, err)
	}
	if !req.CSV {
		return sendLong(c, texts.AdminWinners(view))
	}
	kind := ExportWinners
	if req.Final {
		kind = ExportFinalists
	}
	file, err := h.Backend.Export(ctxOf(c), adminID(c), ExportRequest{Kind: kind, ID: view.ID, Ref: c.Message().Payload})
	if err != nil {
		return h.fail(c, ActionExport, err)
	}
	return c.Send(&tele.Document{
		File:     tele.FromReader(strings.NewReader(string(file.Data))),
		FileName: file.Name,
		Caption:  texts.AdminExportDone(string(kind), file.Rows),
	})
}

func (h *handlers) purgeContacts(c tele.Context) error {
	confirm, err := parsePurgeArgs(c.Message().Payload)
	if err != nil {
		return h.fail(c, ActionPurgeContacts, err)
	}
	res, err := h.Backend.PurgeContacts(ctxOf(c), adminID(c), confirm)
	if err != nil {
		return h.fail(c, ActionPurgeContacts, err)
	}
	if !confirm {
		return c.Send(texts.AdminPurgeDryRun(res.Stored), tele.ModeHTML)
	}
	return c.Send(texts.AdminPurged(res.Purged), tele.ModeHTML)
}
