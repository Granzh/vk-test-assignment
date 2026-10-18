package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"chat_bot/internal/service"
	"chat_bot/internal/texts"
)

// Drawer holds and confirms daily draws (implemented by *service.DailyDraw).
type Drawer interface {
	Run(ctx context.Context, adminID int64, date time.Time, force bool) (service.DrawResult, error)
	Confirm(ctx context.Context, adminID int64, date time.Time) (service.ConfirmResult, error)
}

// WithDrawer sets the draw service used by RunDraw and ConfirmDraw.
func (s *Service) WithDrawer(d Drawer) *Service {
	s.draw = d
	return s
}

// RunDraw holds the draw of a day (or returns the stored result) and turns the
// service's sentinel errors into explanations for the admin.
func (s *Service) RunDraw(ctx context.Context, adminID int64, date time.Time, force bool) (service.DrawResult, error) {
	res, err := s.draw.Run(ctx, adminID, date, force)
	return res, drawError(err, date)
}

// ConfirmDraw confirms a held draw and notifies its winners.
func (s *Service) ConfirmDraw(ctx context.Context, adminID int64, date time.Time) (service.ConfirmResult, error) {
	res, err := s.draw.Confirm(ctx, adminID, date)
	return res, drawError(err, date)
}

func drawError(err error, date time.Time) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, service.ErrDrawDayNotFound):
		return inputErr(texts.AdminDrawNoDay(date))
	case errors.Is(err, service.ErrDrawDayNotOver):
		return inputErr(texts.AdminDrawNotOver(date))
	case errors.Is(err, service.ErrDrawDayInFuture):
		return inputErr(texts.AdminDrawInFuture(date))
	case errors.Is(err, service.ErrDrawNotRun):
		return inputErr(texts.AdminDrawNotRun(date))
	case errors.Is(err, service.ErrSubscriptionCheck):
		return inputErr(texts.AdminDrawCheckFailed(err))
	}
	return err
}

// parseDrawArgs parses "date" or, when allowForce, "date [force]".
func parseDrawArgs(payload, cmd, usage string, allowForce bool) (date time.Time, force bool, err error) {
	fields := strings.Fields(payload)
	max := 1
	if allowForce {
		max = 2
	}
	if len(fields) < 1 || len(fields) > max {
		return date, false, inputErr(texts.AdminFieldCount(cmd, len(fields), 1, max, usage))
	}
	if date, err = time.ParseInLocation(time.DateOnly, fields[0], time.UTC); err != nil {
		return date, false, inputErr(texts.AdminBadDate("Дата", fields[0]))
	}
	if len(fields) == 2 {
		if !strings.EqualFold(fields[1], "force") {
			return date, false, inputErr(fmt.Sprintf("Неизвестный параметр «%s». Допустим только force.\nФормат: %s", fields[1], usage))
		}
		force = true
	}
	return date, force, nil
}

func drawView(r service.DrawResult) texts.AdminDrawView {
	v := texts.AdminDrawView{
		Date: r.Day.Day.Time, Partner: r.Partner.Name, Status: r.Day.Status, Already: r.Already, DrawnAt: r.DrawnAt,
		Entries: r.Entries, Excluded: r.Excluded, WinnersWanted: r.WinnersCount,
	}
	for _, w := range r.Winners {
		v.Winners = append(v.Winners, texts.AdminDrawWinner{
			TelegramID: w.TelegramID, Username: w.Username, FirstName: w.FirstName, Prize: w.Prize, Status: w.Status,
		})
	}
	return v
}

// DrawPreview renders a draw result for the admin (used by the scheduler too).
func DrawPreview(r service.DrawResult, loc *time.Location) string {
	return texts.AdminDrawPreview(drawView(r), loc)
}
