# outbox-enforcer Specification

## Purpose
TBD - created by archiving change phase-04-outbox-enforcer-merge. Update Purpose after archive.
## Requirements
### Requirement: Decisions and execution are separated by durable outbox

Доменный код MUST записывать исходящее Telegram-действие в
`access_actions` до исполнения. Если действие связано с изменением
durable состояния, строка action MUST коммититься в той же
handler-транзакции (`tx2`), что и доменное изменение, `audit_log` и
terminal status входящего update. Telegram-вызов MUST выполняться
асинхронно Enforcer'ом после коммита.

Транзакция БД MUST NOT удерживаться открытой во время сетевого вызова
Telegram: сетевые probe выполняются до `tx2`, durable action пишется
внутри `tx2`, фактический side effect выполняется после коммита.

#### Scenario: Action и доменное изменение коммитятся атомарно
- **WHEN** обработчик меняет доменное состояние и должен выполнить
  исходящее Telegram-действие
- **THEN** доменное изменение и строка `access_actions` пишутся в одной
  SQL-транзакции
- **AND** при откате транзакции не сохраняется ни доменное изменение,
  ни строка action

#### Scenario: Telegram-вызов не выполняется внутри tx2
- **WHEN** обработка требует Telegram side effect
- **THEN** внутри `tx2` создаётся durable action
- **AND** фактический вызов Telegram выполняет Enforcer после коммита

### Requirement: Actions are idempotent and leased by workers

`access_actions.idempotency_key` MUST быть обязательным и уникальным.
Повторная постановка того же смыслового действия MUST NOT создавать
вторую строку. Готовыми к работе считаются строки
`status='queued' AND run_after<=now` и зависшие строки
`status='running' AND locked_until<now`.

Воркер MUST брать действие короткой lease-транзакцией: атомарно
перевести строку в `running`, выставить новый `locked_until` и
закоммитить до сетевого вызова. Зависшие `running` действия MUST
подхватываться заново после истечения lease.

#### Scenario: Дубль idempotency_key не создаёт второй строки
- **WHEN** action с уже существующим `idempotency_key` ставится
  повторно
- **THEN** в `access_actions` остаётся одна строка для этого ключа
- **AND** caller получает существующее действие или явный no-op без
  ошибки

#### Scenario: Готовое действие лизится одним воркером
- **WHEN** есть `queued` action с `run_after<=now`
- **THEN** один воркер переводит его в `running` и выставляет
  `locked_until`
- **AND** второй воркер не получает это же действие до истечения lease

#### Scenario: Зависшее running подхватывается по locked_until
- **WHEN** action остался в `running` с просроченным `locked_until`
- **THEN** следующий цикл воркера может взять его новым lease
- **AND** падение воркера не теряет действие навсегда

### Requirement: Enforcer executes all supported action types

Enforcer MUST быть единственной точкой доменных исходящих вызовов
Telegram. Он MUST читать `access_actions`, декодировать `payload_json`,
маппить `resource` на настроенный club chat или club channel и
исполнять action через узкие consumer-интерфейсы, объявленные в пакете
`enforcer`.

Поддержанные `action_type` MUST включать:
`ensure_invite`, `send_invite`, `approve_join`, `decline_join`,
`soft_kick`, `hard_ban`, `unban`, `send_dm`, `verify_member`,
`revoke_invite`. Ожидаемые no-op ошибки в контексте конкретного action
MUST считаться успешным исполнением и логироваться на уровне `warn`.

#### Scenario: soft_kick выполняет ban и unban
- **WHEN** Enforcer исполняет `soft_kick` для resource и пользователя
- **THEN** он проверяет, что пользователь не creator/admin
- **AND** вызывает `banChatMember`
- **AND** затем вызывает `unbanChatMember` с `only_if_banned=true`

#### Scenario: Creator или admin не кикается soft_kick
- **WHEN** `soft_kick` нацелен на creator или administrator ресурса
- **THEN** Enforcer не вызывает `banChatMember`
- **AND** action завершается как expected no-op с warning

#### Scenario: Ожидаемый no-op завершает action успешно
- **WHEN** `approve_join`, `decline_join`, `soft_kick` или
  `revoke_invite` получает Telegram-ошибку, означающую уже выполненное
  или уже невозможное действие
- **THEN** action помечается как `done`
- **AND** событие логируется как warning, а не ретраится

#### Scenario: Закрытая личка не ретраится
- **WHEN** `send_dm` получает Telegram `403`
- **THEN** пользователь помечается `dm_state='blocked'`
- **AND** action завершается без retry

### Requirement: Enforcer retries, throttles and raises dead-action alerts

Enforcer MUST применять общий rate limiter перед Telegram-вызовами:
примерно один message-вызов в секунду на chat, общий потолок заметно
ниже 30 запросов в секунду и консервативный лимит для `getChatMember`
около 1-2 запросов в секунду. Telegram `429` MUST перепланировать
action на `now + retry_after`. Прочие retryable ошибки MUST
перепланироваться с backoff и jitter.

Если число попыток достигает `max_attempts`, action MUST перейти в
`dead`, а система MUST создать `admin_alert` с kind
`outbox_action_dead`. Воркер MUST NOT держать открытую DB-транзакцию
во время ожидания rate limiter или сетевого ответа Telegram.

#### Scenario: Telegram 429 планирует retry_after
- **WHEN** Telegram отвечает `429` с `retry_after=17`
- **THEN** action возвращается в очередь с `run_after=now+17s`
- **AND** процесс Enforcer не падает

#### Scenario: Исчерпание попыток создаёт dead alert
- **WHEN** retryable action достигает `max_attempts`
- **THEN** строка получает `status='dead'`
- **AND** создаётся `admin_alert(kind='outbox_action_dead')`

#### Scenario: Сеть выполняется вне DB-транзакции
- **WHEN** Enforcer вызывает Telegram API
- **THEN** lease-транзакция уже закоммичена
- **AND** финальный статус записывается отдельной короткой транзакцией
