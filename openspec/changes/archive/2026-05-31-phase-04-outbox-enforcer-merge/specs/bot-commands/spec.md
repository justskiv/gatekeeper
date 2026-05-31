## MODIFIED Requirements

### Requirement: DM delivery respects dm_state and handles blocking

Пакет `notify` MUST формировать личные сообщения через узкий
интерфейс-потребитель и для Telegram update handler'ов MUST ставить
durable `send_dm` action вместо прямого Telegram-вызова. Перед
постановкой, если строка пользователя существует, notify MUST сверяться
с `users.dm_state` и **пропускать** заведомо `blocked` пользователя
(не создаёт лишний `send_dm`). Ответ Telegram `403` при фактическом
исполнении Enforcer'ом означает закрытую личку: пользователь
помечается `dm_state='blocked'`, action завершается без retry.

Текущие личные ответы bot-command handler'ов (`/start`, `/help`,
`/status`, `/whois` и некомандный DM-текст, который обрабатывается как
`/start`) MUST enqueue `send_dm` в той же handler-транзакции, где
фиксируются durable изменения и terminal status входящего update.
Admission-specific сообщения Фазы 05 (`send_invite`, invite-soon и
join-request replies) в этой фазе не вводятся, но MUST использовать тот
же outbox/Enforcer канал, когда появятся.

#### Scenario: Command DM ставится в durable outbox
- **WHEN** bot-command handler должен отправить личное сообщение
  пользователю
- **THEN** в той же handler-транзакции создаётся `send_dm` action
- **AND** прямой `sendMessage` из handler'а не вызывается

#### Scenario: Блокировка выставляет dm_state и не ретраится
- **WHEN** отправка DM через Enforcer возвращает `403`
- **THEN** у пользователя выставляется `users.dm_state='blocked'`
- **AND** action завершается без повторов

#### Scenario: Известный blocked-пользователь пропускается до enqueue
- **WHEN** notify просят отправить DM пользователю с
  `dm_state='blocked'`
- **THEN** `send_dm` action не создаётся
- **AND** вызов `sendMessage` не делается
