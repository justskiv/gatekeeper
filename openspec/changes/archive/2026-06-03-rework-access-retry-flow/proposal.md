## Why

В продакшене вскрылись три связанных дефекта access-флоу, которые
сходились в один и тот же тихий отказ — eligible-пользователя
отклоняли без обратной связи:

- Поллер сохранял в `telegram_updates` уже *отредактированный* payload
  и затем обрабатывал эту же строку. В join-request `invite_link`
  приходил литералом `[redacted_invite_link]`, его hash не совпадал ни
  с одной managed-ссылкой, и каждый запрос отклонялся как «проверка
  подписки не удалась». Redaction должен жить на границе логирования, а
  не на данных, по которым бот принимает решения.
- 30-секундный per-user throttle и 30-секундный idempotency-bucket
  схлопывали повторные тапы в один ответ: второй `/start` или повторное
  нажатие retry уходили в тишину или возвращали фейковый сбой.
- Retry-кнопка не давала никакой мгновенной реакции (спиннер висел),
  а результат прилетал новым сообщением, оставляя исходное с «мёртвой»
  кнопкой.

Параллельно вход в подписку через `?start=` deep-link оказался
нерабочим: Telegram схлопывает deep-link до голого `/start`, который
перехватывается access-флоу, и пользователь никогда не видел страницу
подписки.

Нужно зафиксировать как контракт: сырое хранение update-payload,
безусловный ответ на каждый запрос доступа, in-place обновление
retry-сообщения, отдельный вход в подписку через команды и алерт на
аномальный decline.

## What Changes

- Telegram update-payload сохраняется и обрабатывается **сырым**;
  redaction (`redact`) применяется только на границе логирования, не к
  persisted/processed payload.
- Per-user 30s access-check throttle и 30s idempotency-bucket удалены:
  каждый `/start` и каждый retry получает ответ. In-memory decision
  cache отложен и не входит в этот change. Idempotency-маркер
  access-ответа становится per-request (UnixNano).
- Retry-callback подтверждается мгновенно: бот отвечает на callback
  query (убирает спиннер), меняет inline-кнопку на лейбл «проверяем» и
  редактирует существующее сообщение на месте (`editMessageText` /
  `editMessageReplyMarkup`) вместо отправки нового. Если сообщение
  слишком старое для редактирования — fallback к свежему DM.
- Ответ об отсутствии подписки несёт кнопку «Проверить ещё раз».
- Join-request, отклонённый из-за неразрешённой invite-ссылки, получает
  отдельное сообщение «ссылка не распознана» вместо вводящего в
  заблуждение «проверка подписки не удалась».
- Decline join-request у пользователя с активной подпиской поднимает
  admin alert `join_declined_active_sub`.
- Вход в подписку реализован через tappable in-bot страницы `/boosty`
  и `/tribute` вместо `?start=` deep-links; команды зарегистрированы в
  меню бота через `setMyCommands`.
- Добавлен новый outbox action type `edit_message`, редактирующий текст
  и inline-клавиатуру существующего сообщения.

## Capabilities

### Modified Capabilities

- `telegram-transport`: update-payload хранится/обрабатывается сырым
  (redaction только в логах); `*Client` получает методы
  `answerCallbackQuery`, `editMessageText`, `editMessageReplyMarkup`;
  поллер мгновенно подтверждает retry-callback.
- `grant-access`: throttle снят, ответ на каждый запрос; per-request
  idempotency; no-sub несёт retry-кнопку; in-place edit результата
  retry; отдельное сообщение об нераспознанной invite-ссылке; alert на
  decline активного подписчика.
- `bot-commands`: rate-limit убран из retry-контракта; user command
  scope включает `/boosty` и `/tribute`.
- `bot-message-ux`: вход в подписку идёт через in-bot команды-страницы,
  а не через `/start` deep-link с скрытым payload.
- `outbox-enforcer`: новый action type `edit_message`.

## Impact

- `internal/telegram/poller.go`: удалён rate limiter, удалена redaction
  persisted payload, добавлено `acknowledgeRetryCallback`.
- `internal/telegram/router.go`: проброс edit-target из callback в
  access request.
- `internal/telegram/client.go`: `AnswerCallbackQuery`,
  `EditMessageText`, `EditMessageReplyMarkup`, `/boosty` и `/tribute` в
  `SetMyCommands`.
- `internal/admission/handler.go`: per-request marker, edit-target,
  `InviteNotRecognized`, alert `join_declined_active_sub`.
- `internal/bot/user.go`: команды `/boosty` и `/tribute`.
- `internal/domain/access.go`, `internal/enforcer`: action type
  `edit_message` и его исполнение.
- `internal/messages`: `Boosty()`, `Tribute()`, `NoSub()` как функция,
  `InviteNotRecognized()`, `CheckingSubscription()`, `CustomEmoji`.
- `migrations/0002_outbox_edit_message.sql`: расширение CHECK
  `action_type` до `edit_message`.
