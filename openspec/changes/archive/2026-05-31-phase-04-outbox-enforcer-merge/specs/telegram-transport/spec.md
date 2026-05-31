## MODIFIED Requirements

### Requirement: The telegram package exposes one concrete Client over the Bot API

Пакет `telegram` MUST экспортировать **один конкретный** тип `*Client` —
обёртку над `github.com/go-telegram/bot` — и не объявляет собственных
интерфейсов (§20.2). `Client` предоставляет методы `getMe`, `getChat`,
`getChatMember`, `sendMessage`, `setMyCommands`, а также методы Bot API,
нужные Enforcer'у: `createChatInviteLink`, `revokeChatInviteLink`,
`approveChatJoinRequest`, `declineChatJoinRequest`, `banChatMember` и
`unbanChatMember`. Узкие интерфейсы объявляют пакеты-потребители у себя.

#### Scenario: Поверхность конкретного клиента
- **WHEN** потребитель использует пакет `telegram`
- **THEN** ему доступен конкретный `*Client` с методами `getMe`,
  `getChat`, `getChatMember`, `sendMessage`, `setMyCommands`,
  `createChatInviteLink`, `revokeChatInviteLink`,
  `approveChatJoinRequest`, `declineChatJoinRequest`, `banChatMember`
  и `unbanChatMember`
- **AND** пакет `telegram` не экспортирует интерфейсов для этих методов

### Requirement: Update handlers commit atomically and keep Telegram calls out of the transaction

Обработчики обновлений MUST соблюдать два инварианта. **I1**: доменные
изменения, `audit_log`-записи, `access_actions`-INSERT'ы и
терминальный `UPDATE telegram_updates.status` коммитятся **в одной
транзакции** (`tx2`). Side-effect'ов вне `tx2`, влияющих на
durable-состояние, нет — иначе крэш между ними дал бы двойную обработку
на старте или потерянное исходящее действие.

Инвариант **I2**: вызовы Telegram внутри `tx2` запрещены — транзакция
не должна зависеть от сетевых таймаутов. Если обработчику нужно
отправить сообщение, выдать invite, approve/decline join request или
выполнить другое доменное Telegram-действие, обработчик MUST поставить
соответствующий `access_actions` row в `tx2`; Enforcer выполнит
Telegram-вызов после коммита.

#### Scenario: Доменное изменение, outbox action и terminal status делят транзакцию
- **WHEN** обработчик применяет доменные изменения и должен выполнить
  Telegram side effect
- **THEN** доменные записи, `access_actions` и терминальный
  `UPDATE telegram_updates.status` коммитятся одной транзакцией
- **AND** при крэше до коммита строка update остаётся `pending` и будет
  переобработана

#### Scenario: В handler-транзакции нет Telegram-вызова
- **WHEN** обработчику нужно отправить сообщение или изменить состояние
  пользователя в Telegram
- **THEN** внутри `tx2` создаётся outbox action
- **AND** прямой вызов Telegram выполняется только Enforcer'ом после
  коммита
