// Package messages keeps user-visible Telegram texts in one place.
package messages

import (
	"fmt"
	"strings"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

const (
	CommandStartDescription  = "начать работу"
	CommandHelpDescription   = "справка"
	CommandHereDescription   = "показать ID чата"
	CommandStatusDescription = "показать статус подписки"
	CommandWhoisDescription  = "показать карточку пользователя"

	MsgNoSub = "Подписка пока не найдена. Проверьте оформление подписки и напишите боту с того же аккаунта Telegram."
)

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

// Welcome returns the /start greeting.
func Welcome() string {
	return "Привет! Я помогу получить доступ в закрытое сообщество. Оформите подписку и пишите боту с того же аккаунта Telegram."
}

// Help returns the /help text.
func Help() string {
	return "Бот проверяет подписку Boosty или Tribute и выдаёт доступ в закрытые чат и канал. Важно: пишите с того же аккаунта Telegram, которым оформляли подписку."
}

// Here returns a chat discovery response for owners.
func Here(chatID int64, chatType string) string {
	return fmt.Sprintf("chat.id: %d\nchat.type: %s", chatID, chatType)
}

// Status returns the /status response.
func Status(
	decision domain.AccessDecision,
	subscriptions []domain.Subscription,
	grants []domain.AccessGrant,
) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Статус доступа: %s\n", effectiveStatusText(decision.Status))
	if len(subscriptions) == 0 {
		b.WriteString(MsgNoSub)
		b.WriteByte('\n')
	} else {
		b.WriteString("Активные подписки:\n")
		for _, sub := range subscriptions {
			fmt.Fprintf(&b, "- %s", platformText(sub.Platform))
			if sub.ExpiresAt != nil {
				fmt.Fprintf(&b, " до %s", dateText(*sub.ExpiresAt))
			}
			if sub.Tier != "" {
				fmt.Fprintf(&b, " (%s)", sub.Tier)
			}
			b.WriteByte('\n')
		}
	}
	// Access grants are populated by the later admit flow; live source
	// membership is already visible through subscriptions and reasons.
	b.WriteString("Клубные ресурсы:\n")
	if len(grants) == 0 {
		b.WriteString("- доступы в чат и канал ещё не выдавались\n")
	} else {
		for _, grant := range grants {
			fmt.Fprintf(&b, "- %s: %s\n",
				resourceText(grant.Resource), grantStateText(grant.State))
		}
	}
	if len(decision.Reasons) > 0 {
		b.WriteString("Проверка источников:\n")
		appendReasons(&b, decision.Reasons)
	}
	return strings.TrimSpace(b.String())
}

// AccessKept returns the grace-period cancellation notification.
func AccessKept() string {
	return "Подписка снова активна. Запланированный отзыв доступа отменён."
}

// WhoisUsage returns the /whois usage hint.
func WhoisUsage() string {
	return "Используйте: /whois <tg_id|@username>"
}

// WhoisNotFound returns a local lookup miss message.
func WhoisNotFound(query string) string {
	return fmt.Sprintf("Пользователь %s не найден в локальной базе.", query)
}

// Whois returns the owner-facing user card.
func Whois(data WhoisData) string {
	var b strings.Builder
	user := data.User
	name := strings.TrimSpace(user.FirstName + " " + user.LastName)
	if name == "" {
		name = "(без имени)"
	}
	fmt.Fprintf(&b, "Пользователь: %d", user.TGID)
	if user.Username != "" {
		fmt.Fprintf(&b, " (@%s)", user.Username)
	}
	fmt.Fprintf(&b, "\nИмя: %s\n", name)
	fmt.Fprintf(&b, "Личка: %s\n", dmStateText(user.DMState))
	fmt.Fprintf(&b, "Белый список: %s\n", yesNo(data.Whitelisted))
	fmt.Fprintf(&b, "Бан: %s", yesNo(user.Banned))
	if user.BannedReason != "" {
		fmt.Fprintf(&b, " (%s)", user.BannedReason)
	}
	b.WriteByte('\n')
	fmt.Fprintf(&b, "Статус доступа: %s\n",
		effectiveStatusText(data.Decision.Status))

	b.WriteString("Подписки:\n")
	if len(data.Subscriptions) == 0 {
		b.WriteString("- нет записей\n")
	} else {
		for _, sub := range data.Subscriptions {
			fmt.Fprintf(&b, "- %s: %s",
				platformText(sub.Platform), subscriptionStatusText(sub.Status))
			if sub.ExpiresAt != nil {
				fmt.Fprintf(&b, " до %s", dateText(*sub.ExpiresAt))
			}
			if sub.EndedAt != nil {
				fmt.Fprintf(&b, ", завершена %s", dateText(*sub.EndedAt))
			}
			b.WriteByte('\n')
		}
	}

	b.WriteString("Доступы:\n")
	if len(data.Grants) == 0 {
		b.WriteString("- нет записей\n")
	} else {
		for _, grant := range data.Grants {
			fmt.Fprintf(&b, "- %s: %s\n",
				resourceText(grant.Resource), grantStateText(grant.State))
		}
	}

	if len(data.Decision.Reasons) > 0 {
		b.WriteString("Причины:\n")
		appendReasons(&b, data.Decision.Reasons)
	}

	b.WriteString("Последний аудит:\n")
	if len(data.Audit) == 0 {
		b.WriteString("- нет записей\n")
	} else {
		for _, audit := range data.Audit {
			fmt.Fprintf(&b, "- %s %s", dateTimeText(audit.CreatedAt), audit.Kind)
			if audit.Source != "" {
				fmt.Fprintf(&b, " [%s]", audit.Source)
			}
			if audit.Detail != "" {
				fmt.Fprintf(&b, ": %s", audit.Detail)
			}
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}

// UnknownChat returns an owner DM for a chat unknown to config.
func UnknownChat(chatID int64, chatType, title string) string {
	if title == "" {
		title = "(без названия)"
	}
	return fmt.Sprintf(
		"Бот добавлен в новый чат.\nchat.id: %d\nchat.type: %s\nНазвание: %s",
		chatID, chatType, title)
}

// HealthFailure returns an owner DM for a degraded configured chat.
func HealthFailure(chatName string, chatID int64, reason string) string {
	return fmt.Sprintf(
		"Проблема с правами бота в %s.\nchat.id: %d\nПричина: %s",
		chatName, chatID, reason)
}

// HealthRestored returns an owner DM for restored bot rights.
func HealthRestored(chatName string, chatID int64) string {
	return fmt.Sprintf(
		"Права бота восстановлены в %s.\nchat.id: %d",
		chatName, chatID)
}

// WhoisUnavailable returns an owner-facing message when /whois was
// created without the repositories needed to build a user card.
func WhoisUnavailable() string {
	return "Команда /whois сейчас недоступна: не хватает внутренних зависимостей."
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
		fmt.Fprintf(b, "- %s: %s",
			platformText(reason.Source), verdictText(reason.Verdict))
		if reason.Detail != "" {
			fmt.Fprintf(b, " — %s", reason.Detail)
		}
		if reason.Until != nil {
			fmt.Fprintf(b, " до %s", dateText(*reason.Until))
		}
		b.WriteByte('\n')
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
