package service

import (
	"context"
	"fmt"
	"time"

	"chat_bot/internal/storage/db"
)

// ChancesStore is the part of the storage layer the chances screen needs.
type ChancesStore interface {
	CountActivityDays(ctx context.Context, userID int64) (int64, error)
	ListUserDays(ctx context.Context, uid int64) ([]db.ListUserDaysRow, error)
}

// PartnerInteraction is what a user did with one partner, over all days.
type PartnerInteraction struct {
	Name  string
	Promo bool // took a promo code
	Entry bool // registered in the daily draw
}

// ActiveDayView is one day that earned a chance.
type ActiveDayView struct {
	Day     time.Time // campaign date
	Partner string
}

// ChancesView is the data of the "my chances" screen.
type ChancesView struct {
	Chances  int
	Max      int
	Days     []ActiveDayView      // days that earned a chance, oldest first
	Partners []PartnerInteraction // in order of first interaction
}

// Chances builds the chances screen.
type Chances struct {
	store      ChancesStore
	maxChances int
}

// NewChances builds the service; maxChances is the cap shown as "N of MAX".
func NewChances(store ChancesStore, maxChances int) *Chances {
	return &Chances{store: store, maxChances: maxChances}
}

// View returns the user's chances, active days and partners. A user without
// activity gets zero chances and empty lists.
func (c *Chances) View(ctx context.Context, userID int64) (ChancesView, error) {
	n, err := chanceCount(ctx, c.store, userID, c.maxChances)
	if err != nil {
		return ChancesView{}, err
	}
	rows, err := c.store.ListUserDays(ctx, userID)
	if err != nil {
		return ChancesView{}, fmt.Errorf("list user days: %w", err)
	}

	v := ChancesView{Chances: n, Max: c.maxChances}
	idx := map[int64]int{} // partner id -> index in v.Partners
	for _, r := range rows {
		if r.Counted {
			v.Days = append(v.Days, ActiveDayView{Day: r.Day.Time, Partner: r.PartnerName})
		}
		i, ok := idx[r.PartnerID]
		if !ok {
			i = len(v.Partners)
			idx[r.PartnerID] = i
			v.Partners = append(v.Partners, PartnerInteraction{Name: r.PartnerName})
		}
		v.Partners[i].Promo = v.Partners[i].Promo || r.HasPromo
		v.Partners[i].Entry = v.Partners[i].Entry || r.HasEntry
	}
	return v, nil
}
