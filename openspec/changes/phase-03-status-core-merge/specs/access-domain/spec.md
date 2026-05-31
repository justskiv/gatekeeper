## MODIFIED Requirements

### Requirement: Модель итогового статуса доступа

Domain model SHALL представлять source verdicts четырьмя значениями:
`active`, `inactive`, `unknown` и `no_signal`. `no_signal` означает, что
источник не дал применимого сигнала, и SHALL быть отличим от `inactive`
(отсутствие основания — это не отрицательный ответ). Итоговый access
status SHALL оставаться трёхзначным: `active`, `inactive` и `unknown`.

#### Scenario: Вердикт источника отличает отсутствие сигнала
- **WHEN** manual-источник не находит whitelist или активную
  `manual`-подписку
- **THEN** domain code может представить это значением `no_signal`
- **AND** оно отличимо от `inactive`

#### Scenario: Решение о доступе требует объяснения
- **WHEN** код представляет причину наличия или отсутствия доступа
- **THEN** он может включить один или несколько access reasons с source,
  verdict, human-readable detail и optional expiry time
- **AND** итоговый статус остаётся одним из `active`, `inactive` или
  `unknown`
