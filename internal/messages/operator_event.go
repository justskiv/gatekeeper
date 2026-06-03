package messages

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

// Operator-feed glyphs. Plain emoji keep the feed readable on any client;
// the feed is owner-only and does not depend on Premium custom emoji.
const (
	opGlyphGranted   = "✅"
	opGlyphScheduled = "⏳"
	opGlyphLost      = "🔻"
	opGlyphKept      = "♻️"
	opGlyphJoined    = "➕"
	opGlyphLeft      = "➖"
	opGlyphSource    = "🟢"
	opGlyphSourceOff = "⚪️"
	opGlyphManual    = "🛠"
	opGlyphBan       = "⛔️"
)

// RenderOperatorEvent renders one operator event-log entry as owner-facing
// Telegram HTML: a summary line first, then safe diagnostics. It renders only
// from the typed safe context — never full invite URLs, raw payloads, secrets
// or internal resource chat IDs. An unknown kind is a feed-build error: the
// caller skips the event without rolling back the domain change.
func RenderOperatorEvent(ev domain.OperatorEvent) (string, error) {
	switch ev.Kind {
	case domain.OpAccessGranted:
		return renderAccessGranted(ev), nil
	case domain.OpAccessLossScheduled:
		return renderAccessLossScheduled(ev), nil
	case domain.OpAccessLost:
		return renderAccessLost(ev), nil
	case domain.OpAccessKept:
		return renderAccessKept(ev), nil
	case domain.OpClubChatJoined, domain.OpClubChannelSubscribed:
		return renderMembershipJoined(ev), nil
	case domain.OpClubChatLeft, domain.OpClubChannelUnsubscribed:
		return renderMembershipLeft(ev), nil
	case domain.OpSourceSubscriptionActivated:
		return renderSourceActivated(ev), nil
	case domain.OpSourceSubscriptionExpired:
		return renderSourceExpired(ev), nil
	case domain.OpManualGrant, domain.OpManualRevoke,
		domain.OpManualBan, domain.OpManualUnban:
		return renderManual(ev), nil
	case domain.OpBannedJoinAttempt:
		return renderBannedJoinAttempt(ev), nil
	default:
		return "", fmt.Errorf("operator event: unknown kind %q", ev.Kind)
	}
}

func renderAccessGranted(ev domain.OperatorEvent) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s <b>Доступ выдан</b>\n\n", opGlyphGranted)
	fmt.Fprintf(&b, "Кто: %s\n", opSubjectLine(ev))
	fmt.Fprintf(&b, "Основание: %s", opSourcesText(ev.ActiveSources))

	if ev.Fallback {
		b.WriteString(" (резервная проверка по сохранённой подписке)")
	}

	if len(ev.Resources) > 0 {
		fmt.Fprintf(&b, "\nРесурсы: %s", opClubResourcesText(ev.Resources))
	}

	if ev.Method != "" {
		fmt.Fprintf(&b, "\nСпособ: %s", opMethodText(ev))
	}

	if ev.ManualReason != "" {
		fmt.Fprintf(&b, "\nПричина: %s", Escape(ev.ManualReason))
	}

	if ev.InviteMode != "" {
		fmt.Fprintf(&b, "\nРежим ссылок: %s", Code(string(ev.InviteMode)))
	}

	return b.String()
}

func renderAccessLossScheduled(ev domain.OperatorEvent) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s <b>Доступ запланирован к отзыву</b>\n\n", opGlyphScheduled)
	fmt.Fprintf(&b, "Кто: %s\n", opSubjectLine(ev))
	b.WriteString("Причина: подписка истекла, идёт грейс-период")

	if ev.ScheduledAt != nil {
		fmt.Fprintf(&b, "\nОтзыв: %s", Code(opTime(*ev.ScheduledAt)))
	}

	return b.String()
}

func renderAccessLost(ev domain.OperatorEvent) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s <b>Доступ отозван</b>\n\n", opGlyphLost)
	fmt.Fprintf(&b, "Кто: %s\n", opSubjectLine(ev))

	switch {
	case ev.HardBan:
		b.WriteString("Причина: блокировка (перекрывает активный источник)")
	case ev.ManualReason != "":
		fmt.Fprintf(&b, "Причина: %s", Escape(ev.ManualReason))
	case opMeaningfulReason(ev.Reason):
		fmt.Fprintf(&b, "Причина: %s", Escape(ev.Reason))
	default:
		b.WriteString("Причина: подписка неактивна")
	}

	if len(ev.Resources) > 0 {
		fmt.Fprintf(&b, "\nРесурсы: %s", opClubResourcesText(ev.Resources))
	}

	if ev.LossMode != "" {
		fmt.Fprintf(&b, "\nРежим: %s", Code(string(ev.LossMode)))
	}

	return b.String()
}

func renderAccessKept(ev domain.OperatorEvent) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s <b>Доступ сохранён</b>\n\n", opGlyphKept)
	fmt.Fprintf(&b, "Кто: %s\n", opSubjectLine(ev))
	b.WriteString("Причина: подписка снова активна, отзыв отменён")
	fmt.Fprintf(&b, "\nОснование: %s", opSourcesText(ev.ActiveSources))

	return b.String()
}

func renderMembershipJoined(ev domain.OperatorEvent) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s <b>%s</b>\n\n", opGlyphJoined, opMembershipSummary(ev))
	fmt.Fprintf(&b, "Кто: %s\n", opSubjectLine(ev))
	fmt.Fprintf(&b, "Способ: %s\n", opMethodText(ev))
	fmt.Fprintf(&b, "Источники доступа: %s", opSourcesText(ev.ActiveSources))

	return b.String()
}

func renderMembershipLeft(ev domain.OperatorEvent) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s <b>%s</b>\n\n", opGlyphLeft, opMembershipSummary(ev))
	fmt.Fprintf(&b, "Кто: %s", opSubjectLine(ev))

	return b.String()
}

func renderSourceActivated(ev domain.OperatorEvent) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s <b>Подписка активирована</b>\n\n", opGlyphSource)
	fmt.Fprintf(&b, "Кто: %s\n", opSubjectLine(ev))
	fmt.Fprintf(&b, "Источник: %s", platformText(ev.Platform))

	if ev.Tier != "" {
		fmt.Fprintf(&b, "\nУровень: %s", Code(ev.Tier))
	}

	if ev.ProviderEvent != "" {
		fmt.Fprintf(&b, "\nСобытие: %s", Code(ev.ProviderEvent))
	}

	if ev.ExpiresAt != nil {
		fmt.Fprintf(&b, "\nДействует до: %s", Code(opTime(*ev.ExpiresAt)))
	}

	return b.String()
}

func renderSourceExpired(ev domain.OperatorEvent) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s <b>Подписка истекла</b>\n\n", opGlyphSourceOff)
	fmt.Fprintf(&b, "Кто: %s\n", opSubjectLine(ev))
	fmt.Fprintf(&b, "Источник: %s", platformText(ev.Platform))

	if ev.ProviderEvent != "" {
		fmt.Fprintf(&b, "\nСобытие: %s", Code(ev.ProviderEvent))
	}

	return b.String()
}

func renderManual(ev domain.OperatorEvent) string {
	var b strings.Builder

	glyph := opGlyphManual
	if ev.Kind == domain.OpManualBan {
		glyph = opGlyphBan
	}

	fmt.Fprintf(&b, "%s <b>%s</b>\n\n", glyph, opManualSummary(ev.Kind))
	fmt.Fprintf(&b, "Кто: %s\n", opSubjectLine(ev))
	b.WriteString("Инициатор: владелец")

	if ev.ExpiresAt != nil && ev.Kind == domain.OpManualGrant {
		fmt.Fprintf(&b, "\nДействует до: %s", Code(opTime(*ev.ExpiresAt)))
	}

	if ev.ManualReason != "" {
		fmt.Fprintf(&b, "\nПричина: %s", Escape(ev.ManualReason))
	}

	return b.String()
}

func renderBannedJoinAttempt(ev domain.OperatorEvent) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s <b>Попытка входа заблокированного</b>\n\n", opGlyphBan)
	fmt.Fprintf(&b, "Кто: %s\n", opSubjectLine(ev))

	if ev.Resource != nil {
		fmt.Fprintf(&b, "Ресурс: %s\n", opClubResourceText(*ev.Resource))
	}

	b.WriteString("Действие: поставлена в очередь блокировка (удаление асинхронно)")

	return b.String()
}

// opSubjectLine renders the subject user safely: display name, @username and
// the user's Telegram id (the same identifier /whois shows). It never renders
// a managed-resource chat id.
func opSubjectLine(ev domain.OperatorEvent) string {
	var b strings.Builder

	if name := strings.TrimSpace(ev.UserLabel); name != "" {
		b.WriteString(Escape(name))
	}

	if ev.Username != "" {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}

		// Plain text (not a code span) so Telegram renders @username as a
		// tappable mention.
		b.WriteString(Escape("@" + ev.Username))
	}

	id := Code(strconv.FormatInt(ev.TGID, 10))
	if b.Len() == 0 {
		return id
	}

	return b.String() + " · id " + id
}

func opMembershipSummary(ev domain.OperatorEvent) string {
	switch ev.Kind {
	case domain.OpClubChatJoined:
		return "Вошёл в клубный чат"
	case domain.OpClubChatLeft:
		return "Вышел из клубного чата"
	case domain.OpClubChannelSubscribed:
		return "Подписался на клубный канал"
	case domain.OpClubChannelUnsubscribed:
		return "Отписался от клубного канала"
	default:
		return "Изменение членства"
	}
}

func opManualSummary(kind domain.OperatorEventKind) string {
	switch kind {
	case domain.OpManualGrant:
		return "Ручная выдача доступа"
	case domain.OpManualRevoke:
		return "Ручной отзыв доступа"
	case domain.OpManualBan:
		return "Ручная блокировка"
	case domain.OpManualUnban:
		return "Снятие блокировки"
	default:
		return "Ручная команда"
	}
}

// opMethodText renders the admission method. For an external add it shows a
// safe actor label only when Telegram exposed one; otherwise it stays
// "внешний вход" without guessing who added the user.
func opMethodText(ev domain.OperatorEvent) string {
	switch ev.Method {
	case domain.AdmissionBotLink:
		return "по ссылке бота"
	case domain.AdmissionExternal:
		if label := strings.TrimSpace(ev.ActorLabel); label != "" {
			return "добавлен внешне: " + Escape(label)
		}

		return "внешний вход (без бота)"
	case domain.AdmissionAdmin:
		return "командой владельца"
	case domain.AdmissionProvider:
		return "от источника"
	case domain.AdmissionJob:
		return "фоновой задачей"
	default:
		return "неизвестно"
	}
}

func opSourcesText(sources []domain.Platform) string {
	if len(sources) == 0 {
		return "источник неизвестен"
	}

	labels := make([]string, 0, len(sources))
	for _, source := range sources {
		labels = append(labels, platformText(source))
	}

	return strings.Join(labels, ", ")
}

func opClubResourceText(resource domain.Resource) string {
	switch resource {
	case domain.ResourceChat:
		return "клубный чат"
	case domain.ResourceChannel:
		return "клубный канал"
	default:
		return resourceText(resource)
	}
}

func opClubResourcesText(resources []domain.Resource) string {
	labels := make([]string, 0, len(resources))
	for _, resource := range resources {
		labels = append(labels, opClubResourceText(resource))
	}

	return strings.Join(labels, ", ")
}

// opMeaningfulReason reports whether a revocation reason adds information
// beyond the default "subscription inactive" copy. Generic system tokens are
// suppressed so the feed does not show a bare "inactive".
func opMeaningfulReason(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "", "inactive":
		return false
	default:
		return true
	}
}

func opTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04 MST")
}
