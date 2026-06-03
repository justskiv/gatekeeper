## Context

Access-флоу принимал `/start`, некомандный DM и retry-callback, прогонял
их через единый admission handler и отвечал через durable `send_dm`.
Поверх этого жили два механизма защиты от нагрузки — 30s per-user
throttle в поллере и 30s idempotency-bucket в access-маркере — и
конвейер хранения update'ов с redaction на входе. На проде эти три
решения вместе превратили нормальный сценарий «пользователь оформил
подписку и нажал проверить» в тихий отказ.

## Decisions

### 1. Redaction — на границе логирования, а не в persisted payload

Поллер сохранял `redact.JSONPayload(payload)` и затем обрабатывал ту же
строку `telegram_updates`. Для join-request это уничтожало `invite_link`
(он становился `[redacted_invite_link]`), и invite-resolution не находил
managed-ссылку — каждый запрос отклонялся.

Решение: `telegram_updates.payload_json` хранит **сырой** update. Это
ровно то, что уже требует спека inbox'а («raw-JSON обновления») —
redaction была регрессией относительно неё. Redaction остаётся, но
применяется только при логировании, где секреты действительно не должны
утекать. Альтернатива «хранить сырое, но обрабатывать redacted» отвергнута:
обработка обязана видеть те же данные, что прислал Telegram, иначе
forensics и runtime расходятся.

### 2. Снять throttle и idempotency-bucket, а не чинить их

30s throttle (`admissionRateLimiter`) и 30s time-bucket в `accessMarker`
(`now/30`) преследовали load protection, но давали ложный молчаливый
отказ: второй тап в окне либо не вызывал проверку, либо делил
idempotency-ключ с первым ответом, и outbox глотал дубликат.

Решение: убрать оба. `accessMarker` становится per-call уникальным
(`UnixNano`), так что каждый запрос доступа получает свой ответ. Это
сознательный временный регресс по нагрузке: правильная замена —
in-memory decision cache, который кеширует *решение*, а не подавляет
*ответ*. Он отложен и не входит в этот change. Action-маркеры
(grant/invite) свою идемпотентность сохраняют — дедуп снят только с
user-facing reply.

### 3. Retry-результат редактирует исходное сообщение

Retry-callback приходит привязанным к сообщению с кнопкой. Раньше
результат уходил новым DM, и старое сообщение оставалось с «живой»
кнопкой и без обратной связи.

Решение двухфазное:

1. До медленного preflight поллер мгновенно отвечает на callback query
   (убирает спиннер) и через `editMessageReplyMarkup` меняет кнопку на
   лейбл «проверяем». Обе операции best-effort: их сбой логируется и не
   прерывает обработку update.
2. Результат доставляется как `edit_message` outbox action, который
   `editMessageText` перезаписывает текст и клавиатуру того же
   сообщения. Не-retry результат (доступ выдан) очищает клавиатуру.

Edit-target пробрасывается из callback (`chat_id` + `message_id`) в
`AccessRequest`. Если координат нет (сообщение недоступно/слишком
старое), reply падает обратно на свежий DM — `editTarget()` возвращает
`ok=false`.

Тонкость Telegram: пустая inline-клавиатура должна быть non-nil
slice — `nil` маршалится в JSON `null`, который Telegram отклоняет
(`field "inline_keyboard" must be of type Array`). Ответ
`message is not modified` трактуется как успех.

### 4. Подписка — через команды-страницы, не deep-link

`?start=boosty`-стиль deep-link Telegram схлопывает до голого `/start`,
который перехватывает access-флоу; payload теряется, и страница подписки
не показывается. Решение: отдельные tappable-команды `/boosty` и
`/tribute`, каждая рендерит свою страницу со ссылками. Это держит
видимую команду честной — в чате видно `/boosty`, а не `/start` со
скрытым payload. `NoSub` становится функцией, потому что теперь несёт
ссылки на эти входы.

### 5. Alert на decline активного подписчика

Decline join-request у пользователя, у которого *есть* свежая активная
подписка, — аномалия: eligible-пользователя развернули. Это ровно тот
silent-failure shape, что породил redaction-баг. Решение: при
`reason=invite_unresolved` и свежей активной подписке поднимать
`admin_alert(kind='join_declined_active_sub', severity='warning')`.
Обычные `inactive`-declines (нет подписки) ожидаемы и не алертят.

## Risks / Trade-offs

- Снятие throttle открывает access-флоу к спаму тапами до появления
  decision cache → принято сознательно; load protection восстановит
  отдельный change, action-идемпотентность сохранена.
- In-place edit может не сработать на старом сообщении → явный fallback
  на свежий DM через `editTarget() ok=false`.
- `edit_message` расширяет CHECK `action_type`; SQLite не умеет ALTER
  CHECK → миграция 0002 пересобирает таблицу (rename/recreate/copy/drop).
