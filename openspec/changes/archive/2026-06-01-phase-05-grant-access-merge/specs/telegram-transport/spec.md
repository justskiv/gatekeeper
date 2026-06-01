## MODIFIED Requirements

### Requirement: The router dispatches updates by type and chat id

Роутер MUST маршрутизировать каждое обновление по типу и `chat.id`.
Реально обрабатываются: `message` в личке -> хендлеры команд бота
(`/start` и некомандный DM запускают grant-access flow); `/here` в
группе или супергруппе от владельца -> ответ с `chat.id` (в каналах
недоступна: нет `channel_post` в `allowed_updates`);
`my_chat_member` -> chat-health; `chat_member` в источнике-чате
(`BOOSTY_GROUP_ID`, либо `TRIBUTE_CHANNEL_ID` в режиме A) ->
нормализация в `SubscriptionEvent` и `engine.handleEvent` внутри
`tx2`; `chat_join_request` в club chat или club channel -> admission
join-request handler; `chat_member` в club chat или club channel ->
club membership handler. Прочее завершается как `ignored`.

Неоднозначное пересечение source chat id и club resource id отклоняется
на уровне runtime/config до запуска poller, поэтому роутер получает уже
однозначную конфигурацию и не выбирает между двумя доменными
обработчиками для одного update.

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

#### Scenario: chat_join_request клубного ресурса маршрутизируется в admission
- **WHEN** приходит `chat_join_request` с `chat.id`, равным club chat
  или club channel
- **THEN** оно направляется в grant-access join-request handler

#### Scenario: chat_member клубного ресурса обновляет grants
- **WHEN** приходит `chat_member` с `chat.id`, равным club chat или
  club channel
- **THEN** оно направляется в club membership handler

#### Scenario: Прочие membership-updates игнорируются
- **WHEN** приходит `chat_member` или `chat_join_request` в чате, не
  являющемся настроенным источником или club resource
- **THEN** строка завершается статусом `ignored`
