## ADDED Requirements

### Requirement: Граница зависимостей пакета domain

Пакет `internal/domain` SHALL содержать только value types и constants
для бизнес-модели. Он SHALL NOT содержать behavioral interfaces и SHALL
NOT зависеть от инфраструктурных пакетов проекта.

#### Scenario: Нижние слои импортируют domain types

- **WHEN** configuration, storage, bot или integration code нужны общие
  бизнес-значения
- **THEN** они импортируют пакет `domain`
- **AND** пакет `domain` не импортирует эти вызывающие пакеты обратно

#### Scenario: Domain remains infrastructure-free

- **WHEN** пакет `internal/domain` компилируется
- **THEN** его imports ограничены стандартной библиотекой, необходимой
  для value types, сейчас `time`
- **AND** никакие `internal/config`, `internal/store`, bot или Telegram
  packages не импортируются из `domain`

### Requirement: Модель источников подписки

Domain model SHALL представлять subscription platforms как Boosty,
Tribute и manual sources, а subscription states как active и expired
periods.

#### Scenario: У пользователя есть subscription period

- **WHEN** подписка представлена в domain code
- **THEN** она содержит Telegram user ID, platform, status, period
  timestamps, optional external identifiers, tier и signal metadata

### Requirement: Модель итогового статуса доступа

Domain model SHALL представлять source verdicts и итоговый access status
значениями active, inactive и unknown.

#### Scenario: Решение о доступе требует объяснения

- **WHEN** код представляет причину наличия или отсутствия доступа
- **THEN** он может включить один или несколько access reasons с
  source, verdict, human-readable detail и optional expiry time

### Requirement: Модель доступа к управляемым ресурсам

Domain model SHALL представлять managed club resources, invite link
states, access grant states и pending revocations, нужные для будущего
Telegram enforcement behavior.

#### Scenario: Пользователь получает доступ к managed resource

- **WHEN** доступ к club chat или club channel представлен в коде
- **THEN** grant фиксирует resource, state, admission source, join time,
  revocation time и revocation reason
