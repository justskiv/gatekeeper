## ADDED Requirements

### Requirement: Runtime отклоняет неоднозначные source и club chat IDs

Startup MUST отклонять конфигурацию, в которой один Telegram `chat.id`
одновременно является source chat и managed club resource. Проверка
MUST выполняться до запуска poller loop, чтобы один Telegram update не
мог быть направлен в два доменных обработчика. Ошибка MUST называть
конфликтующие configuration keys и shared value.

#### Scenario: Source chat id совпадает с club resource id
- **WHEN** один и тот же `chat.id` настроен, например, как
  `BOOSTY_GROUP_ID` и `CLUB_CHAT_ID`
- **THEN** startup завершается ошибкой конфигурации до запуска poller
- **AND** ошибка называет оба конфликтующих key и shared value
