// Package texts holds every user-facing string of the bot.
//
// Short messages and button labels are constants; the long how-to and rules
// texts live in .txt files next to this package and are embedded at build time.
// Functions whose doc says "(HTML)" return text for tele.ModeHTML with all
// dynamic parts escaped.
package texts

import (
	_ "embed"
	"fmt"
	"html"
	"strings"
	"time"
)

// Welcome and the main menu.
const (
	Welcome = "Добро пожаловать в Бот розыгрышей"

	BtnGetPromo = "Получить промокод"
	BtnJoin     = "Участвовать в розыгрыше"
	BtnHowTo    = "Как участвовать"
	BtnChances  = "Мои шансы"
	BtnRules    = "Правила акции"
)

// Campaign availability.
const (
	CampaignFinished = "Акция уже завершилась. Спасибо, что был с нами! 🎉"
	NoPartnerToday   = "Сегодня партнёр ещё не назначен. Загляни чуть позже 👀"
)

// InternalError is the generic apology for unexpected failures.
const InternalError = "Что-то пошло не так. Попробуй ещё раз чуть позже."

//go:embed how_to.txt
var howTo string

//go:embed rules.txt
var rules string

// HowToParticipate returns the "how to participate" text.
func HowToParticipate() string { return strings.TrimSpace(howTo) }

// Rules returns the campaign rules text.
func Rules() string { return strings.TrimSpace(rules) }

// CampaignNotStarted tells the user when the campaign begins.
func CampaignNotStarted(start time.Time) string {
	return fmt.Sprintf("Акция ещё не началась — старт %s. Возвращайся, будет интересно! ✨", start.Format("02.01.2006"))
}

// PromoIssued is the promo code message (HTML).
//
// where completes the phrase "на ..." (e.g. "сайте example.ru", "точках
// продаж"). validUntil and where are optional: the matching part of the
// instruction is dropped when empty.
func PromoIssued(partner, code string, validUntil *time.Time, where string, loc *time.Location) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Твой фестивальный бонус от %s готов 🎁\n\n", html.EscapeString(partner))
	fmt.Fprintf(&b, "Промокод: <code>%s</code>\n\n", html.EscapeString(code))

	var parts []string
	if validUntil != nil {
		parts = append(parts, "до "+validUntil.In(loc).Format("02.01.2006"))
	}
	if where = strings.TrimSpace(where); where != "" {
		parts = append(parts, "на "+html.EscapeString(where))
	}
	if len(parts) > 0 {
		fmt.Fprintf(&b, "Используй его %s.\n\n", strings.Join(parts, " "))
	}
	b.WriteString("Теперь можешь принять участие в розыгрыше призов дня.")
	return b.String()
}

// PromoSoldOut is shown when the partner's promo codes are exhausted (HTML).
func PromoSoldOut(partner string) string {
	return fmt.Sprintf("Промокоды от %s уже закончились — их разобрали быстрее всех 🔥\n\n"+
		"Но ты все еще можешь подписаться на партнера и принять участие в розыгрыше призов дня.",
		html.EscapeString(partner))
}

// Daily partner draw buttons.
const (
	BtnCheckSub      = "Проверить подписку"
	BtnSubscribe     = "Подписаться"
	BtnCheckSubAgain = "Проверить еще раз"
)

// CheckFailed is shown when the subscription could not be verified.
const CheckFailed = "Не удалось проверить, попробуй позже"

// AlreadyJoined is shown when the user already has today's entry.
const AlreadyJoined = "Ты уже участвуешь в сегодняшнем розыгрыше ✅\nРезультаты сообщим после подведения итогов."

// BtnSubscribeTo is the label of the button that opens the partner's channel.
func BtnSubscribeTo(partner string) string { return "Подписаться на " + partner }

// JoinPrompt asks the user to subscribe to the partner (HTML).
func JoinPrompt(partner string) string {
	return fmt.Sprintf("Чтобы попасть в розыгрыш призов от %s, подпишись на аккаунт партнера.", html.EscapeString(partner))
}

// JoinSuccess confirms the registration and shows the chances so far (HTML).
func JoinSuccess(partner string, chances int) string {
	return fmt.Sprintf("Ты в лайнапе ✅\n\nЗаявка на розыгрыш призов от %s зарегистрирована.\n"+
		"Возвращайся завтра — на сцену выйдет новый партнер.\n\n"+
		"Шансов на главный приз: %d", html.EscapeString(partner), chances)
}

// NotSubscribed is shown when no subscription was found (HTML).
func NotSubscribed(partner string) string {
	return fmt.Sprintf("Пока не вижу подписку 👀\n\nПодпишись на аккаунт %s, вернись в бот и попробуй еще раз.",
		html.EscapeString(partner))
}

// ChancesScreen is the "my chances" message (HTML).
//
// days are the active days (date and partner name) and partners what the user
// did with each partner; daysLeft is the number of campaign days remaining.
func ChancesScreen(chances, max int, days []DayLine, partners []PartnerLine, daysLeft int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Твой фестивальный пропуск\nНакоплено шансов: %d из %d\n", chances, max)
	b.WriteString("Чем больше дней розыгрыша ты посетишь, тем выше вероятность выиграть главный приз.\n\n")

	b.WriteString("Пройденные дни:\n")
	if len(days) == 0 {
		b.WriteString("пока ни одного — получи промокод или участвуй в розыгрыше 🎟\n")
	}
	for _, d := range days {
		fmt.Fprintf(&b, "• %s — %s\n", d.Date.Format("02.01"), html.EscapeString(d.Partner))
	}

	if len(partners) > 0 {
		b.WriteString("\nПартнёры, с которыми ты уже взаимодействовал:\n")
		for _, p := range partners {
			fmt.Fprintf(&b, "• %s — %s\n", html.EscapeString(p.Name), interaction(p.Promo, p.Entry))
		}
	}

	fmt.Fprintf(&b, "\nОсталось дней акции: %d\n\n", daysLeft)
	b.WriteString("Главный приз разыграем в финале среди всех накопленных шансов: один шанс — один билет.")
	return b.String()
}

// DayLine is an active day on the chances screen.
type DayLine struct {
	Date    time.Time
	Partner string
}

// PartnerLine is a partner on the chances screen with what the user did there.
type PartnerLine struct {
	Name         string
	Promo, Entry bool
}

func interaction(promo, entry bool) string {
	switch {
	case promo && entry:
		return "промокод и розыгрыш"
	case promo:
		return "промокод"
	default:
		return "розыгрыш"
	}
}

// DayClosed is shown when the day's draw has been held and entries are closed.
const DayClosed = "Розыгрыш этого дня уже проведён, заявки больше не принимаются. Загляни завтра — на сцену выйдет новый партнер 🎤"

// DailyWinner is the notification to a daily draw winner (HTML).
func DailyWinner(prize, partner, instruction string) string {
	return fmt.Sprintf("Ты в числе хедлайнеров дня 🔥\n\nТы выиграл %s от %s.\n\nЧтобы получить приз, %s",
		html.EscapeString(prize), html.EscapeString(partner), html.EscapeString(instruction))
}

// DailyWinnerInstruction is the default follow-up instruction of a winner notification.
const DailyWinnerInstruction = "дождись сообщения от команды акции — мы напишем тебе здесь, в боте, и подскажем, что делать дальше."

// Final draw winner notification (spec 6.2).
const (
	BtnGetPrize = "Получить приз"
)

// FinalWinner is the notification to a winner of the final draw (HTML).
func FinalWinner(prize string, prizeTickets int) string {
	return fmt.Sprintf("Твой сентябрь выходит на главную сцену 🔥\n\nТы выиграл %s!\nКоличество билетов: %d.\n\n"+
		"Чтобы подтвердить получение приза, нажми кнопку ниже и оставь необходимые контактные данные.",
		html.EscapeString(prize), prizeTickets)
}

// Prize claim dialog (spec 6.3).
const (
	BtnConsentYes = "Согласен"
	BtnConsentNo  = "Не согласен"

	AskContactName = "Отправь одним сообщением своё имя и фамилию — на них оформим приз."

	BadContactName = "Не получилось разобрать имя. Напиши имя и фамилию буквами, например: Анна Иванова."

	ConsentThanks = "Спасибо! Согласие и данные получены ✅\nМы передадим их партнёру или билетному оператору и свяжемся с тобой, чтобы вручить приз."

	ConsentDeclined = "Понимаем. Без согласия на передачу данных мы не можем оформить приз, поэтому он переходит следующему участнику. " +
		"Твои данные удалены. Спасибо, что был с нами!"

	NoActivePrize = "Сейчас у тебя нет приза, который нужно оформить."

	PrizeAlreadyReceived = "Данные для приза уже получены ✅ Мы свяжемся с тобой."

	PrizeDeadlinePassed = "Срок ответа истёк, и приз перешёл следующему участнику. Спасибо, что был с нами!"

	PrizeExpired = "Срок для подтверждения приза истёк, поэтому приз переходит следующему участнику. Спасибо, что был с нами!"

	PrizeDialogStale = "Этот вопрос уже неактуален."
)

// ConsentPrompt asks for the explicit consent (HTML).
func ConsentPrompt(name string) string {
	return fmt.Sprintf("Проверь, пожалуйста: <b>%s</b>.\n\nДаёшь ли ты согласие на передачу этих данных (имя и фамилия) "+
		"партнёру или билетному оператору для вручения приза? Без согласия приз оформить нельзя.", html.EscapeString(name))
}

// PrizeAdminEvent is an alert for admins about the prize queue (HTML).
func PrizeAdminEvent(kind string, promotedPos int, noReserve bool, undeliverable []int64) string {
	var b strings.Builder
	switch kind {
	case "expired":
		b.WriteString("⏰ Победитель финала не ответил в срок, приз передаётся дальше.\n")
	case "declined":
		b.WriteString("🚫 Победитель финала отказался от передачи данных, приз передаётся дальше.\n")
	}
	for _, id := range undeliverable {
		fmt.Fprintf(&b, "• резервисту id %d не удалось доставить сообщение (вероятно, заблокировал бота), он пропущен\n", id)
	}
	switch {
	case promotedPos > 0:
		fmt.Fprintf(&b, "Приз передан резервисту №%d, срок ответа пошёл заново.", promotedPos)
	case noReserve:
		b.WriteString("⚠️ Резерв закончился: приз остался без победителя. Решите вручную.")
	}
	return strings.TrimSpace(b.String())
}

// PrizeDeclinedAlready answers a user who refused to share data earlier.
const PrizeDeclinedAlready = "Ты отказался от передачи данных, поэтому приз перешёл дальше."

// ConsentNudge is shown when a user types text while the consent question is open.
const ConsentNudge = "Нажми «Согласен» или «Не согласен» под вопросом ниже 👇"

// Busy is the reply to an update dropped because the bot is overloaded.
const Busy = "Сейчас очень много обращений. Попробуй ещё раз через минуту 🙏"
