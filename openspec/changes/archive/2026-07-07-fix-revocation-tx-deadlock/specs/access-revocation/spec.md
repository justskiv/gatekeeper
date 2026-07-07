## ADDED Requirements

### Requirement: Revocation apply revalidates grants against the decide snapshot

Автоматический отзыв MUST вычислять live-вердикт protection на фазе decide (вне
транзакции) и применять отзыв на фазе apply (в транзакции). Apply MUST
ревокать grant ресурса ТОЛЬКО если этот grant не изменился с момента decide
(идентичность по `updated_at`) и был оценён как не protected. Grant, который
изменился или появился после decide, MUST быть пропущен, а его live-вердикт
MUST NOT применяться по устаревшему снимку.

`pending_revocation` MUST быть сохранён для следующего прохода, если хотя бы
один eligible grant был пропущен — даже когда другие grants в этом проходе уже
отозваны. Клиринг `pending_revocation` MUST происходить только когда весь
eligible-набор grants разрешён (отозван или protected) без пропусков.

#### Scenario: Изменённый после decide grant не отзывается по устаревшему вердикту
- **WHEN** grant ресурса изменился между decide и apply
- **THEN** apply не ревокает этот grant
- **AND** `pending_revocation` сохраняется для повторной оценки

#### Scenario: Появившийся после decide grant сохраняет pending
- **WHEN** apply отзывает один grant, но другой eligible grant появился после
  decide
- **THEN** появившийся grant не отзывается в этом проходе
- **AND** `pending_revocation` не удаляется, несмотря на частичный отзыв
