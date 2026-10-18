package texts

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// Stats commands.
const (
	UsageStats = "/stats [csv]"
	menuStats  = "статистика: пользователи, промокоды, ежедневные розыгрыши, шансы; с csv — полная выгрузка"
)

// StatsDay is a count on a campaign date.
type StatsDay struct {
	Date  time.Time
	Count int
}

// StatsPartner is the promo stock of a partner.
type StatsPartner struct {
	Name                string
	Free, Total, Issued int
}

// StatsDraw is a campaign day with its draw and promo numbers.
type StatsDraw struct {
	Date               time.Time
	Partner            string
	Entries, Excluded  int
	Requesters, Issued int
	Status             string
}

// StatsBucket is the number of users with exactly Chances chances.
type StatsBucket struct{ Chances, Users int }

// StatsView is the data of the /stats message.
type StatsView struct {
	GeneratedAt                  time.Time
	TotalUsers, BlockedUsers     int
	NewUsers                     []StatsDay
	PromoRequests, Requesters    int
	GotCode, NoCode, PromoIssued int
	Partners                     []StatsPartner
	Days                         []StatsDraw
	MaxChances                   int
	Distribution                 []StatsBucket
	FullChances                  int
}

// statsRecentDays is how many of the latest days with new users the message lists.
const statsRecentDays = 14

// AdminStats renders /stats (HTML). The csv export carries every row.
func AdminStats(v StatsView, loc *time.Location) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📊 <b>Статистика</b> на %s\n", v.GeneratedAt.In(loc).Format("02.01.2006 15:04"))

	fmt.Fprintf(&b, "\n<b>Пользователи</b>\nЗапустили бот: %d (заблокировали бота: %d)\n", v.TotalUsers, v.BlockedUsers)
	if len(v.NewUsers) > 0 {
		recent := v.NewUsers
		if len(recent) > statsRecentDays {
			recent = recent[len(recent)-statsRecentDays:]
		}
		b.WriteString("Новые по дням")
		if len(recent) < len(v.NewUsers) {
			fmt.Fprintf(&b, " (последние %d из %d)", len(recent), len(v.NewUsers))
		}
		b.WriteString(":\n")
		for _, d := range recent {
			fmt.Fprintf(&b, "• %s — %d\n", d.Date.Format("02.01"), d.Count)
		}
	}

	fmt.Fprintf(&b, "\n<b>Промокоды</b>\nЗапросов: %d от %d (пользователь × день)\nПолучили код: %d, кодов не хватило: %d\nВыдано кодов: %d\n",
		v.PromoRequests, v.Requesters, v.GotCode, v.NoCode, v.PromoIssued)
	if len(v.Partners) > 0 {
		b.WriteString("Остаток по партнёрам:\n")
		for _, p := range v.Partners {
			warn := ""
			if p.Free == 0 {
				warn = " ⚠️"
			}
			fmt.Fprintf(&b, "• %s — свободно %d из %d (выдано %d)%s\n", html.EscapeString(p.Name), p.Free, p.Total, p.Issued, warn)
		}
	}

	b.WriteString("\n<b>Ежедневные розыгрыши</b>\n")
	if len(v.Days) == 0 {
		b.WriteString("Дни ещё не назначены (/day_set).\n")
	}
	for _, d := range v.Days {
		fmt.Fprintf(&b, "• %s · %s — участников %d", d.Date.Format("02.01"), html.EscapeString(d.Partner), d.Entries)
		if d.Excluded > 0 {
			fmt.Fprintf(&b, " (выбыло %d)", d.Excluded)
		}
		fmt.Fprintf(&b, "; промокод: запросили %d, выдано %d\n", d.Requesters, d.Issued)
	}

	fmt.Fprintf(&b, "\n<b>Шансы на главный приз</b> (максимум %d)\n", v.MaxChances)
	for _, k := range v.Distribution {
		share := 0.0
		if v.TotalUsers > 0 {
			share = float64(k.Users) * 100 / float64(v.TotalUsers)
		}
		fmt.Fprintf(&b, "• %d — %d (%.1f%%)\n", k.Chances, k.Users, share)
	}
	fmt.Fprintf(&b, "Набрали максимум (%d): %d\n\nПолная выгрузка: <code>/stats csv</code>", v.MaxChances, v.FullChances)
	return b.String()
}
