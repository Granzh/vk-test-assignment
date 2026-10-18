package texts

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// Admin-facing texts (HTML). Errors are deliberately detailed: they say what
// was received, what is wrong and what a valid value looks like.

// Usage lines of the admin commands, also used as the menu.
const (
	UsagePartnerAdd  = "/partner_add название | ссылка | chat_id канала | текст условий [| где действует промокод | действует до ГГГГ-ММ-ДД]"
	UsagePartnerEdit = "/partner_edit id партнёра | поле | новое значение"
	UsagePartnerList = "/partner_list"
	UsageDaySet      = "/day_set ГГГГ-ММ-ДД | id партнёра | число победителей | призы"
	UsagePromoUpload = "/promo_upload id_партнёра — командой-подписью к CSV-файлу"
	UsagePromoStats  = "/promo_stats"
	UsageExport      = "/export participants|winners|partner_winners|finalists id"
)

// AdminMenu is the reply to /admin.
func AdminMenu() string {
	var b strings.Builder
	b.WriteString("<b>Админ-панель</b>\n\n")
	rows := [][2]string{
		{UsagePartnerAdd, "добавить партнёра"},
		{UsagePartnerEdit, "изменить поле партнёра: name, url, chat_id, terms, promo_text, valid_until (пусто или - очищает promo_text и valid_until)"},
		{UsagePartnerList, "список партнёров"},
		{UsageDaySet, "назначить партнёра, победителей и призы на дату"},
		{UsagePromoUpload, "загрузить промокоды: отправь боту CSV-файл (один код в строке) с подписью <code>/promo_upload id_партнёра</code>"},
		{UsagePromoStats, "остатки промокодов по партнёрам"},
		{UsageDrawRun, menuDrawRun},
		{UsageDrawConfirm, menuDrawConfirm},
		{UsageFinalRun, menuFinalRun},
		{UsageFinalConfirm, menuFinalConfirm},
		{UsageWinners, menuWinners},
		{UsagePurgeContacts, menuPurge},
		{UsageBroadcast, menuBroadcast},
		{UsageBroadcastStatus, menuBroadcastStatus},
		{UsageBroadcastCancel, menuBroadcastCancel},
		{UsageStats, menuStats},
		{UsageExport, "выгрузка CSV: участники дня, победители дня, финалисты. Для дня вместо id можно указать дату ГГГГ-ММ-ДД"},
	}
	for _, r := range rows {
		fmt.Fprintf(&b, "<code>%s</code>\n%s\n\n", html.EscapeString(r[0]), r[1])
	}
	b.WriteString("Поля команд разделяются символом <code>|</code>. Все действия записываются в журнал аудита.")
	return b.String()
}

// AdminUsage explains a command invoked without valid arguments.
func AdminUsage(usage, example string) string {
	return fmt.Sprintf("Формат команды:\n<code>%s</code>\n\nПример:\n<code>%s</code>",
		html.EscapeString(usage), html.EscapeString(example))
}

// Examples shown with the usage text.
const (
	ExamplePartnerAdd  = "/partner_add Кофейня Север | https://t.me/kofe_sever | -1001234567890 | Скидка 20% на второй напиток | кофейне на Арбате | 2026-09-30"
	ExamplePartnerEdit = "/partner_edit 3 | chat_id | -1001234567890"
	ExampleDaySet      = "/day_set 2026-09-05 | 3 | 2 | Сертификат на 5000 ₽ и годовая подписка"
	ExampleExport      = "/export participants 2026-09-05"
)

// AdminError wraps a problem description for the admin.
func AdminError(msg string) string { return "⚠️ " + msg }

// AdminInternalError reports an unexpected failure with its cause (admins may see it).
func AdminInternalError(err error) string {
	return "❌ Внутренняя ошибка, действие не выполнено. Причина: " + html.EscapeString(err.Error()) +
		"\nПодробности записаны в лог бота."
}

// AdminFieldCount: wrong number of |-separated fields.
func AdminFieldCount(cmd string, got, min, max int, usage string) string {
	want := fmt.Sprintf("от %d до %d", min, max)
	if min == max {
		want = fmt.Sprint(min)
	}
	return fmt.Sprintf("В команде %s получено полей: %d, нужно %s (поля разделяются символом |).\nФормат: %s",
		cmd, got, want, html.EscapeString(usage))
}

// AdminEmptyField: a required field is blank.
func AdminEmptyField(n int, name string) string {
	return fmt.Sprintf("Поле %d («%s») не заполнено, а оно обязательное.", n, name)
}

// AdminNameTooLong: partner name over the limit.
func AdminNameTooLong(got, max int) string {
	return fmt.Sprintf("Название партнёра слишком длинное: %d символов, максимум %d.", got, max)
}

// AdminBadURL: the partner link is not a usable URL.
func AdminBadURL(raw string) string {
	return fmt.Sprintf("Ссылка «%s» не подходит. Нужен адрес вида https://t.me/имя_канала (или @имя_канала), "+
		"он используется для кнопки «Подписаться».", html.EscapeString(raw))
}

// AdminBadChatID: chat_id is not a channel/group id.
func AdminBadChatID(raw string, isNumber bool) string {
	if !isNumber {
		return fmt.Sprintf("chat_id «%s» — не число. Это числовой идентификатор канала или группы, например -1001234567890. "+
			"Имя вида @channel сюда не подходит: Telegram проверяет подписку только по числовому id.", html.EscapeString(raw))
	}
	return fmt.Sprintf("chat_id %s не похож на id канала или группы: у каналов и групп он отрицательный "+
		"(например -1001234567890). Положительные числа — это личные аккаунты, подписку на них проверить нельзя.",
		html.EscapeString(raw))
}

// AdminDuplicatePartner: a partner with this name exists.
func AdminDuplicatePartner(name string, id int64) string {
	return fmt.Sprintf("Партнёр «%s» уже существует (id %d). Повторно не добавляю, чтобы не плодить дубли.",
		html.EscapeString(name), id)
}

// AdminBadDate: unparsable date.
func AdminBadDate(field, raw string) string {
	return fmt.Sprintf("%s «%s» не распознана. Нужен формат ГГГГ-ММ-ДД, например 2026-09-05.", field, html.EscapeString(raw))
}

// AdminBadNumber: unparsable positive integer.
func AdminBadNumber(field, raw string) string {
	return fmt.Sprintf("%s «%s» — не положительное целое число.", field, html.EscapeString(raw))
}

// AdminDayOutsideCampaign: date outside CAMPAIGN_START..CAMPAIGN_END.
func AdminDayOutsideCampaign(day, start, end time.Time) string {
	return fmt.Sprintf("Дата %s вне периода акции (%s — %s). Период задаётся переменными CAMPAIGN_START и CAMPAIGN_END.",
		day.Format("2006-01-02"), start.Format("2006-01-02"), end.Format("2006-01-02"))
}

// AdminPartnerNotFound: no partner with this id.
func AdminPartnerNotFound(id int64) string {
	return fmt.Sprintf("Партнёр с id %d не найден. Список партнёров: /partner_list", id)
}

// AdminDayLocked: the day was already drawn.
func AdminDayLocked(day time.Time, status string) string {
	return fmt.Sprintf("День %s уже в статусе «%s» — розыгрыш проведён, менять партнёра, победителей и призы нельзя.",
		day.Format("2006-01-02"), status)
}

// AdminDayPartnerInUse: replacing the partner would orphan users' actions.
func AdminDayPartnerInUse(day time.Time, entries, issues int64) string {
	return fmt.Sprintf("На %s уже есть действия пользователей: заявок в розыгрыше %d, выданных промокодов %d. "+
		"Сменить партнёра нельзя — они относятся к прежнему партнёру. Число победителей и призы менять можно.",
		day.Format("2006-01-02"), entries, issues)
}

// AdminDayNotFound: export of an unknown day.
func AdminDayNotFound(ref string) string {
	return fmt.Sprintf("День «%s» не найден среди назначенных. Назначить день: /day_set. Можно указать id дня или дату ГГГГ-ММ-ДД.",
		html.EscapeString(ref))
}

// AdminFinalDrawNotFound: export of an unknown final draw.
func AdminFinalDrawNotFound(id int64) string {
	return fmt.Sprintf("Финальный розыгрыш с id %d не найден.", id)
}

// AdminBadExportKind: unknown export type.
func AdminBadExportKind(raw string) string {
	return fmt.Sprintf("Неизвестный тип выгрузки «%s». Доступно: participants (участники дня), winners (победители дня для команды), "+
		"partner_winners (победители для партнёра), finalists (финалисты).\nФормат: %s", html.EscapeString(raw), html.EscapeString(UsageExport))
}

// AdminDaySet confirms /day_set.
func AdminDaySet(created bool, dayID int64, day time.Time, partner string, winners int32, prizes string) string {
	verb := "обновлён"
	if created {
		verb = "назначен"
	}
	return fmt.Sprintf("✅ День %s %s (id дня: %d).\nПартнёр: %s\nПобедителей: %d\nПризы: %s",
		day.Format("2006-01-02"), verb, dayID, html.EscapeString(partner), winners, orDash(prizes))
}

// AdminPartnerAdded confirms /partner_add.
func AdminPartnerAdded(id int64, name string) string {
	return fmt.Sprintf("✅ Партнёр «%s» добавлен, id %d.\nЧтобы бот мог проверять подписку, "+
		"сделайте бота администратором этого канала или группы.\nДальше: /promo_upload %d и /day_set.",
		html.EscapeString(name), id, id)
}

// PartnerLine is a partner row of /partner_list.
type AdminPartnerLine struct {
	ID         int64
	Name, URL  string
	ChatID     *int64
	Terms      string
	ValidUntil *time.Time
}

// AdminPartnerList renders /partner_list.
func AdminPartnerList(ps []AdminPartnerLine, loc *time.Location) string {
	if len(ps) == 0 {
		return "Партнёров пока нет. Добавить: /partner_add"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Партнёров: %d\n", len(ps))
	for _, p := range ps {
		fmt.Fprintf(&b, "\n<b>%d. %s</b>\nссылка: %s\n", p.ID, html.EscapeString(p.Name), orDash(html.EscapeString(p.URL)))
		if p.ChatID == nil {
			b.WriteString("chat_id: не задан ⚠️ (подписку проверить нельзя)\n")
		} else {
			fmt.Fprintf(&b, "chat_id: <code>%d</code>\n", *p.ChatID)
		}
		fmt.Fprintf(&b, "условия: %s\n", orDash(html.EscapeString(p.Terms)))
		if p.ValidUntil != nil {
			fmt.Fprintf(&b, "промокоды до: %s\n", p.ValidUntil.In(loc).Format("02.01.2006"))
		}
	}
	return b.String()
}

// AdminPromoStatsLine is a row of /promo_stats.
type AdminPromoStatsLine struct {
	ID          int64
	Name        string
	Free, Total int64
}

// AdminPromoStats renders /promo_stats.
func AdminPromoStats(rows []AdminPromoStatsLine) string {
	if len(rows) == 0 {
		return "Партнёров пока нет. Добавить: /partner_add"
	}
	var b strings.Builder
	b.WriteString("Остатки промокодов:\n\n")
	for _, r := range rows {
		warn := ""
		if r.Free == 0 {
			warn = " ⚠️ закончились"
		}
		fmt.Fprintf(&b, "%d. %s — свободно %d из %d (выдано %d)%s\n",
			r.ID, html.EscapeString(r.Name), r.Free, r.Total, r.Total-r.Free, warn)
	}
	return b.String()
}

// AdminPromoIssue is a rejected row of an uploaded CSV.
type AdminPromoIssue struct {
	Line   int
	Value  string
	Reason string
}

// AdminPromoReport is the outcome of a promo upload.
type AdminPromoReport struct {
	PartnerID     int64
	PartnerName   string
	Rows          int
	Loaded        int
	AlreadyInDB   int
	DupInFile     int
	Invalid       []AdminPromoIssue
	HeaderSkipped bool
}

const promoReportMaxIssues = 15

// AdminPromoUploaded renders the upload report.
func AdminPromoUploaded(r AdminPromoReport) string {
	var b strings.Builder
	icon := "✅"
	if r.Loaded == 0 {
		icon = "⚠️"
	}
	fmt.Fprintf(&b, "%s Загрузка промокодов для «%s» (id %d)\n\n", icon, html.EscapeString(r.PartnerName), r.PartnerID)
	fmt.Fprintf(&b, "Строк с кодами в файле: %d", r.Rows)
	if r.HeaderSkipped {
		b.WriteString(" (заголовок пропущен)")
	}
	fmt.Fprintf(&b, "\nЗагружено: %d\nОтброшено: %d\n", r.Loaded, r.AlreadyInDB+r.DupInFile+len(r.Invalid))
	if r.DupInFile > 0 {
		fmt.Fprintf(&b, "  • повторы внутри файла: %d\n", r.DupInFile)
	}
	if r.AlreadyInDB > 0 {
		fmt.Fprintf(&b, "  • уже были загружены ранее: %d\n", r.AlreadyInDB)
	}
	if len(r.Invalid) > 0 {
		fmt.Fprintf(&b, "  • некорректные: %d\n", len(r.Invalid))
		for i, is := range r.Invalid {
			if i == promoReportMaxIssues {
				fmt.Fprintf(&b, "    … и ещё %d\n", len(r.Invalid)-promoReportMaxIssues)
				break
			}
			fmt.Fprintf(&b, "    строка %d: «%s» — %s\n", is.Line, html.EscapeString(truncate(is.Value, 40)), is.Reason)
		}
	}
	if r.Loaded == 0 {
		b.WriteString("\nНи один код не загружен. Проверьте файл: один код в строке, без пробелов, не длиннее 64 символов.")
	}
	return b.String()
}

// Promo CSV validation reasons (shown inside the upload report).
const (
	PromoReasonEmpty     = "пустой код"
	PromoReasonSpaces    = "код содержит пробелы"
	PromoReasonBadChars  = "код содержит недопустимые символы"
	PromoReasonDuplicate = "повтор"
)

// PromoReasonTooLong: code over the limit.
func PromoReasonTooLong(got, max int) string {
	return fmt.Sprintf("слишком длинный код (%d символов, максимум %d)", got, max)
}

// AdminPromoMultiColumn: the CSV has more than one column.
func AdminPromoMultiColumn(line int) string {
	return fmt.Sprintf("В строке %d файла несколько столбцов. Ожидается один столбец: промокод в каждой строке "+
		"(первая строка может быть заголовком «code»). Номер партнёра указывается в подписи к файлу, а не в файле.", line)
}

// AdminPromoCSVSyntax: unparsable CSV.
func AdminPromoCSVSyntax(err error) string {
	return "Не удалось разобрать CSV: " + html.EscapeString(err.Error()) +
		". Проверьте, что файл в кодировке UTF-8 и в каждой строке один код."
}

// AdminPromoTooManyRows: file over the row limit.
func AdminPromoTooManyRows(max int) string {
	return fmt.Sprintf("В файле больше %d строк. Разбейте его на части и загрузите по очереди.", max)
}

// AdminPromoFileTooBig: file over the size limit.
func AdminPromoFileTooBig(got, max int64) string {
	return fmt.Sprintf("Файл слишком большой: %d КБ, максимум %d КБ.", (got+1023)/1024, max/1024)
}

// AdminPromoNeedCaption: a document without a usable /promo_upload caption.
func AdminPromoNeedCaption(caption string) string {
	if strings.TrimSpace(caption) == "" {
		return "Файл получен, но без подписи — не понимаю, чей это список. Отправьте файл ещё раз с подписью " +
			"<code>/promo_upload id_партнёра</code> (id смотрите в /partner_list)."
	}
	return fmt.Sprintf("Подпись «%s» не распознана. Нужна подпись <code>/promo_upload id_партнёра</code> (id смотрите в /partner_list).",
		html.EscapeString(caption))
}

// AdminExportDone describes a sent export file.
func AdminExportDone(kind string, rows int) string {
	return fmt.Sprintf("Выгрузка «%s»: строк %d.", kind, rows)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// AdminBadEditField: unknown field in /partner_edit.
func AdminBadEditField(raw string) string {
	return fmt.Sprintf("Неизвестное поле «%s». Можно менять: name (название), url (ссылка), chat_id (id канала), "+
		"terms (текст условий), promo_text (где действует промокод), valid_until (действует до, ГГГГ-ММ-ДД).\nФормат: %s",
		html.EscapeString(raw), html.EscapeString(UsagePartnerEdit))
}

// AdminPartnerEdited confirms /partner_edit with the old and new value.
func AdminPartnerEdited(id int64, name, field, oldVal, newVal string) string {
	return fmt.Sprintf("✅ Партнёр «%s» (id %d) обновлён.\n%s: %s → %s",
		html.EscapeString(name), id, field, orDash(html.EscapeString(oldVal)), orDash(html.EscapeString(newVal)))
}

// AdminPartnerNoChange: the new value equals the current one.
func AdminPartnerNoChange(id int64, field, val string) string {
	return fmt.Sprintf("У партнёра %d поле %s уже равно «%s», менять нечего.", id, field, html.EscapeString(val))
}

// AdminFieldNotClearable: a required field was given an empty value.
func AdminFieldNotClearable(field string) string {
	return fmt.Sprintf("Поле %s обязательное, его нельзя очистить. Очищаются только promo_text и valid_until.", field)
}

// Draw commands.
const (
	UsageDrawRun     = "/draw_run ГГГГ-ММ-ДД [force]"
	UsageDrawConfirm = "/draw_confirm ГГГГ-ММ-ДД"
)

// AdminDrawWinner is a winner line of the draw preview.
type AdminDrawWinner struct {
	TelegramID int64
	Username   string
	FirstName  string
	Prize      string
	Status     string
}

// AdminDrawView is the data of the draw preview.
type AdminDrawView struct {
	Date          time.Time
	Partner       string
	Status        string // campaign day status: drawn, confirmed, notified
	Already       bool   // the draw had been held before this request
	DrawnAt       time.Time
	Entries       int
	Excluded      int
	WinnersWanted int
	Winners       []AdminDrawWinner
}

// AdminDrawPreview renders the result of /draw_run (HTML).
func AdminDrawPreview(v AdminDrawView, loc *time.Location) string {
	date := v.Date.Format("2006-01-02")
	var b strings.Builder
	fmt.Fprintf(&b, "🎲 Розыгрыш дня %s — %s\n", date, html.EscapeString(v.Partner))
	if v.Already {
		b.WriteString("Розыгрыш уже был проведён ранее, результат зафиксирован и не меняется.\n")
	}
	fmt.Fprintf(&b, "Проведён: %s\n", v.DrawnAt.In(loc).Format("02.01.2006 15:04"))
	fmt.Fprintf(&b, "Заявок: %d, выбыло из-за отписки: %d\n", v.Entries, v.Excluded)
	fmt.Fprintf(&b, "Победителей: %d из %d\n", len(v.Winners), v.WinnersWanted)
	if len(v.Winners) < v.WinnersWanted {
		b.WriteString("⚠️ Победителей меньше заявленного: подписанных участников не хватило.\n")
	}
	if len(v.Winners) > 0 {
		b.WriteString("\n")
	}
	for i, w := range v.Winners {
		name := w.FirstName
		if name == "" {
			name = "без имени"
		}
		user := ""
		if w.Username != "" {
			user = "@" + w.Username + ", "
		}
		fmt.Fprintf(&b, "%d. %s (%sid %d) — %s\n", i+1, html.EscapeString(name), html.EscapeString(user), w.TelegramID, orDash(html.EscapeString(w.Prize)))
	}
	b.WriteString("\n")
	switch v.Status {
	case "drawn":
		fmt.Fprintf(&b, "Уведомления ещё НЕ отправлены. Проверьте список и подтвердите: <code>/draw_confirm %s</code>", date)
	case "confirmed":
		fmt.Fprintf(&b, "Результаты подтверждены, но не все уведомления доставлены. Повторить для недоставленных: <code>/draw_confirm %s</code>", date)
	default:
		b.WriteString("Результаты подтверждены, победители уведомлены. Выгрузки: /export winners и /export partner_winners.")
	}
	return b.String()
}

// AdminDrawFailure is a winner who could not be notified.
type AdminDrawFailure struct {
	TelegramID int64
	Reason     string
}

// AdminDrawConfirmed renders the result of /draw_confirm (HTML).
func AdminDrawConfirmed(date time.Time, partner string, winners, sent, already int, failed []AdminDrawFailure) string {
	d := date.Format("2006-01-02")
	var b strings.Builder
	if len(failed) == 0 {
		fmt.Fprintf(&b, "✅ Результаты дня %s (%s) подтверждены.\n", d, html.EscapeString(partner))
	} else {
		fmt.Fprintf(&b, "⚠️ Результаты дня %s (%s) подтверждены, но не все уведомления доставлены.\n", d, html.EscapeString(partner))
	}
	fmt.Fprintf(&b, "Победителей: %d. Уведомлено сейчас: %d", winners, sent)
	if already > 0 {
		fmt.Fprintf(&b, ", уже было уведомлено раньше: %d", already)
	}
	b.WriteString(".\n")
	for _, f := range failed {
		fmt.Fprintf(&b, "• id %d: %s\n", f.TelegramID, html.EscapeString(f.Reason))
	}
	if len(failed) > 0 {
		fmt.Fprintf(&b, "\nЧаще всего это значит, что пользователь заблокировал бота. Повторить отправку недоставленным: <code>/draw_confirm %s</code>", d)
	}
	return b.String()
}

// AdminDrawNoDay: no campaign day on the date.
func AdminDrawNoDay(date time.Time) string {
	return fmt.Sprintf("На дату %s не назначен партнёр, розыгрывать нечего. Назначить: /day_set", date.Format("2006-01-02"))
}

// AdminDrawNotOver: the day is still running.
func AdminDrawNotOver(date time.Time) string {
	return fmt.Sprintf("День %s ещё не закончился: заявки продолжают приходить, а после розыгрыша приём закрывается. "+
		"Дождитесь конца дня или проведите розыгрыш досрочно: <code>/draw_run %s force</code>",
		date.Format("2006-01-02"), date.Format("2006-01-02"))
}

// AdminDrawInFuture: the day has not started.
func AdminDrawInFuture(date time.Time) string {
	return fmt.Sprintf("День %s ещё не наступил, заявок на него быть не может. Розыгрыш невозможен даже с force.", date.Format("2006-01-02"))
}

// AdminDrawNotRun: confirm before the draw.
func AdminDrawNotRun(date time.Time) string {
	return fmt.Sprintf("Розыгрыш дня %s ещё не проводился. Сначала: <code>/draw_run %s</code>", date.Format("2006-01-02"), date.Format("2006-01-02"))
}

// AdminDrawCheckFailed: Telegram could not verify winners' subscriptions.
func AdminDrawCheckFailed(err error) string {
	return "Не удалось проверить подписку кандидатов в победители: " + html.EscapeString(err.Error()) +
		"\nРозыгрыш НЕ проведён, ничего не изменилось. Проверьте, что бот — администратор канала партнёра " +
		"(/partner_list покажет chat_id), и повторите команду."
}

// AdminResultsNotConfirmed: partner export before confirmation.
func AdminResultsNotConfirmed(date string, status string) string {
	return fmt.Sprintf("Результаты дня %s ещё не подтверждены (статус «%s»). Выгрузка для партнёра доступна после /draw_confirm.", date, status)
}

// Draw-related lines of the admin menu.
const (
	menuDrawRun     = "провести розыгрыш дня и показать превью (победителям пока ничего не уходит). force — досрочно, в день розыгрыша"
	menuDrawConfirm = "подтвердить результаты и отправить уведомления победителям"
)

// Final draw commands.
const (
	UsageFinalRun     = "/final_run [force]"
	UsageFinalConfirm = "/final_confirm"
)

// Final draw lines of the admin menu.
const (
	menuFinalRun     = "провести финальный розыгрыш и показать превью (победителям пока ничего не уходит). Проводится один раз; force — до конца акции"
	menuFinalConfirm = "подтвердить финал и отправить уведомления победителям (резервистам не отправляется)"
)

// AdminFinalLine is a winner or reserve line of the final draw preview.
type AdminFinalLine struct {
	TelegramID int64
	Username   string
	FirstName  string
	Tickets    int
	Reserve    bool
	Position   int
}

// AdminFinalView is the data of the final draw preview.
type AdminFinalView struct {
	ID             int64
	Status         string
	Already        bool
	HeldAt         time.Time
	HeldBy         int64
	DrumTickets    int
	DrumUsers      int
	WinnersWanted  int
	ReservesWanted int
	PrizeTickets   int
	Lines          []AdminFinalLine
}

// AdminFinalPreview renders the result of /final_run (HTML).
func AdminFinalPreview(v AdminFinalView, loc *time.Location) string {
	var b strings.Builder
	b.WriteString("🏆 Финальный розыгрыш\n")
	if v.Already {
		b.WriteString("Розыгрыш уже был проведён ранее, результат зафиксирован и не меняется.\n")
	}
	by := "планировщик"
	if v.HeldBy != 0 {
		by = fmt.Sprintf("админ %d", v.HeldBy)
	}
	fmt.Fprintf(&b, "Проведён: %s (%s)\n", v.HeldAt.In(loc).Format("02.01.2006 15:04"), by)
	fmt.Fprintf(&b, "В барабане: %d билетов у %d участников\n", v.DrumTickets, v.DrumUsers)

	winners, reserves := 0, 0
	for _, l := range v.Lines {
		if l.Reserve {
			reserves++
		} else {
			winners++
		}
	}
	fmt.Fprintf(&b, "Победителей: %d из %d (призовых билетов на победителя: %d), резерв: %d из %d\n", winners, v.WinnersWanted, v.PrizeTickets, reserves, v.ReservesWanted)
	if winners < v.WinnersWanted {
		b.WriteString("⚠️ Победителей меньше заявленного: участников с шансами не хватило.\n")
	}

	line := func(i int, l AdminFinalLine) string {
		name := l.FirstName
		if name == "" {
			name = "без имени"
		}
		user := ""
		if l.Username != "" {
			user = "@" + l.Username + ", "
		}
		return fmt.Sprintf("%d. %s (%sid %d) — билетов в барабане: %d\n", i, html.EscapeString(name), html.EscapeString(user), l.TelegramID, l.Tickets)
	}
	n := 0
	b.WriteString("\n<b>Победители</b>\n")
	for _, l := range v.Lines {
		if !l.Reserve {
			n++
			b.WriteString(line(n, l))
		}
	}
	if reserves > 0 {
		b.WriteString("\n<b>Резерв (очередь)</b>\n")
		for _, l := range v.Lines {
			if l.Reserve {
				b.WriteString(line(l.Position, l))
			}
		}
	}
	b.WriteString("\n")
	switch v.Status {
	case "drawn":
		b.WriteString("Уведомления ещё НЕ отправлены. Проверьте список и подтвердите: <code>/final_confirm</code>")
	case "confirmed":
		b.WriteString("Результаты подтверждены, но не все уведомления доставлены. Повторить для недоставленных: <code>/final_confirm</code>")
	default:
		fmt.Fprintf(&b, "Результаты подтверждены, победители уведомлены. Выгрузка: <code>/export finalists %d</code>", v.ID)
	}
	return b.String()
}

// AdminFinalConfirmed renders the result of /final_confirm (HTML).
func AdminFinalConfirmed(winners, sent, already int, failed []AdminDrawFailure) string {
	var b strings.Builder
	if len(failed) == 0 {
		b.WriteString("✅ Финальный розыгрыш подтверждён.\n")
	} else {
		b.WriteString("⚠️ Финальный розыгрыш подтверждён, но не все уведомления доставлены.\n")
	}
	fmt.Fprintf(&b, "Победителей: %d. Уведомлено сейчас: %d", winners, sent)
	if already > 0 {
		fmt.Fprintf(&b, ", уже было уведомлено раньше: %d", already)
	}
	b.WriteString(".\nРезервистам уведомления не отправляются: к ним обращаются, если победитель не ответит вовремя.\n")
	for _, f := range failed {
		fmt.Fprintf(&b, "• id %d: %s\n", f.TelegramID, html.EscapeString(f.Reason))
	}
	if len(failed) > 0 {
		b.WriteString("\nЧаще всего это значит, что пользователь заблокировал бота. Повторить отправку недоставленным: <code>/final_confirm</code>")
	}
	return b.String()
}

// AdminFinalNotOver: the campaign is still running.
func AdminFinalNotOver(end time.Time) string {
	return fmt.Sprintf("Акция ещё не закончилась (последний день — %s): пользователи продолжают копить шансы, "+
		"а финальный розыгрыш проводится один раз и не пересчитывается. Дождитесь конца акции или проведите его досрочно: <code>/final_run force</code>",
		end.Format("2006-01-02"))
}

// AdminFinalNoTickets: nobody has a chance.
const AdminFinalNoTickets = "В барабане нет ни одного билета: ни у одного пользователя нет шансов. Розыгрыш не проведён и не записан."

// AdminFinalNotRun: confirm before the draw.
const AdminFinalNotRun = "Финальный розыгрыш ещё не проводился. Сначала: <code>/final_run</code>"

// Winners and personal data commands.
const (
	UsageWinners       = "/winners ГГГГ-ММ-ДД|final [csv]"
	UsagePurgeContacts = "/purge_contacts [confirm]"
)

// Winners/purge lines of the admin menu.
const (
	menuWinners = "статусы победителей дня или финала; с csv — выгрузка (для финала: контакты тех, кто дал согласие)"
	menuPurge   = "удалить собранные имена победителей после окончания акции (без confirm — только показать, сколько их)"
)

// AdminWinnerLine is a row of the /winners view.
type AdminWinnerLine struct {
	TelegramID int64
	Username   string
	FirstName  string
	Role       string
	Status     string
}

// AdminWinnersView is the data of /winners.
type AdminWinnersView struct {
	Title string
	Lines []AdminWinnerLine
	Final bool
	ID    int64
}

// AdminWinners renders the /winners view (HTML). Names given by the winners
// are deliberately not shown here: they are in the csv export only.
func AdminWinners(v AdminWinnersView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", html.EscapeString(v.Title))
	if len(v.Lines) == 0 {
		b.WriteString("Победителей нет.\n")
	}
	for i, l := range v.Lines {
		name := l.FirstName
		if name == "" {
			name = "без имени"
		}
		user := ""
		if l.Username != "" {
			user = "@" + l.Username + ", "
		}
		fmt.Fprintf(&b, "\n%d. %s (%sid %d) — %s\n   %s\n", i+1, html.EscapeString(name), html.EscapeString(user), l.TelegramID, l.Role, l.Status)
	}
	if v.Final {
		b.WriteString("\nКонтакты: <code>/winners final csv</code>")
	}
	return b.String()
}

// DailyWinnerStatus describes a daily winner status for admins.
func DailyWinnerStatus(status string) string {
	switch status {
	case "selected":
		return "выбран, ещё не уведомлён"
	case "notified":
		return "уведомлён"
	case "claimed":
		return "приз получен"
	case "forfeited":
		return "отказ"
	}
	return status
}

// FinalWinnerStatus describes a final winner for admins.
func FinalWinnerStatus(status, step, forfeitReason string, reserve, promoted, purged bool, position int, deadline, consentAt time.Time, loc *time.Location) (role, text string) {
	role = "победитель"
	if reserve {
		role = fmt.Sprintf("резерв №%d", position)
		if promoted {
			role += " (получил приз по очереди)"
		}
	}
	switch status {
	case "selected":
		if reserve {
			return role, "ждёт своей очереди"
		}
		return role, "выбран, ещё не уведомлён"
	case "notified":
		switch step {
		case "name":
			text = "вводит имя и фамилию"
		case "consent":
			text = "имя получено, ждём согласие"
		default:
			text = "уведомлён, ещё не отвечал"
		}
		return role, fmt.Sprintf("%s, срок ответа до %s", text, deadline.In(loc).Format("02.01 15:04"))
	case "contact_received":
		text = fmt.Sprintf("согласие и данные получены (%s)", consentAt.In(loc).Format("02.01.2006 15:04"))
		if purged {
			text += ", имя удалено"
		}
		return role, text
	case "forfeited":
		switch forfeitReason {
		case "expired":
			return role, "срок истёк, приз передан дальше"
		case "declined":
			return role, "отказался от передачи данных, приз передан дальше"
		case "undeliverable":
			return role, "не удалось написать (бот заблокирован), пропущен"
		}
		return role, "отказ"
	}
	return role, status
}

// AdminPurgeDryRun reports how many names are stored.
func AdminPurgeDryRun(stored int) string {
	if stored == 0 {
		return "Собранных имён нет, удалять нечего."
	}
	return fmt.Sprintf("Сохранено имён победителей: %d.\nСначала выгрузите контакты (<code>/winners final csv</code>) — после удаления вернуть их будет нельзя. "+
		"Согласия (дата и время) и статусы останутся. Удалить: <code>/purge_contacts confirm</code>", stored)
}

// AdminPurged confirms the purge.
func AdminPurged(purged int) string {
	return fmt.Sprintf("✅ Удалено имён: %d. Записи о согласиях и статусы призов сохранены.", purged)
}

// AdminPurgeTooEarly: the campaign is not over.
func AdminPurgeTooEarly(end time.Time) string {
	return fmt.Sprintf("Контакты можно удалять только после окончания акции (последний день — %s): до этого они нужны для вручения призов.", end.Format("2006-01-02"))
}

// AdminNoFinalDraw: /winners final before the final draw.
const AdminNoFinalDraw = "Финальный розыгрыш ещё не проводился: /final_run"
