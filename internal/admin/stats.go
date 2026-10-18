package admin

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/service"
	"chat_bot/internal/texts"
)

// ActionStats is the audit action of the statistics view and export.
const ActionStats = "stats"

// StatsSource builds the analytics report (implemented by *service.Stats).
type StatsSource interface {
	Report(ctx context.Context) (service.StatsReport, error)
}

// WithStats sets the analytics behind /stats.
func (s *Service) WithStats(src StatsSource) *Service {
	s.stats = src
	return s
}

// Stats returns the analytics report.
func (s *Service) Stats(ctx context.Context, adminID int64, csv bool) (service.StatsReport, error) {
	rep, err := s.stats.Report(ctx)
	if err != nil {
		return rep, err
	}
	err = s.Audit(ctx, adminID, ActionStats, map[string]any{"csv": csv, "users": rep.TotalUsers})
	return rep, err
}

func parseStatsArgs(payload string) (csv bool, err error) {
	fields := strings.Fields(payload)
	switch {
	case len(fields) == 0:
		return false, nil
	case len(fields) == 1 && strings.EqualFold(fields[0], "csv"):
		return true, nil
	}
	return false, inputErr(texts.AdminFieldCount("/stats", len(fields), 0, 1, texts.UsageStats) + "\nДопустим только параметр csv.")
}

func statsView(r service.StatsReport) texts.StatsView {
	v := texts.StatsView{
		GeneratedAt: r.GeneratedAt, TotalUsers: r.TotalUsers, BlockedUsers: r.BlockedUsers,
		PromoRequests: r.PromoRequests, Requesters: r.PromoRequesters, GotCode: r.PromoGotCode, NoCode: r.PromoNoCode,
		PromoIssued: r.PromoIssued, MaxChances: r.MaxChances, FullChances: r.FullChances,
	}
	for _, d := range r.NewUsers {
		v.NewUsers = append(v.NewUsers, texts.StatsDay{Date: d.Day, Count: d.Count})
	}
	for _, p := range r.Partners {
		v.Partners = append(v.Partners, texts.StatsPartner{Name: p.Name, Free: p.Free, Total: p.Total, Issued: p.Issued})
	}
	for _, d := range r.Days {
		v.Days = append(v.Days, texts.StatsDraw{
			Date: d.Day, Partner: d.Partner, Entries: d.Entries, Excluded: d.Excluded,
			Requesters: d.PromoRequesters, Issued: d.PromoIssued, Status: d.Status,
		})
	}
	for _, k := range r.Distribution {
		v.Distribution = append(v.Distribution, texts.StatsBucket{Chances: k.Chances, Users: k.Users})
	}
	return v
}

// StatsCSV renders the whole report as one long-format CSV (UTF-8 with BOM):
// columns section, key, label, value, extra. Long format keeps every metric in
// one file and loads straight into a pivot table. Sections: summary,
// new_users, partner_stock, draw, chances.
func StatsCSV(r service.StatsReport, loc *time.Location) (*ExportFile, error) {
	w := newCSV("section", "key", "label", "value", "extra")
	i := strconv.Itoa
	w.row("summary", "generated_at", "время формирования", r.GeneratedAt.In(loc).Format("2006-01-02 15:04:05"), "")
	w.row("summary", "total_users", "запустили бот", i(r.TotalUsers), "")
	w.row("summary", "blocked_users", "заблокировали бота", i(r.BlockedUsers), "")
	w.row("summary", "promo_requests", "запросов промокода (нажатий)", i(r.PromoRequests), "")
	w.row("summary", "promo_requesters", "запросили промокод (пользователь × день)", i(r.PromoRequesters), "")
	w.row("summary", "promo_got_code", "получили код", i(r.PromoGotCode), "")
	w.row("summary", "promo_no_code", "кодов не хватило", i(r.PromoNoCode), "")
	w.row("summary", "promo_issued", "выдано промокодов", i(r.PromoIssued), "")
	w.row("summary", "full_chances", fmt.Sprintf("пользователей с максимумом шансов (%d)", r.MaxChances), i(r.FullChances), "")
	for _, d := range r.NewUsers {
		w.row("new_users", d.Day.Format(time.DateOnly), "новые пользователи", i(d.Count), "")
	}
	for _, p := range r.Partners {
		w.row("partner_stock", i(int(p.ID)), p.Name, i(p.Free), fmt.Sprintf("total=%d;issued=%d", p.Total, p.Issued))
	}
	for _, d := range r.Days {
		w.row("draw", d.Day.Format(time.DateOnly), d.Partner, i(d.Entries),
			fmt.Sprintf("excluded=%d;promo_requesters=%d;promo_issued=%d;status=%s", d.Excluded, d.PromoRequesters, d.PromoIssued, d.Status))
	}
	for _, k := range r.Distribution {
		w.row("chances", i(k.Chances), "пользователей с таким числом шансов", i(k.Users), "")
	}
	return w.file("stats_" + r.GeneratedAt.In(loc).Format("2006-01-02_1504") + ".csv")
}

func (h *handlers) stats(c tele.Context) error {
	csv, err := parseStatsArgs(c.Message().Payload)
	if err != nil {
		return h.fail(c, ActionStats, err)
	}
	rep, err := h.Backend.Stats(ctxOf(c), adminID(c), csv)
	if err != nil {
		return h.fail(c, ActionStats, err)
	}
	if !csv {
		return sendLong(c, texts.AdminStats(statsView(rep), h.Location))
	}
	file, err := StatsCSV(rep, h.Location)
	if err != nil {
		return h.fail(c, ActionStats, err)
	}
	return c.Send(&tele.Document{
		File:     tele.FromReader(strings.NewReader(string(file.Data))),
		FileName: file.Name,
		Caption:  texts.AdminExportDone("stats", file.Rows),
	})
}
