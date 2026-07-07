# Tasks

## 1. Engine: decide/apply split
- [x] 1.1 `RevocationPlan` + `PlannedRevoke{Resource, UpdatedAt, Protected}` в `ports.go`
- [x] 1.2 `WithUserLock(tgID, fn)` со scoped non-reentrant контрактом
- [x] 1.3 `RevocationDecision` (decide: LiveSnapshot + `grantPlan`, на пуле, без tx/записи)
- [x] 1.4 `ApplyRevocation` (apply: `applyObservations` + revoke, в tx, `Members == nil`)
- [x] 1.5 `revokeNow` переведён на примитивы; удалены старые `revocationDecision`/`revokeGrant`

## 2. Grant snapshot revalidation
- [x] 2.1 `access_grants.updated_at` в проекции grant (`domain.AccessGrant.UpdatedAt`, `scanGrant`, 3 SELECT-а)
- [x] 2.2 `applyRevokes` ревокает только grant, совпадающий с decide-снимком по `updated_at`
- [x] 2.3 skipped grant (изменён/появился) → pending revocation сохраняется, даже при частичном отзыве

## 3. Reconciler pool-bound + WithTx
- [x] 3.1 `store.WithTx(ctx, db, fn)`
- [x] 3.2 `reconcile.New(db *sql.DB, …)` (compile-time guard, pool-bound)
- [x] 3.3 `revokeDue`: `WithUserLock` → `RevocationDecision(liveStore)` → `WithTx` → `ApplyRevocation(txStore)`
- [x] 3.4 `txStore` без `Members` (apply-стор без live checker)

## 4. `/sync` post-commit
- [x] 4.1 `RouteResult.PostCommitSync` + `SyncJob{Config, Target}`
- [x] 4.2 `adminSync` ставит post-commit job вместо inline reconcile; `messages.SyncStarted`
- [x] 4.3 Поллер исполняет job на пуле после `tx.Commit()`; итог в admin log

## 5. Тесты
- [x] 5.1 Регресс: `revokeDue` с pool-читающим источником не дедлочит (timeout-guard)
- [x] 5.2 Protection считается в decide (protected admin не soft-kick)
- [x] 5.3 Stale-identity grant не отзывается; pending сохраняется
- [x] 5.4 Частичный отзыв при появившемся гранте сохраняет pending
- [x] 5.5 `WithUserLock` non-reentrant contract
- [x] 5.6 `/sync` ставит `PostCommitSync` (не inline)
- [x] 5.7 `go build`, `go vet`, `gofmt`, `go test ./...` зелёные (кроме pre-existing webhook)

## 6. Follow-up (Phase 2, отдельными changes)
- [ ] 6.1 Immediate-mode (`EXPIRY_MODE=immediate`) split + запрет live `Members` в tx-сторах роутера
- [ ] 6.2 Healthcheck/автолечение: `/livez` через собственный пул + self-exit watchdog / autoheal
