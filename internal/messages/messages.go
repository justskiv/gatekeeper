// Package messages keeps user-visible Telegram texts in one place.
package messages

import "fmt"

const (
	CommandStartDescription = "начать работу"
	CommandHelpDescription  = "справка"
	CommandHereDescription  = "показать ID чата"

	MsgNoSub = "Подписка пока не найдена. Проверьте оформление подписки и напишите боту с того же аккаунта Telegram."
)

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
