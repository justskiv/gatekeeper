## MODIFIED Requirements

### Requirement: The router dispatches updates by type and chat id

Роутер MUST маршрутизировать каждое обновление по типу и `chat.id`.
В этой фазе **реально обрабатываются**: `message` в личке → хендлеры
команд бота; `/here` в группе/супергруппе от владельца → ответ с
`chat.id` (в каналах недоступна: нет `channel_post` в
`allowed_updates`); `my_chat_member` → chat-health; `chat_member` в
источнике-чате (`BOOSTY_GROUP_ID`, либо `TRIBUTE_CHANNEL_ID` в режиме A)
→ нормализация в `SubscriptionEvent` и `engine.handleEvent` внутри
`tx2`. Обновления `chat_member` в прочих чатах и `chat_join_request`
маршрутов ещё не имеют и завершаются как `ignored` (наполнят Фаза 05).
Прочее — `ignored`.

#### Scenario: Private message маршрутизируется в bot command handlers
- **WHEN** приходит `message` из приватного чата
- **THEN** оно направляется в хендлеры команд бота

#### Scenario: my_chat_member маршрутизируется в chat-health
- **WHEN** приходит `my_chat_member`
- **THEN** оно направляется в обработку chat-health

#### Scenario: chat_member источника-чата маршрутизируется в движок
- **WHEN** приходит `chat_member` с `chat.id`, равным `BOOSTY_GROUP_ID`
  или (в режиме A) `TRIBUTE_CHANNEL_ID`
- **THEN** оно нормализуется в `SubscriptionEvent` и применяется через
  `engine.handleEvent`

#### Scenario: Прочие membership-updates игнорируются в этой фазе
- **WHEN** приходит `chat_member` в чате, не являющемся настроенным
  источником, или `chat_join_request`
- **THEN** в этой фазе строка завершается статусом `ignored`

## ADDED Requirements

### Requirement: Membership-события источников нормализуются в SubscriptionEvent

Роутер MUST приводить `chat_member` из источника-чата к нормализованному
`SubscriptionEvent` до передачи в движок. Платформа MUST определяться по
`chat.id` (`boosty` или `tribute`). Обработчик MUST вычислить членство
до и после по `old_chat_member`/`new_chat_member`: переход
«не-участник → участник» MUST давать `Activated`, «участник →
не-участник» — `Deactivated`. Признак участия MUST включать статусы
`creator`/`administrator`/`member` и `restricted` с `is_member=true`.
Изменения, затрагивающие ботов (включая самого бота), MUST
игнорироваться; смена прав без смены членства (членство до == после)
MUST быть no-op. Применение события MUST идти внутри `tx2`, сохраняя
инварианты I1 (атомарность с терминальным статусом) и I2 (без вызовов
Telegram в транзакции — членство берётся из payload, не из сети).

#### Scenario: Вступление даёт Activated
- **WHEN** в источнике-чате пользователь перешёл из не-участника в
  участники
- **THEN** формируется `SubscriptionEvent{Kind: Activated}` для платформы
  этого чата

#### Scenario: Выход даёт Deactivated
- **WHEN** в источнике-чате пользователь перестал быть участником
- **THEN** формируется `SubscriptionEvent{Kind: Deactivated}`

#### Scenario: Смена прав без смены членства игнорируется
- **WHEN** членство до и после совпадает (изменились только права)
- **THEN** событие не формируется, обновление завершается без доменных
  изменений

#### Scenario: События ботов игнорируются
- **WHEN** затронутый пользователь — бот (в том числе сам бот)
- **THEN** событие не формируется
