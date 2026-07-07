# Design

## Context

`store.Open` задаёт `SetMaxOpenConns(1)` — единственное SQLite-соединение,
сериализующее запись (DSN: WAL + `busy_timeout` + `_txlock=immediate`). Класс
дефекта: транзакция удерживается через I/O, которым не управляет. На revoke-пути
было **два** live-обращения внутри write-транзакции:

1. `revocationDecision → LiveSnapshot` — источники читают пул → второй запрос за
   единственным соединением → self-deadlock (сам инцидент);
2. `revokeGrant → isProtected → Members.GetChatMember` — Telegram-вызов внутри
   write-цикла (держит соединение через сеть).

Восстанавливаемый инвариант: **внутри транзакции — только tx-scoped
DB-операции; никаких live/pool/network обращений.**

## Decisions

### Разделение decide/apply
- `Engine.RevocationDecision(ctx, live Store, tgID, reason) → RevocationPlan` —
  decide-фаза: `LiveSnapshot` (или `persistedDecision` без источников) + live
  `isProtected` по eligible grants. На пуле, без транзакции, без записи.
- `Engine.ApplyRevocation(ctx, tx Store, tgID, plan) → []Effect` — apply-фаза:
  в транзакции caller'а, `Members == nil`, только tx-scoped запись
  (`applyObservations` атомарно с revoke). Без live/pool/network.
- `Engine.WithUserLock(tgID, fn)` — scoped per-user lock, охватывает обе фазы.
  **Non-reentrant contract**: внутри `fn` нельзя звать методы, берущие тот же
  lock (`RevokeNow`/`RecomputeAccess`/`ApplyObservations`/`HandleEvent`); только
  не-локающие `RevocationDecision`/`ApplyRevocation`.

Публичный `RevokeNow` сохранён для pool-вызовов (поведение не меняется):
внутренне зовёт decide+apply на одном `Store`.

### Ревалидация grant-снимка (apply)
`access_grants` имеет стабильную строку per `(tg_id, resource)`; `id` не меняется
при re-grant. Идентичность снимка — `updated_at`. `RevocationPlan.Revokes` несёт
per-grant `{Resource, UpdatedAt, Protected}`. Apply перечитывает eligible grants
в транзакции и ревокает grant **только** если он совпадает с decide-снимком по
`updated_at` и не protected. Изменившийся или появившийся после decide grant
пропускается; если что-то пропущено — pending revocation **не удаляется** (даже
если часть grants уже отозвана), чтобы следующий проход переоценил его live.

### Reconciler pool-bound + `/sync` post-commit
- `reconcile.New(db *sql.DB, …)` — reconciler владеет своими короткими
  транзакциями (`store.WithTx`) и никогда не вложен в чужую. Compile-time guard.
- `/sync` (`router.adminSync`) не запускает reconcile внутри update-транзакции:
  ставит `RouteResult.PostCommitSync` (`SyncJob{Config, Target}`); поллер после
  `tx.Commit()` исполняет job на пуле, итог — в admin log. Роутеру пул не даётся.

## Trade-offs / Risks

- Окно decide→apply (protection считается в decide) закрыто per-user локом и
  ревалидацией по `updated_at`; изменённый grant не отзывается по устаревшему
  вердикту, а переоценивается следующим проходом.
- `/sync` теперь асинхронный: владелец видит «запущена», сводка приходит
  отдельным сообщением. Ядро требования («один пользователь / полный pass»)
  сохранено.

## Out of scope (Phase 2)

- Immediate-mode (`EXPIRY_MODE=immediate`) split и запрет live `Members` в
  tx-сторах роутера (в проде `grace`, путь спящий).
- Healthcheck/автолечение (HTTP `/livez` через собственный пул + self-exit
  watchdog / autoheal).
