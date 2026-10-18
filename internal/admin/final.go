package admin

import (
	"context"
	"errors"
	"strings"
	"time"

	"chat_bot/internal/service"
	"chat_bot/internal/texts"
)

// FinalDrawer holds and confirms the final draw (implemented by *service.FinalDraw).
type FinalDrawer interface {
	Run(ctx context.Context, adminID int64, force bool) (service.FinalResult, error)
	Confirm(ctx context.Context, adminID int64) (service.FinalConfirmResult, error)
}

// WithFinalDrawer sets the final draw service.
func (s *Service) WithFinalDrawer(d FinalDrawer) *Service {
	s.final = d
	return s
}

// RunFinal holds the final draw (or returns the stored result).
func (s *Service) RunFinal(ctx context.Context, adminID int64, force bool) (service.FinalResult, error) {
	res, err := s.final.Run(ctx, adminID, force)
	return res, s.finalError(err)
}

// ConfirmFinal confirms the final draw and notifies its winners.
func (s *Service) ConfirmFinal(ctx context.Context, adminID int64) (service.FinalConfirmResult, error) {
	res, err := s.final.Confirm(ctx, adminID)
	return res, s.finalError(err)
}

func (s *Service) finalError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, service.ErrFinalNotOver):
		return inputErr(texts.AdminFinalNotOver(s.end))
	case errors.Is(err, service.ErrFinalNoTickets):
		return inputErr(texts.AdminFinalNoTickets)
	case errors.Is(err, service.ErrFinalNotRun):
		return inputErr(texts.AdminFinalNotRun)
	}
	return err
}

// parseFinalArgs parses "" or "force".
func parseFinalArgs(payload string) (force bool, err error) {
	fields := strings.Fields(payload)
	switch {
	case len(fields) == 0:
		return false, nil
	case len(fields) == 1 && strings.EqualFold(fields[0], "force"):
		return true, nil
	}
	return false, inputErr(texts.AdminFieldCount("/final_run", len(fields), 0, 1, texts.UsageFinalRun) +
		"\nДопустим только параметр force.")
}

func finalView(r service.FinalResult, loc *time.Location) texts.AdminFinalView {
	v := texts.AdminFinalView{
		ID: r.Draw.ID, Status: r.Draw.Status, Already: r.Already, HeldAt: r.Draw.HeldAt.Time, HeldBy: r.Draw.HeldByTelegramID,
		DrumTickets: int(r.Draw.DrumTickets), DrumUsers: int(r.Draw.DrumUsers), WinnersWanted: int(r.Draw.WinnersCount),
		ReservesWanted: int(r.Draw.ReservesCount), PrizeTickets: int(r.Draw.PrizeTickets),
	}
	for _, w := range r.Winners {
		v.Lines = append(v.Lines, texts.AdminFinalLine{
			TelegramID: w.TelegramID, Username: w.Username, FirstName: w.FirstName,
			Tickets: w.Tickets, Reserve: w.Reserve, Position: w.Position,
		})
	}
	return v
}
