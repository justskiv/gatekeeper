// Package messages keeps user-visible Telegram texts in one place.
package messages

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/justskiv/gatekeeper/internal/domain"
)

const (
	CommandStartDescription     = "начать работу"
	CommandHelpDescription      = "справка"
	CommandBoostyDescription    = "оформить подписку Boosty"
	CommandTributeDescription   = "оформить подписку Tribute"
	CommandHereDescription      = "показать ID чата"
	CommandStatusDescription    = "показать статус подписки"
	CommandWhoisDescription     = "показать карточку пользователя"
	CommandGrantDescription     = "выдать ручной доступ"
	CommandRevokeDescription    = "отозвать ручной доступ"
	CommandBanDescription       = "заблокировать пользователя"
	CommandUnbanDescription     = "снять блокировку"
	CommandSyncDescription      = "запустить сверку доступа"
	CommandStatsDescription     = "показать сводку состояния"
	CommandAlertsDescription    = "показать открытые тревоги"
	CommandExportDescription    = "выгрузить пользователей CSV"
	CommandChatsDescription     = "показать настроенные чаты"
	CommandHelpAdminDescription = "справка владельца"

	RetryAccessCallbackData = "grant_access.retry"
	RetryAccessButtonText   = "Проверить ещё раз"
	GetAccessButtonText     = "Получить доступ"
	AdminConfirmButtonText  = "Подтвердить"
	AdminCancelButtonText   = "Отмена"

	// Custom emoji ids from the Decoration_Pack set, used as minimalist
	// status signals. Rendering requires the bot owner's Telegram Premium;
	// the plain-emoji fallback covers any client that cannot display them.
	emojiDotGreen    = "5215584915898243758"
	emojiDotOrange   = "5215200271512117515"
	emojiGopherHeart = "5294107689847119376"
)

// SubscribeLinks carries the deployment-specific sign-up URLs shown to users
// without active access. They flow in from configuration so the personal links
// are not hardcoded. TributeEUR is optional: an empty value omits the euro
// billing entry point.
type SubscribeLinks struct {
	Boosty     string
	TributeRUB string
	TributeEUR string
}

// InviteLinkLine is one managed resource row shown to a user. A resource the
// user must still join carries its join URL; one already joined is marked with
// Joined so the same list can show both states in a consistent order.
type InviteLinkLine struct {
	Resource domain.Resource
	URL      string
	Joined   bool
}

// AuditLine is the audit data needed for owner-facing /whois text.
type AuditLine struct {
	Kind      string
	Source    string
	Detail    string
	CreatedAt time.Time
}

// WhoisData is the owner-facing user card payload.
type WhoisData struct {
	User          domain.User
	Subscriptions []domain.Subscription
	Grants        []domain.AccessGrant
	Whitelisted   bool
	Decision      domain.AccessDecision
	Audit         []AuditLine
}

// NamedCount is one named count in owner ops summaries.
type NamedCount struct {
	Name  string
	Count int
}

// NamedValue is one named string value in owner ops summaries.
type NamedValue struct {
	Name  string
	Value string
}

// GrantCount is an access-grant count in owner ops summaries.
type GrantCount struct {
	Resource string
	State    string
	Count    int
}

// OutboxCount is an outbox count in owner ops summaries.
type OutboxCount struct {
	Status string
	Count  int
}

// OpsStatsData is the owner-facing /stats payload.
type OpsStatsData struct {
	ActiveSubscriptions []NamedCount
	Grants              []GrantCount
	PendingRevocations  int
	DueRevocations      int
	Health              []NamedValue
	ReconcileLastRunAt  *time.Time
	Outbox              []OutboxCount
	OpenAlerts          int
}

// OpsAlertData is one owner-facing alert line.
type OpsAlertData struct {
	ID        int64
	Severity  string
	Kind      string
	Title     string
	CreatedAt time.Time
}

// ChatRoleData is one configured chat role. Title is the live Telegram chat
// title when it could be resolved; empty otherwise.
type ChatRoleData struct {
	Role   string
	ChatID int64
	Title  string
}

// subscribeOptions names the paid subscription offerings, each pointing at its
// in-bot page command. Telegram makes /boosty and /tribute tappable, and a
// distinct command (not a /start deep link) keeps the visible command honest:
// the chat shows /boosty or /tribute, not a bare /start with a hidden payload.
func subscribeOptions() string {
	return "Boosty (/boosty) или Tribute (/tribute)"
}

// renewOptions is the actionable "where to renew" line shared by access-lapse
// notices, naming both subscription entry points so the user can act without
// hunting for the commands.
func renewOptions() string {
	return "Продлить подписку: " + subscribeOptions() + "."
}

// Boosty is the /boosty page: where to subscribe and the chat-membership
// requirement the bot verifies against.
func Boosty(links SubscribeLinks) string {
	return strings.Join([]string{
		"<b>Подписка Boosty</b>",
		"",
		"Оформи подписку: " + SafeLink(links.Boosty, "Boosty"),
		"",
		"Для проверки доступа нужно состоять в чате Boosty — " +
			"бот сверяет подписку через него.",
		"",
		"Оформил? Отправь /start — проверю доступ.",
	}, "\n")
}

// Tribute is the /tribute page: ruble and euro options and the channel
// subscription requirement the bot verifies against.
func Tribute(links SubscribeLinks) string {
	intro := "Оплата в рублях:"
	if links.TributeEUR != "" {
		intro = "Оплата в рублях или евро:"
	}

	lines := []string{
		"<b>Подписка Tribute</b>",
		"",
		intro,
		"",
		"• " + SafeLink(links.TributeRUB, "Рубли"),
	}

	if links.TributeEUR != "" {
		lines = append(lines, "• "+SafeLink(links.TributeEUR, "Евро"))
	}

	lines = append(lines,
		"",
		"После оформления подписки надо подписаться на Tribute-канал — "+
			"тогда бот подтвердит доступ.",
		"",
		"Оформил? Отправь /start — проверю доступ.",
	)

	return strings.Join(lines, "\n")
}

// Welcome returns the /start greeting.
func Welcome() string {
	return strings.Join([]string{
		"<b>Привет</b>",
		"",
		"Я проверю твою подписку и помогу попасть в закрытые чат и канал.",
		"",
		Italic("Важно: пиши с того же аккаунта Telegram, которым оформлял подписку."),
		"",
		"Чтобы проверить доступ, нажми /status или отправь /start ещё раз.",
	}, "\n")
}

// NoSub is the /start reply when the user has no active subscription.
// It is rendered alongside the «Проверить ещё раз» inline button, so the
// copy can reference it directly.
func NoSub() string {
	return strings.Join([]string{
		statusDotOrange() + "<b>Активная подписка не найдена</b>",
		"",
		"Чтобы получить доступ к закрытым ресурсам, оформи подписку на " +
			subscribeOptions() + ".",
		"",
		"После оплаты нажми «" + RetryAccessButtonText + "» — с того же " +
			"аккаунта Telegram, которым оформлял подписку.",
	}, "\n")
}

// ActiveShared returns the active admission response. It lists every managed
// resource in a stable order: those still to join carry a «вступить» link,
// those already joined show their membership state, so the user sees the whole
// picture rather than only what is missing.
func ActiveShared(links []InviteLinkLine) string {
	var b strings.Builder

	b.WriteString(statusDotGreen() + "<b>Доступ активен</b>")
	b.WriteString("\n\nВступай в клубные ресурсы:\n")

	for _, link := range links {
		name := Escape(capitalizeFirst(resourceText(link.Resource)))

		switch {
		case link.Joined:
			fmt.Fprintf(&b, "\n• %s — %s", name,
				Escape(resourceMembershipText(link.Resource, domain.GrantJoined)))
		case link.URL != "":
			fmt.Fprintf(&b, "\n• %s — %s", name, SafeLink(link.URL, "вступить"))
		}
	}

	return b.String()
}

// ActiveDirect returns the active response for direct-invite mode.
func ActiveDirect() string {
	return statusDotGreen() + "<b>Доступ активен</b>" +
		"\n\n" +
		"Готовлю персональные ссылки для входа — пришлю их сюда, как только будут готовы."
}

// InviteSoon tells the user that personal links are being prepared.
func InviteSoon() string {
	return statusDotGreen() + "<b>Доступ активен</b>" +
		"\n\n" +
		"Сейчас отправлю тебе персональные ссылки для входа в закрытые ресурсы."
}

// Granted confirms that a join request was approved.
func Granted() string {
	return statusGopherHeart() + "<b>Доступ подтверждён</b>" +
		"\n\n" +
		"Заявка одобрена — добро пожаловать в клуб."
}

// CheckingSubscription is the inline-button label shown while a retry-access
// request runs its slow subscription check; the message body stays as is.
func CheckingSubscription() string {
	return "⏳ Проверяем…"
}

// TryLater asks the user to retry after a temporary check failure.
func TryLater() string {
	return statusDotOrange() + "<b>Не удалось проверить подписку</b>" +
		"\n\n" +
		"Похоже, проверка на нашей стороне временно недоступна. Попробуй чуть позже."
}

// InviteNotRecognized explains that a join request used an invite link the
// bot can no longer match to the user. It is an invite problem, not a
// subscription one, so the copy points the user back to /start for fresh links.
func InviteNotRecognized() string {
	return statusDotOrange() + "<b>Не удалось распознать ссылку</b>" +
		"\n\n" +
		"Эта ссылка-приглашение не подтверждается. Запроси доступ заново " +
		"через /start — я выдам свежие ссылки."
}

// Banned explains a manual hard-ban.
func Banned() string {
	return statusDotOrange() + "<b>Доступ заблокирован</b>" +
		"\n\n" +
		"Если думаешь, что это ошибка, напиши владельцу."
}

// AlreadyIn tells the user that every managed resource is already joined,
// itemizing each one with its membership state instead of a flat sentence.
func AlreadyIn(resources []domain.Resource) string {
	var b strings.Builder

	b.WriteString(statusDotGreen() + "<b>Доступ уже выдан</b>")
	b.WriteString("\n\nТы уже во всех клубных ресурсах:\n")

	for _, resource := range resources {
		fmt.Fprintf(&b, "\n• %s — %s",
			Escape(capitalizeFirst(resourceText(resource))),
			Escape(resourceMembershipText(resource, domain.GrantJoined)))
	}

	return b.String()
}

// PersonalInviteMisused explains that an invite belongs to another account.
func PersonalInviteMisused() string {
	return statusDotOrange() + "<b>Ссылка не для твоего аккаунта</b>" +
		"\n\n" +
		"Запроси доступ со своего Telegram-аккаунта."
}

// Help returns the /help text.
func Help() string {
	return strings.Join([]string{
		"<b>Как это работает</b>",
		"",
		"Бот проверяет твою подписку и выдаёт доступ в закрытые чат и канал.",
		"",
		"<b>Важно:</b> пиши с того же аккаунта Telegram, которым оформлял подписку.",
		"",
		"<b>Оформить подписку</b>",
		"",
		"Для доступа к закрытым ресурсам оформи подписку на " + subscribeOptions() + ".",
		"",
		"<b>Команды</b>",
		"",
		"/start — запросить доступ",
		"/status — проверить подписку и членство",
		"/help — открыть эту справку",
	}, "\n")
}

// Here returns a chat discovery response for owners.
func Here(chatID int64, chatType string) string {
	return fmt.Sprintf("<b>Этот чат</b>\n\n• ID: %s\n• Тип: %s",
		Code(strconv.FormatInt(chatID, 10)), Code(chatType))
}

// Status returns the /status response. graceUntil, when set, is the deadline
// up to which access is kept after a subscription lapsed; it lets /status echo
// the same grace-period notice the user got proactively, instead of pretending
// there is no access at all. offerAccessButton tells the renderer that the
// caller attaches a «Получить доступ» inline button, so the textual "/start"
// call-to-action footer is dropped in favour of the button.
func Status(
	decision domain.AccessDecision,
	subscriptions []domain.Subscription,
	grants []domain.AccessGrant,
	graceUntil *time.Time,
	offerAccessButton bool,
) string {
	var b strings.Builder

	switch decision.Status {
	case domain.StatusActive:
		b.WriteString(statusDotGreen() + "<b>Доступ активен</b>")
	case domain.StatusUnknown:
		b.WriteString(statusDotOrange() + "<b>Проверяем доступ</b>")
	default:
		b.WriteString(statusDotOrange() + "<b>Активная подписка не найдена</b>")
	}

	b.WriteString("\n\n")

	if len(subscriptions) == 0 {
		switch decision.Status {
		case domain.StatusActive:
			b.WriteString("Доступ активен. Активная подписка в подключённых источниках не показана.")
		case domain.StatusUnknown:
			b.WriteString("Проверка временно недоступна. Попробуй позже.")
		default:
			if graceUntil != nil {
				fmt.Fprintf(&b, "Доступ сохранён до %s — без активной "+
					"подписки он будет отозван.\n\n%s",
					Escape(dateTimeText(*graceUntil)), renewOptions())
			} else {
				b.WriteString("Для получения доступа к закрытым ресурсам оформи подписку на " +
					subscribeOptions() + ". Подробнее: /help")
			}
		}
	} else {
		b.WriteString("<b>Действующие подписки:</b>\n")

		for _, sub := range subscriptions {
			fmt.Fprintf(&b, "\n• %s", Escape(platformText(sub.Platform)))

			if sub.ExpiresAt != nil {
				fmt.Fprintf(&b, " до %s", Escape(dateText(*sub.ExpiresAt)))
			}

			if sub.Tier != "" {
				fmt.Fprintf(&b, " (%s)", Escape(sub.Tier))
			}
		}
	}

	b.WriteString("\n\n<b>Клубные ресурсы:</b>\n")

	if len(grants) == 0 {
		b.WriteString("\n• доступ ещё не выдавался")
	} else {
		for _, grant := range grants {
			fmt.Fprintf(&b, "\n• %s — %s",
				Escape(capitalizeFirst(resourceText(grant.Resource))),
				Escape(resourceMembershipText(grant.Resource, grant.State)))
		}
	}

	if !offerAccessButton && graceUntil == nil {
		b.WriteString("\n\nДля получения доступа отправь команду /start")
	}

	return strings.TrimSpace(b.String())
}

// AccessKept returns the grace-period cancellation notification.
func AccessKept() string {
	return statusGopherHeart() + "<b>Доступ снова активен</b>" +
		"\n\n" +
		"Запланированный отзыв доступа отменён — делать ничего не нужно."
}

// ExpiryWarning warns a user that access will be revoked after grace.
func ExpiryWarning(until time.Time) string {
	return fmt.Sprintf(
		statusDotOrange()+"<b>Активная подписка не найдена</b>"+
			"\n\n"+
			"Доступ сохранён до %s — без активной подписки он будет отозван."+
			"\n\n"+renewOptions(),
		Escape(dateTimeText(until)),
	)
}

// ExpiredNotice informs a user about inactive access in notify-only mode.
func ExpiredNotice() string {
	return statusDotOrange() + "<b>Активная подписка не найдена</b>" +
		"\n\n" +
		"Доступ пока сохранён, но без активной подписки он будет отозван." +
		"\n\n" + renewOptions()
}

// Revoked informs a user that club access was revoked.
func Revoked() string {
	return statusDotOrange() + "<b>Доступ в клуб отозван</b>" +
		"\n\n" +
		"Чтобы вернуться, продли подписку и отправь /start." +
		"\n\n" + renewOptions()
}

// AdminConfirm renders a compact owner confirmation prompt.
func AdminConfirm(summary string) string {
	return "⚠️ <b>Подтвердите действие</b>\n\n" + summary
}

// AdminConfirmed renders a successful owner action result.
func AdminConfirmed(summary string) string {
	return statusDotGreen() + "<b>Готово</b>\n\n" + summary
}

// AdminCancelled reports a cancelled confirmation.
func AdminCancelled() string {
	return "<b>Действие отменено.</b>"
}

// AdminConfirmationExpired reports an expired confirmation.
func AdminConfirmationExpired() string {
	return "⏳ <b>Действие устарело</b>\n\nПодтверждение живёт 5 минут."
}

// AdminConfirmationInProgress reports a confirmation already being executed.
func AdminConfirmationInProgress() string {
	return "⏳ <b>Действие уже выполняется.</b>"
}

// AdminSyncUnavailable reports a temporary missing sync dependency.
func AdminSyncUnavailable() string {
	return "❔ <b>Сверка сейчас недоступна.</b>"
}

// AdminActionAllUsers renders a full-sync action target.
func AdminActionAllUsers() string {
	return "все пользователи"
}

// AdminActionTargetID renders one owner command target.
func AdminActionTargetID(tgID int64) string {
	return Code(strconv.FormatInt(tgID, 10))
}

// AdminActionSummary renders the stable confirmation summary header.
func AdminActionSummary(kind, target string) string {
	return fmt.Sprintf("Действие: %s\nЦель: %s",
		Escape(adminActionKindText(kind)), target)
}

// AdminActionExpiryLine renders an admin action expiry summary line.
func AdminActionExpiryLine(until time.Time) string {
	return "Срок: " + Escape(dateTimeText(until))
}

// AdminActionReasonLine renders an admin action reason summary line.
func AdminActionReasonLine(reason string) string {
	return "Причина: " + Code(reason)
}

func adminActionKindText(kind string) string {
	switch kind {
	case "grant":
		return "выдать доступ"
	case "revoke":
		return "отозвать ручной доступ"
	case "ban":
		return "заблокировать пользователя"
	case "unban":
		return "снять блокировку"
	case "sync":
		return "запустить сверку"
	default:
		return kind
	}
}

// AdminCommandUsage returns a short usage hint for owner commands.
func AdminCommandUsage(command string) string {
	switch command {
	case "grant":
		return "Используйте: " + Code("/grant <tg_id|@username> [срок] [причина]")
	case "revoke":
		return "Используйте: " + Code("/revoke <tg_id|@username> [причина]")
	case "ban":
		return "Используйте: " + Code("/ban <tg_id|@username> [причина]")
	case "unban":
		return "Используйте: " + Code("/unban <tg_id|@username> [причина]")
	case "sync":
		return "Используйте: " + Code("/sync [tg_id|@username]")
	default:
		return "<b>Команда указана неверно.</b>"
	}
}

// AdminHelp returns the owner/admin command reference.
func AdminHelp() string {
	return strings.Join([]string{
		"<b>Команды владельца</b>",
		"",
		"<b>Поиск</b>",
		Code("/here") + " — показать ID текущего чата",
		Code("/whois <tg_id|@username>") + " — карточка пользователя",
		"",
		"<b>Доступ</b>",
		Code("/grant <tg_id|@username> [срок] [причина]") + " — выдать доступ",
		Code("/revoke <tg_id|@username> [причина]") + " — отозвать ручной доступ",
		Code("/ban <tg_id|@username> [причина]") + " — заблокировать доступ",
		Code("/unban <tg_id|@username> [причина]") + " — снять блокировку",
		"",
		"<b>Операции</b>",
		Code("/sync [tg_id|@username]") + " — запустить сверку",
		Code("/stats") + " — сводка состояния",
		Code("/alerts") + " — открытые тревоги",
		Code("/chats") + " — настроенные роли чатов",
		Code("/export") + " — CSV users/subscriptions только в личке",
	}, "\n")
}

// OpsUnavailable reports missing read-model dependencies.
func OpsUnavailable() string {
	return "❔ <b>Операционная сводка сейчас недоступна.</b>"
}

// OpsStats renders the owner-facing /stats summary.
//
//nolint:wsl_v5 // String-builder formatting is clearer without extra gaps.
func OpsStats(data OpsStatsData) string {
	var b strings.Builder
	b.WriteString("<b>Сводка состояния</b>\n")
	b.WriteString("Рутинный статус. Действие не требуется, если ниже нет ошибок.\n")

	b.WriteString("\n<b>Подписки</b>\n")
	if len(data.ActiveSubscriptions) == 0 {
		b.WriteString("• нет активных записей\n")
	} else {
		for _, count := range data.ActiveSubscriptions {
			fmt.Fprintf(&b, "• %s: %s\n",
				Escape(count.Name), Code(strconv.Itoa(count.Count)))
		}
	}

	b.WriteString("\n<b>Доступы</b>\n")
	if len(data.Grants) == 0 {
		b.WriteString("• нет\n")
	} else {
		for _, count := range data.Grants {
			fmt.Fprintf(&b, "• %s / %s: %s\n",
				Escape(resourceText(domain.Resource(count.Resource))),
				Escape(grantStateText(domain.GrantState(count.State))),
				Code(strconv.Itoa(count.Count)))
		}
	}

	fmt.Fprintf(&b, "\n<b>Отзывы доступа</b>\n• ожидают: %s\n• готовы: %s\n",
		Code(strconv.Itoa(data.PendingRevocations)),
		Code(strconv.Itoa(data.DueRevocations)))

	b.WriteString("\n<b>Health</b>\n")
	if len(data.Health) == 0 {
		b.WriteString("• нет данных\n")
	} else {
		for _, health := range data.Health {
			fmt.Fprintf(&b, "• %s: %s\n",
				Escape(health.Name), Code(health.Value))
		}
	}

	b.WriteString("\n<b>Сверка</b>\n")
	if data.ReconcileLastRunAt == nil {
		b.WriteString("• последний запуск: нет данных\n")
	} else {
		fmt.Fprintf(&b, "• последний запуск: %s\n",
			Code(data.ReconcileLastRunAt.UTC().Format(time.RFC3339)))
	}

	pendingOutbox := 0
	deadOutbox := 0
	for _, count := range data.Outbox {
		if count.Status == "queued" || count.Status == "running" {
			pendingOutbox += count.Count
		}
		if count.Status == "dead" {
			deadOutbox += count.Count
		}
	}

	b.WriteString("\n<b>Outbox</b>\n")
	fmt.Fprintf(&b, "• ожидают: %s\n• мёртвые: %s\n",
		Code(strconv.Itoa(pendingOutbox)), Code(strconv.Itoa(deadOutbox)))

	fmt.Fprintf(&b, "\n<b>Открытые тревоги</b>: %s",
		Code(strconv.Itoa(data.OpenAlerts)))

	return b.String()
}

// OpsAlerts renders the owner-facing /alerts response.
//
//nolint:wsl_v5 // String-builder formatting is clearer without extra gaps.
func OpsAlerts(alerts []OpsAlertData) string {
	if len(alerts) == 0 {
		return statusDotGreen() + "<b>Открытых тревог нет</b>" +
			"\n\nДействие не требуется."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "<b>Открытые тревоги (%s)</b>\n",
		Code(strconv.Itoa(len(alerts))))
	for _, alert := range alerts {
		fmt.Fprintf(&b, "\n%s <b>%s</b>\n",
			severityGlyph(alert.Severity), Escape(alert.Title))
		fmt.Fprintf(&b, "• ID: %s\n", Code(strconv.FormatInt(alert.ID, 10)))
		fmt.Fprintf(&b, "• Тип: %s\n", Code(alert.Kind))
		fmt.Fprintf(&b, "• Создана: %s\n",
			Code(alert.CreatedAt.UTC().Format(time.RFC3339)))
	}

	return strings.TrimSpace(b.String())
}

// OpsChats renders configured chat roles grouped by purpose, so the owner can
// tell subscription sources from club resources at a glance instead of reading
// a flat list of numeric ids.
//
//nolint:wsl_v5 // String-builder formatting is clearer without extra gaps.
func OpsChats(roles []ChatRoleData) string {
	groups := []struct {
		title string
		match func(string) bool
	}{
		{"Источники подписки", func(r string) bool {
			return strings.Contains(r, "Boosty") || strings.Contains(r, "Tribute")
		}},
		{"Клубные ресурсы", func(r string) bool {
			return strings.Contains(r, "club")
		}},
	}

	var b strings.Builder
	b.WriteString("<b>Настроенные чаты</b>\n")

	shown := make(map[int]struct{}, len(roles))
	for _, group := range groups {
		lines := make([]string, 0, len(roles))
		for i, role := range roles {
			if !group.match(role.Role) {
				continue
			}
			shown[i] = struct{}{}
			lines = append(lines, chatRoleLine(role))
		}

		if len(lines) == 0 {
			continue
		}

		fmt.Fprintf(&b, "\n<b>%s</b>\n%s\n", group.title, strings.Join(lines, "\n"))
	}

	var rest []string
	for i, role := range roles {
		if _, ok := shown[i]; ok {
			continue
		}
		rest = append(rest, chatRoleLine(role))
	}

	if len(rest) > 0 {
		fmt.Fprintf(&b, "\n<b>Служебные</b>\n%s\n", strings.Join(rest, "\n"))
	}

	return strings.TrimSpace(b.String())
}

// chatRoleLine renders one /chats row: role, the live chat title when known,
// and the chat id as a copy-friendly code span.
func chatRoleLine(role ChatRoleData) string {
	id := Code(strconv.FormatInt(role.ChatID, 10))
	if role.Title == "" {
		return fmt.Sprintf("• %s: %s", Escape(role.Role), id)
	}

	return fmt.Sprintf("• %s — «%s» %s",
		Escape(role.Role), Escape(role.Title), id)
}

// SyncSummary renders owner-facing reconciliation result counters.
func SyncSummary(processed, failed int) string {
	marker := statusDotGreen()
	suffix := ""

	if failed > 0 {
		marker = "⚠️ "
		suffix = " с ошибками"
	}

	return fmt.Sprintf(
		"%s<b>Сверка завершена%s</b>\n\n• обработано: %s\n• ошибок: %s",
		marker,
		suffix,
		Code(strconv.Itoa(processed)),
		Code(strconv.Itoa(failed)))
}

// OperatorAlert renders a durable operator alert.
func OperatorAlert(severity, kind, title, detail string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s <b>%s</b>\n\n", severityGlyph(severity), Escape(title))
	b.WriteString("Влияние: требуется внимание владельца.\n")
	b.WriteString("Действие: проверьте диагностику ниже и состояние /alerts.\n\n")
	fmt.Fprintf(&b, "<b>Диагностика</b>\n• Уровень: %s\n• Тип: %s",
		Code(severity), Code(kind))

	if detail != "" {
		fmt.Fprintf(&b, "\n• Детали: %s", Code(detail))
	}

	return b.String()
}

// WhoisUsage returns the /whois usage hint.
func WhoisUsage() string {
	return "Используйте: " + Code("/whois <tg_id|@username>")
}

// WhoisNotFound returns a local lookup miss message.
func WhoisNotFound(query string) string {
	return fmt.Sprintf("❔ <b>Пользователь %s не найден.</b>", Escape(query))
}

// Whois returns the owner-facing user card.
//
//nolint:wsl_v5 // String-builder formatting is clearer without extra gaps.
func Whois(data WhoisData) string {
	var b strings.Builder

	user := data.User

	name := strings.TrimSpace(user.FirstName + " " + user.LastName)
	if name == "" {
		name = "(без имени)"
	}

	fmt.Fprintf(&b, "<b>Пользователь %s</b>\n", Code(strconv.FormatInt(user.TGID, 10)))

	if user.Username != "" {
		fmt.Fprintf(&b, "Username: %s\n", Code("@"+user.Username))
	}

	fmt.Fprintf(&b, "Имя: %s\n", Escape(name))
	fmt.Fprintf(&b, "Статус доступа: <b>%s</b>\n",
		Escape(effectiveStatusText(data.Decision.Status)))

	b.WriteString("\n<b>Основание</b>\n")
	if len(data.Subscriptions) == 0 && !data.Whitelisted {
		b.WriteString("• активных подписок или ручного основания нет\n")
	} else {
		for _, sub := range data.Subscriptions {
			fmt.Fprintf(&b, "• %s — %s",
				Escape(platformText(sub.Platform)),
				Escape(subscriptionStatusText(sub.Status)))

			if sub.ExpiresAt != nil {
				fmt.Fprintf(&b, " до %s", Escape(dateText(*sub.ExpiresAt)))
			}

			if sub.EndedAt != nil {
				fmt.Fprintf(&b, ", завершена %s", Escape(dateText(*sub.EndedAt)))
			}

			b.WriteByte('\n')
		}

		if data.Whitelisted {
			b.WriteString("• ручное основание: активно\n")
		}
	}

	b.WriteString("\n<b>Гранты</b>\n")
	if len(data.Grants) == 0 {
		b.WriteString("• нет записей\n")
	} else {
		for _, grant := range data.Grants {
			fmt.Fprintf(&b, "• %s — %s\n",
				Escape(resourceText(grant.Resource)),
				Escape(grantStateText(grant.State)))
		}
	}

	b.WriteString("\n<b>Профиль</b>\n")
	fmt.Fprintf(&b, "• Личка: %s\n", Escape(dmStateText(user.DMState)))
	fmt.Fprintf(&b, "• Белый список: %s\n", Escape(yesNo(data.Whitelisted)))
	fmt.Fprintf(&b, "• Бан: %s", Escape(yesNo(user.Banned)))

	if user.BannedReason != "" {
		fmt.Fprintf(&b, " (%s)", Code(user.BannedReason))
	}

	b.WriteByte('\n')

	b.WriteString("\n<b>Последние события</b>\n")

	if len(data.Audit) == 0 {
		b.WriteString("• нет записей\n")
	} else {
		for _, audit := range data.Audit {
			fmt.Fprintf(&b, "• %s %s",
				Escape(dateTimeText(audit.CreatedAt)),
				Code(audit.Kind))

			if audit.Source != "" {
				fmt.Fprintf(&b, " [%s]", Code(audit.Source))
			}

			if audit.Detail != "" {
				fmt.Fprintf(&b, ": %s", Code(audit.Detail))
			}

			b.WriteByte('\n')
		}
	}

	if len(data.Decision.Reasons) > 0 {
		b.WriteString("\n<b>Диагностика</b>\n")
		appendReasons(&b, data.Decision.Reasons)
	}

	return strings.TrimSpace(b.String())
}

// UnknownChat returns an owner DM for a chat unknown to config.
func UnknownChat(chatID int64, chatType, title string) string {
	if title == "" {
		title = "(без названия)"
	}

	return fmt.Sprintf(
		"⚠️ <b>Бот добавлен в новый чат</b>\n\n"+
			"• Название: %s\n• ID: %s\n• Тип: %s\n\n%s",
		Escape(title),
		Code(strconv.FormatInt(chatID, 10)),
		Code(chatType),
		Italic("Проверьте, должен ли этот чат быть в конфигурации."))
}

// HealthFailure returns an owner DM for a degraded configured chat.
func HealthFailure(chatName string, chatID int64, reason string) string {
	return fmt.Sprintf(
		"⚠️ <b>Проблема с правами бота</b>\n\n"+
			"• Чат: %s\n• ID: %s\n• Причина: %s\n\n"+
			"Действие: проверьте права бота в этом чате.",
		Escape(chatName),
		Code(strconv.FormatInt(chatID, 10)),
		Code(reason))
}

// HealthRestored returns an owner DM for restored bot rights.
func HealthRestored(chatName string, chatID int64) string {
	return fmt.Sprintf(
		statusDotGreen()+"<b>Права бота восстановлены</b>\n\n"+
			"• Чат: %s\n• ID: %s\n\nДействие не требуется.",
		Escape(chatName),
		Code(strconv.FormatInt(chatID, 10)))
}

// WhoisUnavailable returns an owner-facing message when /whois was
// created without the repositories needed to build a user card.
func WhoisUnavailable() string {
	return "❔ <b>Команда /whois сейчас недоступна.</b>\nНе хватает runtime-зависимостей для карточки."
}

// ReasonHardBan explains a manual hard-ban verdict.
func ReasonHardBan() string {
	return "пользователь заблокирован вручную"
}

// ReasonWhitelist explains a whitelist verdict.
func ReasonWhitelist() string {
	return "пользователь в белом списке"
}

// ReasonLocalActiveSubscription explains a persisted active subscription.
func ReasonLocalActiveSubscription() string {
	return "есть активная подписка в локальной базе"
}

// ReasonLocalExpiredSubscription explains a persisted row whose expiry passed.
func ReasonLocalExpiredSubscription() string {
	return "активная строка в локальной базе уже истекла"
}

// ReasonLocalNoBasis explains absence of persisted access reasons.
func ReasonLocalNoBasis() string {
	return "активных оснований в локальной базе нет"
}

// ReasonSourceError explains an unexpected source error.
func ReasonSourceError() string {
	return "источник временно недоступен"
}

// ReasonMembershipCheckFailed explains a failed Telegram membership check.
func ReasonMembershipCheckFailed() string {
	return "не удалось проверить членство в Telegram"
}

// ReasonMembershipInChat explains a positive Telegram membership check.
func ReasonMembershipInChat(chatID int64) string {
	return fmt.Sprintf("пользователь состоит в чате %d", chatID)
}

// ReasonMembershipNotInChat explains a negative Telegram membership check.
func ReasonMembershipNotInChat(chatID int64) string {
	return fmt.Sprintf("пользователь не состоит в чате %d", chatID)
}

// ReasonLedgerReadFailed explains a failed local ledger read.
func ReasonLedgerReadFailed() string {
	return "не удалось прочитать локальную историю подписок"
}

// ReasonLedgerNoActive explains absence of an active ledger row.
func ReasonLedgerNoActive() string {
	return "локальная история не содержит активной записи"
}

// ReasonLedgerExpired explains an expired ledger row.
func ReasonLedgerExpired() string {
	return "локальная подписка истекла"
}

// ReasonLedgerActive explains a positive ledger row.
func ReasonLedgerActive() string {
	return "локальная история подтверждает подписку"
}

// ReasonWhitelistReadFailed explains a failed whitelist read.
func ReasonWhitelistReadFailed() string {
	return "не удалось прочитать whitelist"
}

// ReasonManualSubscriptionReadFailed explains a failed manual subscription read.
func ReasonManualSubscriptionReadFailed() string {
	return "не удалось прочитать ручную подписку"
}

// ReasonManualSubscriptionActive explains an active manual subscription.
func ReasonManualSubscriptionActive() string {
	return "активная ручная подписка"
}

// ReasonManualNoBasis explains absence of manual access.
func ReasonManualNoBasis() string {
	return "ручного основания нет"
}

// ReasonStatusNotComputed explains missing status-engine data.
func ReasonStatusNotComputed() string {
	return "статус не вычислен"
}

func appendReasons(b *strings.Builder, reasons []domain.AccessReason) {
	for _, reason := range reasons {
		fmt.Fprintf(b, "• %s: %s",
			Code(platformText(reason.Source)), Code(verdictText(reason.Verdict)))

		if reason.Detail != "" {
			fmt.Fprintf(b, " — %s", Code(reason.Detail))
		}

		if reason.Until != nil {
			fmt.Fprintf(b, " до %s", Code(dateText(*reason.Until)))
		}

		b.WriteByte('\n')
	}
}

func severityGlyph(severity string) string {
	switch strings.ToLower(severity) {
	case "critical", "error", "warning":
		return "⚠️"
	default:
		return "ℹ️"
	}
}

func effectiveStatusText(status domain.EffectiveStatus) string {
	switch status {
	case domain.StatusActive:
		return "активен"
	case domain.StatusUnknown:
		return "неизвестен"
	default:
		return "нет доступа"
	}
}

func platformText(platform domain.Platform) string {
	switch platform {
	case domain.PlatformBoosty:
		return "Boosty"
	case domain.PlatformTribute:
		return "Tribute"
	case domain.PlatformManual:
		return "ручной доступ"
	case domain.Platform("whitelist"):
		return "белый список"
	case domain.Platform("ban"):
		return "бан"
	case domain.Platform("system"):
		return "система"
	default:
		return string(platform)
	}
}

func verdictText(verdict domain.Verdict) string {
	switch verdict {
	case domain.VerdictActive:
		return "активно"
	case domain.VerdictInactive:
		return "неактивно"
	case domain.VerdictUnknown:
		return "неизвестно"
	case domain.VerdictNoSignal:
		return "нет сигнала"
	default:
		return string(verdict)
	}
}

func subscriptionStatusText(status domain.SubscriptionStatus) string {
	switch status {
	case domain.SubActive:
		return "активна"
	case domain.SubExpired:
		return "истекла"
	default:
		return string(status)
	}
}

func resourceText(resource domain.Resource) string {
	switch resource {
	case domain.ResourceChat:
		return "чат"
	case domain.ResourceChannel:
		return "канал"
	default:
		return string(resource)
	}
}

func grantStateText(state domain.GrantState) string {
	switch state {
	case domain.GrantPending:
		return "ожидает вступления"
	case domain.GrantJoined:
		return "внутри"
	case domain.GrantLeft:
		return "вышел"
	case domain.GrantRevoked:
		return "отозван"
	default:
		return string(state)
	}
}

// resourceMembershipText phrases a user's membership with friendly,
// second-person wording tailored to the resource type: a channel is something
// you subscribe to, a chat is something you belong to. Only the joined state
// counts as present; pending (link issued, no join yet) and left both read as
// "not in".
func resourceMembershipText(resource domain.Resource, state domain.GrantState) string {
	joined := state == domain.GrantJoined

	switch resource {
	case domain.ResourceChannel:
		if joined {
			return "подписан"
		}

		return "не подписан"
	case domain.ResourceChat:
		if joined {
			return "состоишь"
		}

		return "не состоишь"
	default:
		if joined {
			return "доступ есть"
		}

		return "доступа нет"
	}
}

// statusDotGreen and statusDotOrange render the minimalist status signals.
func statusDotGreen() string  { return CustomEmoji(emojiDotGreen, "🟢") }
func statusDotOrange() string { return CustomEmoji(emojiDotOrange, "🟠") }

// statusGopherHeart renders the brand "welcome / thank you" accent, used only
// in genuine join-approved and access-kept moments.
func statusGopherHeart() string { return CustomEmoji(emojiGopherHeart, "❤️") }

// capitalizeFirst upper-cases the first rune, polishing list items that start
// a line (resource names are stored lower-case for inline owner views).
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}

	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])

	return string(r)
}

func dmStateText(state domain.DMState) string {
	switch state {
	case domain.DMOpen:
		return "открыта"
	case domain.DMBlocked:
		return "заблокирована"
	default:
		return "неизвестно"
	}
}

func yesNo(v bool) string {
	if v {
		return "да"
	}

	return "нет"
}

func dateText(t time.Time) string {
	return t.Local().Format("2006-01-02")
}

func dateTimeText(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04")
}
