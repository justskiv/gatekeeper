# Invite-ссылки

Lifecycle строк `invite_links`, создание Telegram invite links в трёх режимах и безопасное разрешение join-request ссылок для admission. Читаемое зеркало спеки `invite-links`.

## Три режима ссылок

Пакет `invite` управляет lifecycle `invite_links` и созданием Telegram-ссылок. Свои зависимости он объявляет узкими интерфейсами: `linkManager` для `createChatInviteLink`/`revokeChatInviteLink` и узкий Store для `invite_links`. Поддерживаются три режима с активной уникальностью per-mode («активная» = `status IN ('created','sent')`):

- **`shared_join_request`** — максимум одна активная ссылка на `(resource, mode)`, `tg_id IS NULL`, `creates_join_request=true`. Повторный запрос переиспользует существующую активную ссылку, второй активной не создаётся.
- **`personal_join_request`** — персональная join-request ссылка с TTL и `nonce`, максимум одна активная на `(tg_id, resource, mode)`, `tg_id NOT NULL`. До истечения TTL возвращается та же ссылка, без повторного `createChatInviteLink`.
- **`direct`** — аварийная персональная ссылка без join request: `creates_join_request=false`, `member_limit=1`, TTL не больше часа, `tg_id NOT NULL`.

Истёкшая (`expired`) personal-ссылка освобождает слот: для той же тройки `(user, resource, mode)` можно создать новую active.

## Shared-ссылки готовятся при старте

В режиме `shared_join_request` старт ставит `ensure_invite` для клубного чата и канала и дожидается по одной активной ссылке (`creates_join_request=true`) на каждый resource, прежде чем считать Telegram runtime готовым к poller loop (см. [runtime](runtime.md), шаг 14).

## Direct-режим деградирован и огорожен

`direct` работает только при `INVITE_MODE=direct` и `ALLOW_DIRECT_INVITES=true`; иначе конфигурация падает до открытия БД. `INVITE_TTL` для direct не больше часа (лимит Telegram). При успешном старте в direct создаётся `admin_alert(kind='invite_mode_degraded')`, а ссылки создаются с `member_limit=1` и без join request.

## Полные URL не попадают в логи

Полные invite URL не пишутся в технические логи. Для корреляции используется `invite_link_hash`; полный `invite_link` хранится только в БД и отправляется пользователю через outbox.

## Разрешение join-request ссылок

Для admission-хендлеров (см. [grant-access](grant-access.md)) сервис предоставляет операцию разрешения ссылки. Она принимает managed resource, опциональный Telegram `invite_link` и requesting tg_id, а возвращает один из исходов: shared-ссылка принята, personal-ссылка принята, personal-ссылка использована другим, информации нет но fallback безопасен, или ссылка не разрешена.

- При наличии `invite_link` сопоставление идёт по сохранённому `invite_link_hash`, без логирования и раскрытия полного URL.
- В `shared_join_request` принимается любая активная shared-ссылка resource — реальный шлагбаум всё равно `approveChatJoinRequest` с live status.
- В `personal_join_request` ссылка должна принадлежать requesting tg_id; иначе она помечается `used_by_other` с `attempted_by=<requesting tg_id>`.
- Если Telegram не прислал `invite_link`, active-пользователь не отклоняется только из-за пустого поля: shared-режим разрешает заявку по resource, personal-режим может использовать последнюю active personal-ссылку пользователя для этого resource.

## Приёмка

- Второй active shared на `(resource, mode)` не создаётся; personal до TTL переиспользуется без вызова Telegram; `expired` personal освобождает слот.
- `INVITE_MODE=direct` без `ALLOW_DIRECT_INVITES=true` или с `INVITE_TTL > 1h` не стартует; валидный direct поднимает `invite_mode_degraded` и режет `member_limit=1`.
- Технический лог содержит максимум `invite_link_hash`, не полный URL — в том числе при resolution.
- Shared join request разрешается по resource (доступ всё равно зависит от live-проверки); personal — по принадлежности requester'у; чужая personal помечается `used_by_other` с `attempted_by`; пустое поле `invite_link` использует безопасный fallback.
