package texts

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// Broadcast commands.
const (
	UsageBroadcast       = "/broadcast all|no_promo|no_entry | текст рассылки"
	UsageBroadcastStatus = "/broadcast_status [id]"
	UsageBroadcastCancel = "/broadcast_cancel id"
	ExampleBroadcast     = "/broadcast no_promo | Сегодня у партнёра новый промокод — забери его до конца дня!"

	BtnBroadcastSend   = "Отправить"
	BtnBroadcastCancel = "Отменить"
)

// Broadcast lines of the admin menu.
const (
	menuBroadcast       = "рассылка: сегмент и текст (на обычном тексте без разметки). Сначала придёт предпросмотр, отправка — после подтверждения"
	menuBroadcastStatus = "ход и итог рассылки (без id — последней)"
	menuBroadcastCancel = "остановить рассылку или отменить черновик"
)

// SegmentLabel names a broadcast segment for admins.
func SegmentLabel(segment string) string {
	switch segment {
	case "all":
		return "все пользователи"
	case "no_promo":
		return "не получили промокод сегодня"
	case "no_entry":
		return "не участвовали в сегодняшнем розыгрыше"
	}
	return segment
}

// AdminBroadcastBadSegment: unknown segment name.
func AdminBroadcastBadSegment(raw string) string {
	return fmt.Sprintf("Неизвестный сегмент «%s». Доступно:\n• all — все пользователи\n• no_promo — не получили промокод сегодня\n"+
		"• no_entry — не участвовали в сегодняшнем розыгрыше\nФормат: %s", html.EscapeString(raw), html.EscapeString(UsageBroadcast))
}

// AdminBroadcastBadText: empty or too long text.
func AdminBroadcastBadText(max int) string {
	return fmt.Sprintf("Текст рассылки пустой или длиннее %d символов. Формат: %s", max, html.EscapeString(UsageBroadcast))
}

// AdminBroadcastSegmentUnavailable: a daily segment without an active day.
func AdminBroadcastSegmentUnavailable(segment, reason string) string {
	return fmt.Sprintf("Сегмент «%s» привязан к сегодняшнему дню акции, но сейчас дня нет (%s). "+
		"Выберите сегмент all или дождитесь дня акции с назначенным партнёром.", html.EscapeString(SegmentLabel(segment)), html.EscapeString(reason))
}

// AdminBroadcastNotFound: no such broadcast.
func AdminBroadcastNotFound(id int64) string {
	if id == 0 {
		return "Рассылок ещё не было. Создать: /broadcast"
	}
	return fmt.Sprintf("Рассылка #%d не найдена.", id)
}

// AdminBroadcastState: the action does not fit the broadcast state.
func AdminBroadcastState(id int64, detail string) string {
	return fmt.Sprintf("Рассылка #%d: действие не подходит к её состоянию (%s). Ход: /broadcast_status %d", id, html.EscapeString(detail), id)
}

// AdminBroadcastDraft is the summary shown under the preview (HTML).
func AdminBroadcastDraft(id int64, segment string, audience int) string {
	return fmt.Sprintf("👆 Так сообщение увидят получатели.\n\n<b>Рассылка #%d</b>\nСегмент: %s\nПолучателей сейчас: %d "+
		"(без заблокировавших бота; точный список фиксируется при отправке)\n\nОтправить?", id, SegmentLabel(segment), audience)
}

// AdminBroadcastStarted confirms the start (HTML).
func AdminBroadcastStarted(id int64, recipients int) string {
	if recipients == 0 {
		return fmt.Sprintf("Рассылка #%d: в сегменте никого нет, отправлять некому.", id)
	}
	return fmt.Sprintf("🚀 Рассылка #%d запущена: получателей %d. Отчёт придёт сюда по завершении, ход: <code>/broadcast_status %d</code>", id, recipients, id)
}

// AdminBroadcastCancelled confirms a cancellation (HTML).
func AdminBroadcastCancelled(id int64, sent, pending int) string {
	return fmt.Sprintf("Рассылка #%d отменена. Уже отправлено: %d, не будет отправлено: %d.", id, sent, pending)
}

// BroadcastReportData is the data of a broadcast report.
type BroadcastReportData struct {
	ID         int64
	Status     string
	Segment    string
	Total      int
	Pending    int
	Sent       int
	Failed     int
	Blocked    int
	StartedAt  time.Time
	FinishedAt time.Time
}

// BroadcastReport renders the progress or outcome of a broadcast (HTML).
func BroadcastReport(r BroadcastReportData) string {
	var b strings.Builder
	switch r.Status {
	case "done":
		fmt.Fprintf(&b, "✅ Рассылка #%d завершена\n", r.ID)
	case "cancelled":
		fmt.Fprintf(&b, "⛔ Рассылка #%d отменена\n", r.ID)
	case "sending":
		fmt.Fprintf(&b, "⏳ Рассылка #%d идёт\n", r.ID)
	default:
		fmt.Fprintf(&b, "📝 Рассылка #%d (черновик, не отправлена)\n", r.ID)
	}
	fmt.Fprintf(&b, "Сегмент: %s\n", SegmentLabel(r.Segment))
	if r.Status == "draft" {
		return strings.TrimSpace(b.String())
	}
	fmt.Fprintf(&b, "Получателей: %d\nОтправлено: %d\nОшибок: %d\nЗаблокировали бота: %d\n", r.Total, r.Sent, r.Failed, r.Blocked)
	if r.Pending > 0 {
		fmt.Fprintf(&b, "Осталось отправить: %d\n", r.Pending)
	}
	if !r.StartedAt.IsZero() && !r.FinishedAt.IsZero() {
		fmt.Fprintf(&b, "Заняло: %s\n", r.FinishedAt.Sub(r.StartedAt).Round(time.Second))
	}
	if r.Blocked > 0 {
		b.WriteString("Заблокировавшие бота помечены и в следующие рассылки не попадут, пока снова не напишут боту.\n")
	}
	if r.Failed > 0 {
		b.WriteString("Ошибки: временные сбои Telegram после нескольких попыток; причины записаны в базе (broadcast_recipients.last_error).\n")
	}
	return strings.TrimSpace(b.String())
}
