# Durable outbox и Enforcer

Разделение принятия решений и их исполнения через durable-таблицу `access_actions`: домен пишет исходящее Telegram-действие в БД, Enforcer исполняет его асинхронно после коммита. Читаемое зеркало спеки `outbox-enforcer`.

## Решение и исполнение разделены outbox'ом

Доменный код записывает исходящее Telegram-действие в `access_actions` *до* исполнения. Если действие связано с изменением durable-состояния, строка action коммитится в той же handler-транзакции (`tx2`), что и доменное изменение, `audit_log` и терминальный статус входящего update (инвариант I1, см. [telegram-transport](telegram-transport.md)). Сам Telegram-вызов выполняет Enforcer асинхронно после коммита.

Транзакция БД не удерживается открытой во время сетевого вызова: сетевые probe идут до `tx2`, durable action пишется внутри `tx2`, фактический side effect — после коммита. При откате `tx2` не сохраняются ни доменное изменение, ни строка action.

## Идемпотентность и lease воркерами

`access_actions.idempotency_key` обязателен и уникален: повторная постановка того же смыслового действия не создаёт вторую строку, а caller получает существующее действие или явный no-op без ошибки. Готовыми к работе считаются строки `status='queued' AND run_after<=now` и зависшие `status='running' AND locked_until<now`.

Воркер берёт действие короткой lease-транзакцией: атомарно переводит строку в `running`, выставляет новый `locked_until` и коммитит *до* сетевого вызова. Второй воркер то же действие до истечения lease не получает. Зависшее `running` (просроченный `locked_until`) подхватывается следующим циклом, поэтому падение воркера не теряет действие.

## Поддержанные действия

Enforcer — единственная точка доменных исходящих Telegram-вызовов. Он читает `access_actions`, декодирует `payload_json`, маппит `resource` на настроенный клубный чат или канал и исполняет действие через узкие consumer-интерфейсы, объявленные в пакете `enforcer`. Поддержаны `action_type`:

`ensure_invite`, `send_invite`, `approve_join`, `decline_join`, `soft_kick`, `hard_ban`, `unban`, `send_dm`, `verify_member`, `revoke_invite`.

Отдельные семантики:

- **`soft_kick`** проверяет, что пользователь не creator/admin ресурса, затем вызывает `banChatMember` и следом `unbanChatMember` с `only_if_banned=true`. Creator или administrator не кикается — действие завершается как expected no-op с warning.
- **`send_dm`** при Telegram `403` помечает пользователя `dm_state='blocked'` и завершается без retry — закрытая личка не сбой.
- **Ожидаемые no-op** (`approve_join`/`decline_join`/`soft_kick`/`revoke_invite` получают ошибку «уже сделано / уже невозможно») считаются успешным исполнением: action помечается `done`, событие логируется на `warn`, ретрая нет.

## Retry, throttle и dead-action alerts

Перед Telegram-вызовами Enforcer применяет общий rate limiter: примерно один message-вызов в секунду на chat, общий потолок заметно ниже 30 запросов/с, консервативный лимит для `getChatMember` около 1–2 запросов/с. Telegram `429` перепланирует action на `now + retry_after`; прочие retryable ошибки — с backoff и jitter. Воркер не держит открытую DB-транзакцию во время ожидания limiter'а или сетевого ответа: lease уже закоммичен, финальный статус пишется отдельной короткой транзакцией.

При достижении `max_attempts` action переходит в `dead`, и создаётся `admin_alert(kind='outbox_action_dead')`.

## Приёмка

- Доменное изменение и строка `access_actions` коммитятся одной транзакцией; при откате не сохраняется ни то ни другое; фактический вызов Telegram делает Enforcer после коммита.
- Дубль `idempotency_key` не создаёт второй строки; готовое действие лизится одним воркером; зависшее `running` подхватывается по истечении `locked_until`.
- `soft_kick` = ban + unban, но не для creator/admin; `send_dm` 403 → `dm_state='blocked'` без retry; ожидаемые no-op → `done` + warning.
- `429` → `run_after=now+retry_after` без падения процесса; исчерпание попыток → `status='dead'` + `outbox_action_dead`; сеть всегда вне DB-транзакции.
