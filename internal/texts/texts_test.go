package texts

import (
	"strings"
	"testing"
	"time"
)

func TestPromoIssuedFull(t *testing.T) {
	msk, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	// 20:59 UTC on 30 Sep is 23:59 on 30 Sep in Moscow.
	until := time.Date(2026, 9, 30, 20, 59, 0, 0, time.UTC)
	got := PromoIssued("Кофейня", "ABC123", &until, "сайте example.ru", msk)

	for _, want := range []string{
		"Твой фестивальный бонус от Кофейня готов 🎁",
		"Промокод: <code>ABC123</code>",
		"Используй его до 30.09.2026 на сайте example.ru.",
		"Теперь можешь принять участие в розыгрыше призов дня.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestPromoIssuedDateIsLocal(t *testing.T) {
	msk, _ := time.LoadLocation("Europe/Moscow")
	// 21:30 UTC on 30 Sep is already 1 October in Moscow.
	until := time.Date(2026, 9, 30, 21, 30, 0, 0, time.UTC)
	if got := PromoIssued("P", "C", &until, "", msk); !strings.Contains(got, "до 01.10.2026") {
		t.Errorf("date must be shown in the campaign timezone:\n%s", got)
	}
}

func TestPromoIssuedOptionalParts(t *testing.T) {
	until := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

	if got := PromoIssued("P", "C", nil, "", time.UTC); strings.Contains(got, "Используй") {
		t.Errorf("no date and no place: instruction must be omitted:\n%s", got)
	}
	if got := PromoIssued("P", "C", &until, "", time.UTC); !strings.Contains(got, "Используй его до 30.09.2026.") {
		t.Errorf("date only:\n%s", got)
	}
	if got := PromoIssued("P", "C", nil, "точках продаж", time.UTC); !strings.Contains(got, "Используй его на точках продаж.") {
		t.Errorf("place only:\n%s", got)
	}
}

func TestPromoEscapesHTML(t *testing.T) {
	got := PromoIssued("A<b>&", "<x>", nil, "<i>", time.UTC)
	for _, bad := range []string{"<b>", "<x>", "<i>"} {
		if strings.Contains(got, bad) {
			t.Errorf("unescaped %q in:\n%s", bad, got)
		}
	}
	if got := PromoSoldOut("A<b>"); strings.Contains(got, "<b>") {
		t.Errorf("sold out text not escaped: %s", got)
	}
}

func TestPromoSoldOut(t *testing.T) {
	got := PromoSoldOut("Кофейня")
	if !strings.HasPrefix(got, "Промокоды от Кофейня уже закончились — их разобрали быстрее всех 🔥") {
		t.Errorf("unexpected text: %s", got)
	}
	if !strings.Contains(got, "принять участие в розыгрыше призов дня") {
		t.Errorf("missing call to action: %s", got)
	}
}

func TestChancesScreen(t *testing.T) {
	d := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	got := ChancesScreen(2, 10,
		[]DayLine{{Date: d, Partner: "A<b>"}},
		[]PartnerLine{{Name: "A<b>", Promo: true, Entry: true}, {Name: "B", Promo: true}, {Name: "C", Entry: true}},
		7)
	for _, want := range []string{
		"Твой фестивальный пропуск\nНакоплено шансов: 2 из 10",
		"выиграть главный приз",
		"• 05.09 — A&lt;b&gt;",
		"• A&lt;b&gt; — промокод и розыгрыш",
		"• B — промокод\n",
		"• C — розыгрыш\n",
		"Осталось дней акции: 7",
		"Главный приз",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("screen lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "пропус\n") || strings.Contains(got, "выигратьт") {
		t.Error("spec typos are back")
	}
}

func TestChancesScreenEmpty(t *testing.T) {
	got := ChancesScreen(0, 10, nil, nil, 30)
	if !strings.Contains(got, "Накоплено шансов: 0 из 10") || !strings.Contains(got, "пока ни одного") {
		t.Errorf("screen:\n%s", got)
	}
	if strings.Contains(got, "Партнёры") {
		t.Error("empty partner list must be omitted")
	}
}
