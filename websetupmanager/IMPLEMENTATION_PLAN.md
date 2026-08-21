# Web Setup Manager — implementation plan и матрица покрытия

Последнее обновление: 2026-08-21. Ветка: `codex/web-setup-manager`.
Текущий normative source:
[PRODUCT_REQUIREMENTS.ru.md](PRODUCT_REQUIREMENTS.ru.md).

Прямая продуктовая корректировка заменила multi-program managed library на
LinuxCNC catalog: один Setup имеет не более одной программы и одной Setup Sheet,
может быть неполным, группируется физическими folders под `PROGRAM_PREFIX` и не
имеет validation/readiness/current workflow. Старый план и матрица 221 P0 /
`AC-01`–`AC-20` сохранены ниже в свёрнутом историческом разделе, но больше не
являются заявлением о текущей приёмке.

## Актуальная архитектура

```text
┌──────────────────────────── React SPA ─────────────────────────────┐
│  catalog file tree (слева) │ G-code / inline Setup Sheet (справа) │
└──────────────────── same-origin /api/v1/catalog ───────────────────┘
                               │
                     PAM session + CSRF
                               │
                         Go catalog API
                 ┌─────────────┴─────────────┐
                 │                           │
       SQLite IDs/revisions/audit     held PROGRAM_ROOT FD
                                      named program/sheet files
                                                │
                                  LinuxCNC PROGRAM_PREFIX / QtDragon
```

Backend не имеет endpoint исполнения и не обращается к LinuxCNC NML. Он только
проверяет active INI, безопасно публикует файл в `PROGRAM_ROOT` и сообщает
operator-facing relative location. Физический root фиксирован server config;
browser не может выбрать другой каталог хоста.

## Этапы текущей реализации

| Этап | Результат | Статус на 2026-08-21 |
|---|---|---|
| 0. Direction reset | новый normative source, decisions, host discovery, migration boundary | выполнено в документации |
| 1. Catalog backend | config/INI match, additive schema, folders/setups, singular files, scoped API | реализовано; catalog service/HTTP integration suites прошли |
| 2. Direct filesystem storage | root-FD traversal, prepared atomic publish, conflicts, durable recovery | реализовано; path/race, real SIGKILL и sparse 10 GiB suites прошли |
| 3. Compact frontend | left file tree/right viewer, native file picker, inline Sheet, folder/component operations | реализовано; 15 files / 103 tests/build, сквозной keyboard-only flow и Firefox desktop/mobile visual smoke прошли |
| 4. Legacy migration | 0/1/N split, sheet fan-out, provenance, manifest и no-replace publication | выполнено на host; schema v4, 2 completed mappings и 4 copied manifests, restart idempotent |
| 5. Integrated verification | automated catalog regression и production wiring | full build/test gates и host integrity/hash/readiness/no-execution checks прошли |
| 6. Deployment | cold backup, verified restore rehearsal, versioned release и direct HTTPS smoke | выполнено; внешний LAN client, DHCP reservation, target performance и ручной QtDragon отмечены отдельно |

Подробная безопасная последовательность преобразования данных находится в
[MIGRATION_PLAN.md](MIGRATION_PLAN.md).

### UI refinement от 2026-08-21

- G-code — родительский file node Setup; существующая Setup Sheet показана
  дочерним node и открывается inline в той же правой editor surface.
- «Добавить» сразу открывает native multi-file picker: один G-code обязателен,
  одна PDF/HTML Sheet необязательна и может быть прикреплена позже.
- Editor не повторяет breadcrumb/commandbar/filename; точный путь остаётся в
  одной status bar. Первый paint получает один 64-КиБ prefix до Worker index.
- Неоднозначно прерванный upload повторяется с тем же idempotency key; новый
  выбор файла после детерминированной ошибки всегда создаёт новый intent/key.
- Search-forced tree expansion использует единое visual/ARIA/keyboard state;
  mobile drawer делает фон inert, удерживает Tab и возвращает focus после
  Escape. PDF text alternative ограничена streaming budget 100 000 символов /
  20 000 items.
- Application CSP разрешает `blob:` только в `frame-src`, потому что очищенный
  HTML показывается через originless empty-sandbox iframe; CSP самого документа
  по-прежнему запрещает script/network/forms/navigation.
- Исторический sheet-only/empty Setup не скрывается и может быть восстановлен
  добавлением G-code; новый UI такие записи не создаёт.

## Фактическая production generation

- Release: `/opt/websetupmanager/releases/266917d3ed04`; source commit
  `266917d3ed04b3245f7e0f3461128a6d0d0bea0d`; SHA-256 binary
  `5d50c3b708eff7ba2262d3958d7caa9c533745d351a76de99d24d4c120cfc202`.
- Cold generation:
  `/var/backups/websetupmanager/pre-catalog-20260821T145214Z`; все четыре archive
  проверены по записанным SHA-256, полный extract/diff завершился marker
  `RESTORE_CHECK_OK`.
- Unit enabled/active от `user`, direct HTTPS `10.0.1.136:443`; listener на
  TCP/80 отсутствует; `/healthz` и `/readyz` вернули 200. Optional Bearer не
  настроен. Интерактивный вход использует PAM account `user` и текущий системный
  Linux password; значение password нигде не записывается.
- SQLite schema v4, `legacy_migration_state=completed`: 2 folders, 2 setups,
  4 files, 2 completed mappings и 4 copied manifest rows. `quick_check=ok`,
  foreign-key violations — 0; source/target size и SHA-256 совпали, legacy rows
  и objects сохранены, journal/staging temp remnants отсутствуют.
- Повторный restart/migration не изменил counts. LinuxCNC snapshot до/после WSM:
  `file=""`, `state/mode/interp/exec=1/1/1/2`; приложение ничего не загрузило и
  не запустило.
- Headless Firefox ESR через WebDriver BiDi на production HTTPS прошёл guest,
  PAM login пользователя `user`, ready/catalog/UI и logout. Проверены desktop
  `1366x768` и mobile `390x844`; authenticated desktop отрисовал 37 virtual
  G-code rows, первая строка `%`, viewport `1030x516`.
- Первый visual run обнаружил G-code viewport высотой 0 px. Commit
  `18411e613b380c4b73837003b96c949a21661041` заменил editor grid на flex;
  повторный production run показал подсвеченный G-code и catalog tree.
- Финальный keyboard-only integration flow обнаружил и закрыл две focus-регрессии:
  portal `autoFocus` больше не теряет инициатор dialog, а переход к строке не
  remount-ит spinbutton. Commit `12aa6a2adf3c9908a2120c03ed310aa40ac1fecc`
  прошёл 15 files / 87 tests, production build и установлен отдельным release;
  post-restart `/healthz`/`/readyz` и guest login contract снова вернули 200.
- Refinement commit `266917d3ed04b3245f7e0f3461128a6d0d0bea0d` прошёл
  15 files / 103 tests, Go PAM/race/vet, production build и disposable Firefox
  visual smoke. После atomic release switch production PAM login/catalog/logout
  снова прошли; 37 virtual rows, первая `%`, viewport `1030x625`, health/ready
  200, `NRestarts=0`, TCP/80 отсутствует.
- Read-only QtDragon audit подтвердил running `g540.ini`, local
  `_CORVUS_FILE_MANAGER`, тот же `PROGRAM_PREFIX` и точную видимую моделью
  цепочку `linuxcnc/nc_files/Импортировано/adssad` со строками `1002.ngc` и
  `1003.ngc`. Вкладка File не открывалась, selection/LinuxCNC state не менялись.
- Screenshot evidence: `/tmp/wsm-catalog-evidence.ZSP7Ft`; SHA-256 login
  desktop/mobile — `fbf1e313ec372d6f87473860a8e87263c4682868e0357a4903426088d2087773` /
  `cdb6610b62b4c4bcd4812efa50d6ebf13e81a278cb8616ecc1cb259db368f0ae`,
  authenticated desktop/mobile —
  `0b4c7f29b2761fb78fe0dedb7aac6d1a91bcfc99ab93f89ef5d797bf6c6c305d` /
  `d036fa0664c7be11ff074d7ca42a2074d4a20102a9a0b0669d97b8901cb04ebc`.
- Refinement evidence: local UI `/tmp/wsm-ui-evidence-csp.OedI4k` (program /
  inline Sheet / mobile SHA-256 `f83cedd4ab5b494717b45d5acf3724a520e65ffbcb9c11f0241edb08e08ef971` /
  `feadf0f6a31790df77376fb34657f533788242ef00d993caa48d38c76c6d536d` /
  `7ea399e3695d3922e71bd4170a14648a01f8de3c92f72e421fc1dd2acc9d0aa0`);
  production PAM `/tmp/wsm-production-auth-refinement.OfbZwm` authenticated
  desktop/mobile SHA-256 `ec322875a2a8aceadd719cd61623ac76a6b71010949f9c91066c75d7edf6310b` /
  `854acd9fe3036114e2b280cbd8f7cb64b99b584be0818d98a208484ee7ccbc0d`.
- Отдельной target qualification, не входящей в текущие `CAT-AC`, остаются
  проверка с другого LAN client, DHCP reservation, controlled 10 GiB browser
  performance и ручной визуальный поиск файлов в QtDragon.

## Актуальная модель данных

| Entity | Обязательные данные и invariants |
|---|---|
| `catalog_folders` | opaque ID, nullable parent, display/normalized name, revision; hierarchy соответствует real directories под root |
| `catalog_setups` | opaque ID, nullable folder, display name, revision; новый UI создаёт запись только из G-code, исторический неполный Setup допустим и восстанавливаем |
| `catalog_files` | setup ID, unique role `program` или `setup_sheet`, relative basename/path, size/digest, inode/version identity; максимум один файл каждой роли |
| `catalog_operations` | durable intent/storage/DB/terminal checkpoints для publish, move, delete и folder operations; recovery привязана к ожидаемым identity/digest/version |
| `catalog_state`, `catalog_legacy_*` | общий migration state, source→target mapping и per-role manifest; completed mapping повторно сверяется с source provenance, catalog row и physical file |
| auth/session/audit/idempotency | переиспользуются без хранения password/raw remembered token или абсолютного program root в public data |
| legacy tables/objects | read-only migration/rollback source до отдельного подтверждённого cleanup |

`rootDisplay` — server-configured operator-facing строка
`~/linuxcnc/nc_files`; `relativePath` разрешён внутри root. Ни одно из них не
является входом для выбора произвольного filesystem root.

## Актуальная структура API

Публичный namespace — `/api/v1/catalog`, не `/fs`. Контракт перехода:

| Method/path | Назначение |
|---|---|
| `GET /api/v1/catalog` | дерево folders/setups, destination `rootLabel`/`rootDisplay` и generation |
| `POST /api/v1/catalog/folders` | создать физический folder под root |
| `PATCH/DELETE /api/v1/catalog/folders/{folderId}` | rename/move/delete пустого folder с expected revision |
| `POST /api/v1/catalog/setups` | создать Setup-запись в folder/root как первый шаг простого G-code upload; совместимый API допускает recovery исторических неполных записей |
| `PATCH/DELETE /api/v1/catalog/setups/{setupId}` | rename/move и безопасное удаление Setup; detail уже входит в catalog snapshot |
| `PUT/DELETE /api/v1/catalog/setups/{setupId}/program` | streaming add/replace/delete единственной программы |
| `HEAD/GET /api/v1/catalog/setups/{setupId}/program/content` | Range/ETag preview программы |
| `PUT/DELETE /api/v1/catalog/setups/{setupId}/setup-sheet` | streaming add/replace/delete единственной sheet |
| `HEAD/GET /api/v1/catalog/setups/{setupId}/setup-sheet/content` | version-bound безопасный viewer content |

Mutations используют opaque IDs, expected revision, session CSRF и
`Idempotency-Key`. Создание component требует ровно `If-None-Match: *`, а
replace/delete — ровно один `If-Match: "<64 lowercase hex version>"`; смешанный,
некавыченный или множественный precondition отклоняется до filesystem effect.
Content GET/HEAD может дополнительно связать чтение с `version` query и точным
ETag; каждый Range viewer-запрос посылает `If-Match`.
Ответы содержат `relativePath`/`rootDisplay`, но не canonical absolute root,
storage key, inode/device или staging name.

## Актуальная storage strategy

- `PROGRAM_ROOT` и `LINUXCNC_INI` canonicalize до listener; INI
  `PROGRAM_PREFIX` обязан совпасть с root.
- Root удерживается открытым directory FD. Каждый component разрешается beneath
  без symlink; reserved `ngcgui_lib`, traversal, absolute/NUL, hardlink и special
  files отклоняются.
- Program allowlist на фактическом станке: `.ngc`, `.nc`, `.tap`; sheet:
  PDF/standalone HTML. `.py` и image-to-gcode filter inputs не принимаются.
- Upload потоково пишет exclusive hidden regular temp в target filesystem,
  проверяет лимит/free space/identity, синхронизирует file, публикует atomic
  rename и синхронизирует directory.
- Create использует no-replace. Replace/delete связаны с expected revision и
  file version; конфликт не уничтожает новые внешние bytes.
- Startup до listener последовательно завершает legacy journal/import recovery,
  проверяет legacy content identity, восстанавливает catalog journal и только
  затем запускает idempotent legacy migration. Ошибка или `manual_review`
  блокирует listener; resume незавершённого общего run повторно проверяет
  completed per-source provenance/manifest, а общий terminal `completed`
  становится безопасным no-op на следующем старте.
- Recovery очищает только journal-owned temp с проверяемым именем и identity.
  Неизвестные entries и legacy objects не удаляются.
- Systemd сохраняет `ProtectHome=read-only`, добавляя только точный
  `ReadWritePaths=/home/user/linuxcnc/nc_files`.

## Актуальная тестовая стратегия

| Gate | Имеющееся automated evidence | Остаётся до target pass |
|---|---|
| Config/INI | fail-closed suites и actual service start against `g540.ini`; readiness 200 | внешний LAN/DNS smoke |
| Domain/API | incomplete/singular CRUD, exact preconditions/routes плюс production Firefox guest/login/catalog/UI/logout smoke | внешний LAN client |
| Storage security | traversal, reserved tree, symlink/hardlink/special substitution, identity races, no-replace, rollback/recovery | target filesystem spot-check |
| Upload/recovery | streaming unknown length, prepared publication, journal phases, actual subprocess SIGKILL and same-key retry | target disconnect/power-loss drill |
| Viewer | single 64 KiB prefix-before-Worker test, sparse 10 GiB bounded Range/ETag/Worker suites, cancellable bounded PDF text и Firefox render G-code/inline HTML | controlled target-hardware performance и malicious-document observation |
| Frontend | 15 files / 103 tests/build; left-tree parent/child, inline Sheet, direct dual/single upload, stable retry, later attach и сквозной keyboard-only flow; Firefox desktop/mobile smoke | дополнительный native-key walkthrough на отдельном managed client |
| No execution | catalog-only route gate, exact target publication/root binding и live LinuxCNC snapshot unchanged | дополнительный ручной visual confirmation в QtDragon |
| Migration | automated 0/1/N/restart/collision suites плюс live manifest/hash/count/restart verification | legacy cleanup только отдельным будущим решением |
| Authentication | PAM/session/CSRF/throttle suites плюс production Firefox PAM login/session/logout; Bearer unset | внешний managed-client login only if separately required |
| Production | full gates, backup/restore, release, integrity/hash/ready и desktop/mobile BiDi visual evidence | LAN client, DHCP reservation, target performance, manual QtDragon |

## Матрица CAT-P0

`V` означает пройденное automated evidence именно catalog-версии, `P` —
реализация и часть автоматизированного evidence есть, но остаётся обязательный
browser/live шаг, `D` — процедура документирована, target drill не выполнен.
`V` не заменяет отдельно отмеченную target qualification.

| ID | Planned evidence | Статус |
|---|---|---|
| `CAT-P0-001` | Workbench/App component tests assert left file tree/right viewer composition | V |
| `CAT-P0-002` | compact/resizable implementation plus production desktop/mobile visual rerun after grid→flex fix | V |
| `CAT-P0-003` | folder service/HTTP CRUD plus `CatalogTree` G-code parent/Sheet child levels, selection and keyboard tests | V |
| `CAT-P0-004` | schema unique role and service/HTTP singular-component tests | V |
| `CAT-P0-005` | native G-code + optional Sheet create, program-only leaf/later attach, legacy sheet-only recovery; no validation flow | V |
| `CAT-P0-006` | current-folder selection, filename-derived Setup name and persistent exact success location tests | V |
| `CAT-P0-007` | direct atomic named publication/exact bytes plus running QtDragon config/log and read-only matching `QFileSystemModel` rows `1002.ngc`/`1003.ngc` verified; manual hidden-tab screenshot is only an operator qualification | V |
| `CAT-P0-008` | catalog-only route плюс live unchanged LinuxCNC snapshot `file=""`, `1/1/1/2` | V |
| `CAT-P0-009` | folder/setup create/rename/move/delete, revision and recovery suites | V |
| `CAT-P0-010` | singular streaming add/replace/delete including unknown-length body tests | V |
| `CAT-P0-011` | exactly one 64 KiB initial Range becomes visible before Worker; catalog Range/ETag, virtualization/index/search suites remain green | V |
| `CAT-P0-012` | inline PDF canvas and sanitized HTML CSP/blob/empty-sandbox tests; no Sheet dialog in Workbench | V |
| `CAT-P0-013` | catalog-only production gate, safe route and filename attack tests | V |
| `CAT-P0-014` | catalog snapshot/API leak assertions cover relative path/root display only | V |
| `CAT-P0-015` | root-FD traversal/reserved/symlink/hardlink/special/race suite | V |
| `CAT-P0-016` | prepared publish, rollback, journal recovery and actual SIGKILL suite | V |
| `CAT-P0-017` | exact conditional headers, no-replace and substitution/version conflicts | V |
| `CAT-P0-018` | active extensions, reserved tree and special-file rejection tests | V |
| `CAT-P0-019` | catalog filter plus loading/empty/offline/error/conflict component states | V |
| `CAT-P0-020` | end-to-end keyboard-only App flow through left tree/child Sheet/native picker/tabs/search/logout plus focused tree/viewer/modal tests | V |
| `CAT-P0-021` | PAM/session/CSRF/throttle foundation plus production Firefox PAM login/session/logout | V |
| `CAT-P0-022` | startup/readiness order, root replacement and INI mismatch tests | V |
| `CAT-P0-023` | three-root cold generation; four archive hashes and full extract/diff `RESTORE_CHECK_OK` | V |
| `CAT-P0-024` | 0/1/N no-delete migration, provenance, manifest/hash, restart and manual-review tests | V |

## Матрица CAT-AC

| AC | Status / remaining evidence |
|---|---|
| `CAT-AC-01` | V — actual host/root/INI, active unit and live health/readiness verified |
| `CAT-AC-02` | V — physical nested-folder create/reload plus React left-tree reload behavior covered together by HTTP/service and App/tree tests |
| `CAT-AC-03` | V — native picker directly uploads G-code + optional Sheet; program-only leaf and later direct Sheet attach automated without application create popup |
| `CAT-AC-04` | V — atomic nested publication/exact bytes, running QtDragon root/model visibility and unchanged live LinuxCNC loaded-file invariant verified; no input was sent to the hidden File tab |
| `CAT-AC-05` | V — direct attach/replace, exact create/replace preconditions and conflicts automated |
| `CAT-AC-06` | V — one 64 KiB prefix appears before Worker in automation; sparse 10 GiB Range/ETag/virtualization suites and Firefox first-line/inline-Sheet smoke passed |
| `CAT-AC-07` | V — DOM/style tests plus desktop/mobile Firefox screenshots confirm compact left-tree/right-viewer, one status path and no duplicate header rows |
| `CAT-AC-08` | V — traversal/absolute/symlink/hardlink/special/race suite passed |
| `CAT-AC-09` | V — prepared rollback, journal phases and real subprocess SIGKILL recovery passed |
| `CAT-AC-10` | V — external same-content/substitution/version conflict suites passed |
| `CAT-AC-11` | V — one integration scenario uses keyboard-only login→left tree/child Sheet→native picker trigger→preview search/line jump→logout; focused suites verify roving tree, editor tabs, modal trap/return and visible focus |
| `CAT-AC-12` | V — production build, Go unit/integration/race/vet, frontend lint/typecheck/tests, path-security, local health/ready and real production PAM smoke all passed |

## Историческая implementation/evidence matrix (до 2026-08-21)

<details>
<summary>Показать старый план и покрытие 221 P0 / AC-01–AC-20</summary>

Статусы ниже относятся только к архивному
[functional-requirements.ru.md](functional-requirements.ru.md):

- `V` — verified в прежней managed-library версии;
- `I` — implementation present, manual scenario не записан;
- `P` — partial;
- `X` — target/external qualification pending.

## Архитектура

Web Setup Manager — отдельный непривилегированный Go-процесс. React SPA работает
same-origin и видит только domain `/api/v1`. SQLite хранит сущности/состояние;
immutable object store хранит bytes. Абсолютные пути, storage keys, inode и
physical layout не входят в public DTO/error. Endpoint для исполнения LinuxCNC
или универсального `/fs` отсутствует.

```text
React SPA + PDF.js + G-code Web Worker
                    |
       same-origin /api/v1 + session CSRF
                    v
Go HTTP API -------- login --------> Linux PAM (configured account)
      |
      v
Service transactions -------------> SQLite/WAL
      |                              journal/audit/jobs/UI state
      |                              remembered token hashes
      v
root-FD managed immutable object store
```

| Package | Ответственность |
|---|---|
| `cmd/websetupmanager` | config/startup, non-root guard, TLS, signals, maintenance, embedded SPA |
| `internal/config` | defaults/env, fail-closed remote policy, canonical disjoint roots/TLS files |
| `internal/auth` | Linux PAM adapter, opaque memory/remembered SQLite sessions and bounded login throttling |
| `internal/database` | process lock, quick-check, WAL/FK/FULL, embedded checked migrations, backup/recovery |
| `internal/domain` | stable IDs/DTO/errors, names, states/revisions, streaming G-code validation |
| `internal/storage` | held root FDs, openat2/*at resolver, staging/publish/read/version/GC primitives |
| `internal/service` | setup/import/artifact/state/validation/duplicate/jobs/idempotency/journal/audit/reconcile |
| `internal/httpapi` | domain routes, Range/ETag, stable errors, sanitization, auth/CSRF/security headers |
| `web/src` | library/detail/dialogs, import, current/recent, preview worker and controlled viewers |

## Реализованные этапы

1. Каркас, production embedding, config, health/readiness and build commands.
2. Domain/SQLite, revisions/states, current/recent/jobs/journal/audit/idempotency.
3. Root-anchored streaming storage, immutable publication, identity and GC.
4. Domain service/API for complete setup lifecycle.
5. Setup library/card/import/current/jobs/accessibility baseline.
6. Range/ETag virtualized G-code preview and PDF/HTML viewers.
7. Acceptance/path/race/sparse/recovery suites and operator documentation.
8. PAM login/browser-session implementation, remembered-session migration and
   accessible login/logout UI; PAM-tagged test/race/vet/build, 84 frontend tests
   and live direct-TLS login/remember/restart/logout verified.

Фактический progress/gate log: [PROGRESS_LOG.md](PROGRESS_LOG.md). Решения
неоднозначностей: [DECISIONS.md](DECISIONS.md).

## Модель данных

SQLite хранит metadata, но не большие bytes. Checked migrations создают:

| Table | Назначение и ключевые инварианты |
|---|---|
| `library_instances` | stable opaque library ID/fingerprint, изоляция state разных roots |
| `setups` | ID, name/description/source, status, revision, timestamps, archived prior status |
| `setup_artifacts` | stable artifact ID, setup FK, `program|setup_sheet`, display/collision name, primary/order, expected identity/version |
| `storage_objects` | internal SHA-256 key, media type/size/hash/ref count; immutable/shared |
| `validation_runs` | setup/revision-bound state, issues/result and job link |
| `current_setup` | максимум один setup/revision/timestamp на library |
| `recent_setups`, `ui_state` | stable-ID history and per-client persisted navigation |
| `import_sessions`, `import_artifacts` | staged manifest, roles, progress, expiry and published references |
| `jobs` | kind/state/progress/bytes/result/error/cancellation; stable terminal result |
| `operation_journal` | intent/storage/DB/terminal recovery checkpoints and object reservations |
| `audit_events` | safe operation/entity/revision/result history |
| `idempotency_requests` | key + canonical request hash + typed result + TTL |
| `delete_confirmations` | setup/revision/name-bound short-lived irreversible token |
| `auth_sessions` | только remembered session: SHA-256 token hash, username, CSRF, creation/expiry и deployment scope |

IDs — random canonical 128-bit hex. `setups.revision` increments once in the same
SQLite transaction as a successful logical mutation. Ready requires at least one
available program and optional configured sheet. Mutations of ready become draft;
external identity mismatch becomes attention; archive preserves prior state.

Object reference truth is DB FK/ref rows plus journal reservations, not filename.
Duplicate gets new setup/artifact IDs but may share immutable object; future
replacement changes only one link. See journal checkpoint collapse in D-016.

Обычные browser sessions живут только в bounded memory store. Remembered row не
содержит Linux password или raw cookie token; смена configured user/deployment
scope инвалидирует несовместимые rows. Session cookie остаётся недоступной
JavaScript, а CSRF выдаётся только уже аутентифицированной same-origin session.

## Public API

JSON uses camelCase. Errors contain stable `code`, `message`, `requestId`, optional
`details` and `retryable`. Mutations use `Idempotency-Key`; setup mutations carry
`expectedRevision`, affected artifacts carry opaque version/ETag.

| Area | Routes |
|---|---|
| Runtime | `GET /healthz`, `GET /readyz`, `GET /api/v1/capabilities` |
| Authentication | `GET /api/v1/auth/session`, `POST /api/v1/auth/login`, `POST /api/v1/auth/logout` |
| Library/detail | `GET/POST /api/v1/setups`, `GET/PATCH /api/v1/setups/{setupId}` |
| Import | `POST /api/v1/setup-imports`, `GET/POST/DELETE .../{importId}`, `POST .../artifacts`, `POST .../commit` |
| Programs | `POST .../{setupId}/programs`, `PUT/PATCH/DELETE .../programs/{artifactId}`, `HEAD/GET .../content` |
| Setup Sheet | `GET/PUT/DELETE .../{setupId}/setup-sheet`, `GET .../setup-sheet/content` |
| Lifecycle | `POST .../validate`, `.../duplicate`, `.../archive`, `.../restore`, `.../delete-plan`; `DELETE .../{setupId}` |
| State/history | `GET/PUT /current-setup`, `GET/DELETE /recent-setups`, `GET/PUT /ui-state`, `GET .../audit|validations` |
| Jobs | `GET /jobs`, `GET/DELETE /jobs/{jobId}` |

No arbitrary path, download, execute, LinuxCNC or filesystem-browse route exists.
В remote mode SPA/assets, probes и auth routes доступны до входа, а domain API и
capabilities требуют opaque session cookie либо явно настроенный optional
Bearer. HTTP Basic отсутствует. Session login/logout/mutations требуют exact
HTTPS Host/Origin; каждая session имеет собственный CSRF. Local mode возвращает
implicit authenticated session и сохраняет прежний startup-CSRF contract.

## Storage strategy

`library_dir` and `state_dir` are startup-only, canonical, writable, disjoint
real directories. They are opened once as directory FDs. Linux operations use
`openat2(RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_SYMLINKS)` or a tested
component-wise `openat`/`O_NOFOLLOW` fallback; opened content must pass `fstat`
as regular file. `O_NONBLOCK` prevents FIFO/device stalls.

Uploads stream to exclusive non-executable state staging while calculating hash,
validating type/content, enforcing configured/import/free-space budgets and
observing cancellation. Publication uses an exclusive temp below the managed
root, file/directory `fsync` and atomic rename to an immutable SHA-256 object.
Only then does a journal reservation permit DB adoption. Error/cancel/restart
cleans staging; GC checks refs and active journal rows in one coordinated DB
decision before unlink. Names never become storage paths or shell input.

Opaque artifact version includes object identity, size, mtime and ctime. Content
reopens by internal key, verifies expected immutable hash/version and exports a
digest-derived ETag. HEAD/Range are 64-bit; every frontend block sends If-Match.
Identity mismatch yields stable `ARTIFACT_CHANGED`, marks setup attention and
prevents mixed-version preview.

A matched cold copy may legitimately change device/inode/ctime. The pre-listener
identity pass marks affected setups `attention`; the first full background scrub
may update only the physical version fields, and only after SHA-256 still matches
the immutable catalog value. Bytes that differ are never rebound. The setup does
not become `ready` without a new revision-bound validation.

Network I/O timeout is sliding: every request-body read and response write resets
its connection deadline. Progressing large upload/download has no absolute
wall-clock cutoff; stalled upload and slow/non-reading response client do.
`ReadHeaderTimeout` remains separate and server-wide absolute ReadTimeout is 0.

For every durable asynchronous mutation, the domain result and its terminal job
snapshot are committed in the same SQLite transaction. Upload completion also
commits the outer `runUploadJob` idempotency result and content digests in that
transaction. Import already commits setup, session, job, journal, audit and
idempotency together. Thus a crash before commit leaves no visible new revision
and startup terminalizes only genuinely active work; a crash after commit sees
the immutable terminal job/result. Recovery never promotes a job to success by
guessing from an object or an intermediate journal state.

Before opening the listener, startup performs SQLite quick-check/migrations and
DB job recovery, journal/import recovery, staging cleanup and an identity-only
managed-content scan. After the listener opens, maintenance immediately runs a
full SHA-256 reconcile followed by expired-record cleanup and reference-safe GC.
`/readyz` checks SQLite and the held roots; it deliberately does not wait for that
background full scrub. Later identity/cleanup/GC cycles use the configured
reconcile interval, while a full SHA-256 scrub repeats every 24 hours.

Remote startup additionally requires a PAM-enabled Linux/cgo build, an existing
configured account equal to the process EUID, and TLS/trusted-proxy policy. PAM
receives the password only for the login exchange; neither password nor raw
session cookie is logged or persisted. Ordinary session state remains memory
only. Remembered sessions persist by SHA-256 token hash in migration 002 and are
bound to user, listener, PAM service, TLS termination mode and library identity.

## Test strategy and evidence catalogue

The following labels expand to exact executable or recorded inspection evidence;
matrix rows never use an unnamed claim. Auth-related unit/integration labels
below passed in the recorded PAM-tagged run; real-account behavior is separate
live evidence in `M-TLS`.

| Label | Exact automated tests / recorded evidence |
|---|---|
| `T-RUN` | existing server/deadline/probe/static/domain tests plus `TestAuthenticationSessionSupportsLocalAndRemoteGuestModes`; `TestAuthenticationEndpointMethodContracts`; `TestRemoteConstructorFailsClosedWithoutAuthenticationDependencies`; `TestRemoteStaticIsPublicAndProtectedAPIUsesBearerWithoutBasic`; `TestRemoteWithoutOptionalBearerHasNoBearerCredentialPath`; `TestRemoteLoginRequiresExactHTTPSOriginBeforePAM`; `TestRemoteLoginCreatesSecureOpaqueSessionAndCapabilitiesUseItsCSRF`; `TestSessionMutationUsesExactOriginAndPerSessionCSRF`; `TestLogoutRequiresSessionOriginAndCSRFThenRevokesCookie`; `TestRememberedLoginSetsPersistentStrictCookie`; `TestRememberedSessionPersistsOnlyHashAcrossRestartAndLogout`; `TestRemoteLoginThrottlingAndConcurrencyAreBounded`; `TestUnknownUsernameStillAuthenticatesOnlyConfiguredPAMAccount`; `TestDuplicateSessionCookiesAreRejectedWithoutAmbiguity`; `TestExplicitInvalidAuthorizationDoesNotFallBackToValidCookie`; `TestSuccessfulReloginRotatesExistingBrowserSession` |
| `T-CFG` | existing defaults/root/TLS tests plus `TestValidateRemoteAuthenticationPolicy`; `TestValidateRequiresAuthenticationAndTransportForLoopbackRemoteMode`; `TestRemoteAuthenticationFailsClosedWithoutPAMBuild`; `TestAuthenticationScopeBindsSecurityRelevantDeploymentState` |
| `T-AUTH` | `TestStoreCreatesOpaqueTokensAndExpiresIdleSession`; `TestStoreCreatesRememberedSessionWithFixedDeadline`; `TestPersistentStoreInvalidatesSessionsWhenUserOrScopeChanges`; `TestPersistentDeleteFailureKeepsRememberedSessionValid`; non-PAM fail-closed tests; real configured-account PAM exercised by `M-TLS` (the credential-driven `TestPAMConfiguredAccount` remains opt-in) |
| `T-DB` | `TestOpenMigratesFullInitialSchema` including migration 002/auth_sessions; `TestPragmasApplyToEveryConnection`; process-lock/root safety; checked migration/backup/recovery/schema suites |
| `T-DOM` | all `internal/domain` tests: stable IDs/JSON/errors, NFC/case-fold names, revision/state transitions and bounded ASCII/UTF-8/BOM G-code validation |
| `T-LIFE` | `TestSetupLifecycleCreateListSearchUpdateAndIdempotency`; `TestListSetupsTenThousandRowsUsesBoundedSQLPagination`; `TestLifecycleSavepointPreventsPartialCreateOnAuditFailure`; `TestCurrentSelectionPersistsAcrossMutationAndRequiresExplicitClear`; `TestRecentAndUIStateAreBoundedStableIDState`; `TestArchiveRestoreDetectsManagedObjectReplacement`; `TestPermanentDeleteConfirmationAndSharedObjectSafety`; `TestDeleteConfirmationExpiresAndArchiveTokensAreInvalidated`; `TestConcurrentMetadataMutationHasSingleWinner` |
| `T-IMPORT` | `TestImportThreeProgramsAndPDFPublishesExactlyOneSetup`; `TestStorageAdoptionReservationBlocksGarbageCollection`; `TestImportRejectsSecondSetupSheetUntilFirstIsExcluded`; `TestImportFailureCanSaveExplicitPartialDraft`; `TestCancelImportInterruptsStreamingAndPublishesNoSetup`; `TestImportCancellationMonitorStopsStorageWork`; `TestArtifactMutationAtomicityRevisionVersionAndSetupSheet`; `TestArtifactStaleRevisionDoesNotLeavePublishedReference`; `TestAddProgramsRejectsWholeBatchWhenOneUploadIsInvalid`; `TestAddProgramsStreamConsumesPartsSequentiallyAndRollsBackSourceFailure`; `TestPrepareArtifactHonorsHeavyWorkLimitBeforeReading`; `TestDeleteLastPrimaryRequiresExplicitLeaveUnassigned`; `TestImportLimitDuplicateNamesExcludeAndRecovery`; `TestExpiredImportCleanupDetachesAndCollectsObjects` |
| `T-OPS` | `TestIdempotencyClaimReplayConflictAndExpiry`; `TestPersistentJobsProgressCancellationAndTerminalStability`; `TestValidationReadyRequiredSheetExternalChangeAndStaleRevision`; `TestDuplicateUsesNewEntityIDsAndIndependentMutations`; `TestReconcileGarbageCollectionAndExpiredCleanup` |
| `T-ATOMIC` | `TestCommittedJobResultsAndUploadClaimsSurviveRestart`; `TestAtomicJobMarshalFailureRollsBackDomainTransaction`; `TestCancelQueuedValidationSignalsWorkerWaitingForHeavySlot` |
| `T-HTTP` | `TestHTTPAddMultipleProgramsUsesConfirmedManifestNamesAtomically`; `TestHTTPAcceptanceCreateUploadValidateCurrentAndRangeETag`; `TestHTTPConcurrentStaleRevisionHasExactlyOneWinner`; `TestParseSingleRange` |
| `T-PATH` | all `internal/storage` tests plus `TestHTTPPathAttackMatrixCannotReadExternalSentinel`; `TestHTTPRejectsSymlinkFIFOAndSocketWithoutBlocking`; `TestHTTPPreviewRejectsSameSizeSameMtimeReplacement` |
| `T-LARGE` | `TestHTTPSparseTenGiBHeadAndRangeUseBoundedMemory`; `TestGCodeValidationUsesBoundedReads`; worker test `cancels before allocating or requesting a block` |
| `T-REC` | `TestReservationAndGarbageCollectionRaceNeverDeletesAdoptedObject`; `TestRestartReconcilesInterruptedImportReplaceAndDuplicate`; `TestProcessKillRollsBackArchiveDeleteAndCurrentSelection`; `TestStartupRecoveryMakesInterruptedWorkTerminal`; `TestFullReconcileRebindsIdenticalColdCopyWithoutClearingAttention` |
| `T-HTML` | `TestSanitizeHTMLRemovesActiveAndNavigatingContent`; `TestSanitizeHTMLHonorsCancellation`; `TestSanitizeHTMLRejectsAnOversizedSingleToken`; `TestSanitizeHTMLStreamsDocumentsLargerThanFormerViewerLimit`; `TestHTMLSetupSheetRejectsOversizedTokenBeforePublication`; `TestHTMLSetupSheetAllowsLargeDocumentWithBoundedTokens`; `TestHTTPHTMLSetupSheetIsSanitizedAndSandboxed`; `TestHTTPHTMLSetupSheetStreamsBeyondFormerViewerLimit`; SetupSheetViewer HEAD/version-bound empty-sandbox tests |
| `T-FE-APP` | all `App.test.tsx` scenarios: session-first boot/login/session expiry/logout plus unavailable/reconnect, library/current, search/filter/cursor/no-match reset, keyboard/state, operations/conflicts/recent/UI-state/offline behavior |
| `T-FE-API` | all `api.test.ts` tests: guest/remote/local sessions, PAM credential POST without web storage, session CSRF, fetch/XHR 401 expiry, one-time CSRF retry, capabilities, errors, paths, jobs/import/recent/UI-state contracts |
| `T-FE-AUTH` | all `LoginView.test.tsx` tests: keyboard submit/no web-storage secret, pending double-submit prevention, generic rejection/password clearing/focus and distinct expired-session announcement |
| `T-FE-IMPORT` | all `ImportWizard.test.tsx` tests: atomic multi-file/primary, one-sheet rules, Unicode backend preflight, conflicts/retry/cancel/partial-draft and progress |
| `T-FE-FOCUS` | all five `Modal.test.tsx` tests: labeling/trap/restore, explicit targets, Escape/backdrop busy rules and escaped focus recovery |
| `T-FE-GC` | all gcodeCore/GCodePreview tests: LF/CRLF sparse offsets, bounded search pages/cancel, adaptive bounded traversal across >1 MiB thinned-index gaps, stable Worker lifecycle, proportional huge-line scroll, wrap paging, highlighting and version errors |
| `A-LOG` | recorded production call-site audit of `httpapi.Server.ServeHTTP`, `safeRouteContext`/`safeMethod`, service job logging, process lifecycle/maintenance logging and every required `appendAudit` path; `TestHealthReadinessAndSafeErrors` verifies that a physical-path error reaches neither response nor captured JSON log |

### Документированные, но ещё не засчитанные manual/target scenarios

| Scenario | Точный проверяемый сценарий | State |
|---|---|---|
| `M-OFFLINE` | отключить все внешние interfaces; non-root systemd start, create/import/preview/validate; browser network log содержит только same-origin; restart remains ready | pending target run |
| `M-UI` | в production browser пройти empty/no-match/loading/backend/storage error; library search/filter/sort/load-more; card/actions/jobs; compare all labels and absence physical values | pending controlled-browser run |
| `M-OPS` | через UI выполнить add/replace/rename/primary/delete/sheet/duplicate/archive/restore/delete-plan; cancel in-flight uploads; verify old revision and navigation | pending controlled-browser run |
| `M-LARGE` | открыть валидный generated/sparse logical 10 GiB G-code; record time/RSS/heap/network/DOM rows, search match near end, cancel/restart search | pending target hardware/browser run |
| `M-VIEWER` | открыть malicious HTML/PDF under browser network/console instrumentation; try scripts/forms/links/actions/fullscreen/close/replace; assert zero unexpected request/code execution | pending controlled-browser run |
| `M-STATE` | save filters/selected setup/program/line/current, restart process/browser, verify same stable IDs; delete selected ID and verify empty state, never name substitution | pending controlled-browser run |
| `M-LOG` | defense-in-depth target drill: run audited operations using newline/control/secret/path-like strings and independently inspect JSON logs/audit for escaped safe IDs/result and absence of secret/content/SQL/path/key | optional target hardening; code audit complete |
| `M-TLS` | current direct TLS 1.3 host: guest session 200/capabilities 401; PAM login 200, authenticated session/capabilities 200, logout 204; remembered login persisted only a token hash and survived graceful service restart, then logout 204 removed it; health/ready 200; TLS 1.2 rejected; no Basic or configured Bearer | V — live 2026-08-20 |
| `M-PROXY` | repeat auth/session/Host/Origin/CSRF flow through a locked-down trusted TLS reverse proxy if that alternative deployment mode is selected | pending conditional deployment run |
| `M-SLOW` | run progressing and stalled large request/response clients against the deployed listener; verify sliding deadline keeps progress and terminates stalls | pending live network run |
| `M-KEYBOARD` | mouse disabled: find/open setup, choose program, preview/search/line jump, sheet viewer, validate, choose current; inspect focus order/trap/return and text/ARIA | pending controlled-browser run |
| `M-POWER` | repeatedly power-cut/SIGKILL import, replace and duplicate on the target filesystem; restart and assert no partial revision plus stable job/journal/sentinel state; archive/delete/current subprocess SIGKILL is already in `T-REC` | pending target fault run |
| `X-HW` | measure cold start, idle RSS/CPU, stream delta RSS, 10k latency and first viewport against NFR budgets on the agreed LinuxCNC workstation | external target pending |
| `X-ARM64` | install/run arm64 binary on supported arm64 host and repeat smoke/security suite | external host pending |

## Матрица всех P0 ID

Механическая сверка 2026-08-20 нашла 221 уникальный P0 ID в normative source и
те же 221 ID ниже без пропусков/лишних строк; таблица AC содержит ровно 20
уникальных строк `AC-01`–`AC-20`. Это проверяет полноту идентификаторов, но не
повышает `I`/`P`/`X` до `V`.

### Deployment и configuration (14)

| ID | Control / exact evidence | Status |
|---|---|---|
| `FR-DEP-001` | Single Go + embedded SQLite architecture: `T-DB`; cgo/PAM production build and live binary | V |
| `FR-DEP-002` | production-tag embedded Vite FS: `T-RUN`; Vite+cgo/PAM build and live SPA | V |
| `FR-DEP-003` | no telemetry/cloud endpoint or dependency; exact network check `M-OFFLINE` | I — manual pending |
| `FR-DEP-004` | startup refuses EUID 0; non-root unit/run in `M-OFFLINE` | I — target run pending |
| `FR-DEP-005` | amd64/arm64 target baseline D-001; current cgo/PAM build plus `X-ARM64` | P — PAM-linked build/arm64 runtime pending |
| `FR-DEP-006` | DB + both roots readiness: `T-RUN` (`TestHealthReadinessAndSafeErrors`) | V |
| `FR-CFG-001` | startup env-only roots: `T-CFG`; no mutation route in `T-RUN` | V |
| `FR-CFG-002` | capabilities/route/UI omit path controls: `T-RUN`, `T-FE-API` | V |
| `FR-CFG-003` | config loaded once before held root FDs; restart scenario `M-OFFLINE` | I — manual pending |
| `FR-CFG-004` | existence/type/access/canonical roots: `T-CFG`, storage root tests | V |
| `FR-CFG-005` | fail startup/no wider fallback/safe diagnostic: `T-CFG` | V |
| `FR-CFG-006` | disjoint roots and no state content route: `T-CFG`, `T-PATH` | V |
| `FR-CFG-007` | safe config/domain/HTTP errors: `T-CFG`, `T-FE-API`, `T-PATH` | V |
| `FR-CFG-008` | persisted marker and DB binding: `TestEnsureLibraryIsStable`, `TestRootsPersistOpaqueLibraryIDAndRemainReady` | V |

### Setup, state, current и validation (29)

| ID | Control / exact evidence | Status |
|---|---|---|
| `FR-SET-001` | random path/name-independent setup ID: `T-DOM`, `T-LIFE` | V |
| `FR-SET-002` | DTO/schema name/description/status/revision/timestamps/source: `T-DOM`, `T-LIFE` | V |
| `FR-SET-003` | empty draft + ready requires program: `T-LIFE`, `T-OPS` | V |
| `FR-SET-004` | multi-program stable artifact IDs/order: `T-IMPORT`, `T-HTTP` | V |
| `FR-SET-005` | one primary/auto-primary DB+service invariants: `T-DB`, `T-IMPORT` | V |
| `FR-SET-006` | setup-level single sheet FK/role: `T-DB`, `T-IMPORT` | V |
| `FR-SET-007` | duplicate setup display names allowed/context shown: `T-LIFE`; dialog walkthrough `M-UI` | P — UI manual pending |
| `FR-SET-008` | NFC/case-fold collision key per setup: `T-DOM`, `T-IMPORT` | V |
| `FR-SET-009` | one atomic revision increment, including terminal upload job/result: `T-LIFE`, `T-IMPORT`, `T-HTTP`, `T-ATOMIC` | V |
| `FR-STATE-001` | create/import/duplicate start draft: `T-LIFE`, `T-IMPORT`, `T-OPS` | V |
| `FR-STATE-002` | revision-bound validation -> ready atomically with terminal job: `T-OPS`, `T-HTTP`, `T-ATOMIC` | V |
| `FR-STATE-003` | composition/required metadata ready -> draft: `T-DOM`, `T-IMPORT` | V |
| `FR-STATE-004` | external identity damage -> attention: `T-PATH`, `T-OPS` | V |
| `FR-STATE-005` | repaired attention returns draft then revalidate: `T-DOM`, `T-OPS` | V |
| `FR-STATE-006` | archive preserves ID/content/prior state: `T-LIFE` | V |
| `FR-STATE-007` | textual not-ready reasons in DTO/cards; complete visual check `M-UI` | I — manual pending |
| `FR-CURRENT-001` | singleton PK/library current row: `T-DB`, `T-LIFE` | V |
| `FR-CURRENT-002` | ready revision + explicit confirmation: `T-LIFE`, `T-FE-APP` | V |
| `FR-CURRENT-003` | pinned panel name/revision/selectedAt: `T-FE-APP`; visual `M-UI` | P — visual pending |
| `FR-CURRENT-004` | only DB pointer/audit, no execute/copy route: `T-LIFE`, `T-RUN`, `T-FE-APP` | V |
| `FR-CURRENT-005` | pointer retained after mutation and blocking state: `T-LIFE`; visual `M-UI` | P — visual pending |
| `FR-CURRENT-006` | explicit replace/clear + audit: `T-LIFE` | V |
| `FR-VAL-001` | setup+revision binding and atomic run/setup/job result across restart: `T-OPS`, `T-ATOMIC` | V |
| `FR-VAL-002` | program count/access/type/name/version rules: `T-OPS`, `T-DOM` | V |
| `FR-VAL-003` | required-sheet config/access: `TestValidationReadyRequiredSheetExternalChangeAndStaleRevision` | V |
| `FR-VAL-004` | text/encoding validation without LinuxCNC execution: `T-DOM`, `T-RUN` | V |
| `FR-VAL-005` | persisted artifact-bound issues/action fields: `T-OPS`; UI rendering `M-UI` | P — UI manual pending |
| `FR-VAL-006` | warning/blocking semantics; blocking run may fail while its completed job succeeds: `T-OPS`, `T-ATOMIC` | V |
| `FR-VAL-007` | ready disclaimer rendered in detail; exact operator check `M-UI` | I — manual pending |

### Library, detail и accessibility (17)

| ID | Control / exact evidence | Status |
|---|---|---|
| `FR-UX-001` | setup library/current, no tree/browser: `T-RUN`, `T-FE-APP` | V |
| `FR-UX-002` | summary name/status/count/sheet/revision/updated: `T-LIFE`; visual `M-UI` | P — visual pending |
| `FR-UX-003` | pinned current one-action open: `T-FE-APP`; keyboard `M-KEYBOARD` | P — keyboard pending |
| `FR-UX-004` | query/status/sheet/current/sort controls before cursor: `T-LIFE`, `T-FE-APP` | V |
| `FR-UX-005` | active excludes archived, explicit archive filter: `T-LIFE`, `T-FE-APP` | V |
| `FR-UX-006` | opaque cursor and bounded 10k SQL: `T-LIFE`, `T-FE-APP` | V |
| `FR-UX-007` | loading/empty/no-match/backend/storage states; full matrix `M-UI` | I — manual pending |
| `FR-UX-008` | visible create/import/current actions; walkthrough `M-UI` | I — manual pending |
| `FR-DETAIL-001` | detail DTO and card fields; `T-FE-APP`, then `M-UI` | P — complete visual pending |
| `FR-DETAIL-002` | setup-level validate/current/duplicate/archive buttons: `M-UI` | I — manual pending |
| `FR-DETAIL-003` | contextual open/replace/rename/primary/delete controls: `M-OPS` | I — manual pending |
| `FR-DETAIL-004` | DTO JSON hides internal data: `T-DOM`, `T-PATH`; visual `M-UI` | V |
| `FR-DETAIL-005` | selected preview retained across card navigation: `T-FE-APP`; line restore `M-STATE` | P — restart/manual pending |
| `FR-DETAIL-006` | job progress/bytes/cancel controls; backend `T-OPS`, UI `M-OPS` | P — UI manual pending |
| `FR-A11Y-001` | native controls/modal baseline; full mouse-free flow `M-KEYBOARD` | I — manual pending |
| `FR-A11Y-002` | textual labels/status/reasons; `T-FE-FOCUS`; audit `M-KEYBOARD` | I — manual pending |
| `FR-A11Y-003` | trap and initiator return: `T-FE-FOCUS` | V |

### Common operations и create/edit (13)

| ID | Control / exact evidence | Status |
|---|---|---|
| `FR-OP-001` | confirmation dialogs name setup/action/artifacts; `M-OPS` | I — manual pending |
| `FR-OP-002` | API/service expected revision and object version: `T-IMPORT`, `T-HTTP` | V |
| `FR-OP-003` | conflict makes no mutation/staging reference: `T-IMPORT`, `T-HTTP` | V |
| `FR-OP-004` | key/hash replay plus atomic upload run claim/digest across restart: `T-OPS`, `T-LIFE`, `T-ATOMIC` | V |
| `FR-OP-005` | publish-before-link plus domain/terminal-job transaction and forced rollback: `T-IMPORT`, `T-LIFE`, `T-ATOMIC` | V |
| `FR-OP-006` | persistent job ID/state/progress/bytes/cancel: `T-OPS`, `T-ATOMIC`; UI `M-OPS` | P — UI manual pending |
| `FR-OP-007` | cancel frees temp/preserves revision and queued worker exits: `T-IMPORT`, `T-ATOMIC` | V |
| `FR-OP-008` | UI reload preserves selected setup/program/navigation: `T-FE-APP`; full `M-OPS` | P — full UI pending |
| `FR-CREATE-001` | required-name empty setup creation: `T-LIFE`, `T-FE-APP` | V |
| `FR-CREATE-002` | immediate stable-ID draft: `T-LIFE`, `T-FE-APP` | V |
| `FR-CREATE-003` | metadata patch retains ID/artifacts: `T-LIFE` | V |
| `FR-CREATE-004` | unsafe/long names rejected and conflict input retained: `T-DOM`, `T-FE-APP` | V |
| `FR-CREATE-005` | duplicate display names allowed with dialog hint: `T-LIFE`; visual `M-UI` | P — visual pending |

### Import, programs, duplicate и delete (32)

| ID | Control / exact evidence | Status |
|---|---|---|
| `FR-IMPORT-001` | name/description + multi-file wizard/session: `T-FE-IMPORT`, `T-IMPORT` | V |
| `FR-IMPORT-002` | extension role suggestion, PDF/HTML option and confirmation: `T-FE-IMPORT`; full UI `M-OPS` | P — role walkthrough pending |
| `FR-IMPORT-003` | multiple programs/max one sheet: `T-IMPORT`, `T-FE-IMPORT` | V |
| `FR-IMPORT-004` | basename/size/role/exclude controls; service exclude `T-IMPORT`, UI `M-OPS` | P — UI manual pending |
| `FR-IMPORT-005` | sequential streaming staging, no whole-file buffer: `T-IMPORT`, storage stage tests | V |
| `FR-IMPORT-006` | no setup row before atomic commit; result remains draft: `TestImportThreeProgramsAndPDFPublishesExactlyOneSetup` | V |
| `FR-IMPORT-007` | retryable failed artifact and explicit partial draft: `TestImportFailureCanSaveExplicitPartialDraft` | V |
| `FR-IMPORT-008` | cancel interrupts reader, removes session references/temp, no setup: `T-IMPORT` | V |
| `FR-IMPORT-009` | confirmed manifest basename ignores fake browser path: `TestHTTPAddMultipleProgramsUsesConfirmedManifestNamesAtomically`, `T-DOM` | V |
| `FR-IMPORT-010` | collisions block silent overwrite; rename/exclude/keep-one UI: `T-IMPORT`, then `M-OPS` | P — all UI choices pending |
| `FR-IMPORT-011` | pre/during free-space/limits and cleanup: storage stage tests, `T-IMPORT` | V |
| `FR-IMPORT-012` | no constant when limit 0, int64 streaming/sparse: `T-LARGE`, `T-IMPORT` | V |
| `FR-IMPORT-013` | start/upload/commit replay same IDs/setup: `TestImportThreeProgramsAndPDFPublishesExactlyOneSetup` | V |
| `FR-PROG-001` | atomic confirmed multi-program add: `TestHTTPAddMultipleProgramsUsesConfirmedManifestNamesAtomically`, `T-IMPORT` | V |
| `FR-PROG-002` | replacement retains ID/name/primary across terminal job and restart: `T-IMPORT`, `T-ATOMIC` | V |
| `FR-PROG-003` | old link survives failure; committed replace/job/revision are atomic: `T-IMPORT`, `T-ATOMIC` | V |
| `FR-PROG-004` | rename keeps artifact ID/object and advances revision: `T-IMPORT`; UI `M-OPS` | P — UI manual pending |
| `FR-PROG-005` | explicit last-program confirmation -> draft: `TestDeleteLastPrimaryRequiresExplicitLeaveUnassigned` | V |
| `FR-PROG-006` | reject ambiguous primary delete; explicit replacement/unassigned: `T-IMPORT`, `T-HTTP` | V |
| `FR-PROG-008` | extension/content treated as text hint; no execute route: `T-DOM`, `T-RUN` | V |
| `FR-DUP-001` | new setup and artifact IDs, same composition: `TestDuplicateUsesNewEntityIDsAndIndependentMutations` | V |
| `FR-DUP-002` | required copy name service/dialog; backend `T-OPS`, UI `M-OPS` | P — UI manual pending |
| `FR-DUP-003` | independent draft regardless source status: `TestDuplicateUsesNewEntityIDsAndIndependentMutations` | V |
| `FR-DUP-004` | transaction/job cancel/error leaves no partial setup; committed result survives restart: `T-OPS`, `T-REC`, `T-ATOMIC` | V |
| `FR-DUP-005` | shared immutable object, independent future mutation/GC: `T-OPS`, `T-LIFE` | V |
| `FR-DEL-001` | archive default and restore operation: `T-LIFE`; UI `M-OPS` | P — UI manual pending |
| `FR-DEL-002` | current setup archive conflict until explicit clear/change: `T-LIFE` | V |
| `FR-DEL-003` | prior status/attention plus atomic terminal restore job/audit across restart: `T-LIFE`, `T-ATOMIC` | V |
| `FR-DEL-004` | only archived + exact-name separate permanent confirmation: `T-LIFE` | V |
| `FR-DEL-005` | delete plan program/sheet/unique-byte summary: `TestPermanentDeleteConfirmationAndSharedObjectSafety` | V |
| `FR-DEL-006` | short-lived setup/revision/name-bound token: `T-LIFE` | V |
| `FR-DEL-007` | shared object unlinked only after last ref/terminal operation: `T-LIFE`, `T-REC` | V |

### G-code preview/search (25)

| ID | Control / exact evidence | Status |
|---|---|---|
| `FR-GC-001` | role + configured case-insensitive extension: `T-DOM`, `T-CFG` | V |
| `FR-GC-002` | ASCII/UTF-8/BOM streaming decode: `T-DOM` | V |
| `FR-GC-003` | stable unsupported-encoding result/no replacement text: `T-DOM`; visual `M-LARGE` | P — visual pending |
| `FR-GC-004` | NUL/binary -> coded preview unavailable, no panic: `T-DOM`; visual `M-LARGE` | P — visual pending |
| `FR-GC-010` | HEAD, streamed 206/Accept-Ranges/int64/cancel: `T-HTTP`, `T-LARGE` | V |
| `FR-GC-011` | constant-range allocation vs 10 GiB sparse: `TestHTTPSparseTenGiBHeadAndRangeUseBoundedMemory` | V |
| `FR-GC-012` | first block fetched independently before Worker full index; timing `M-LARGE` | I — target timing pending |
| `FR-GC-013` | visible+overscan absolute rows only; DOM count `M-LARGE` | I — browser measurement pending |
| `FR-GC-014` | Worker performs full decode/index/search; thinned-index navigation advances in cancellable bounded async chunks: `T-FE-GC` | V |
| `FR-GC-015` | every block/Worker request If-Match; changed object rejected: `T-HTTP`, `T-PATH` | V |
| `FR-GC-016` | in-memory sparse index scoped to active library page + artifact/version and discarded on change; `M-STATE` | I — browser state scenario pending |
| `FR-GC-017` | rendered line capped at 4096 with explicit marker; `M-LARGE` | I — manual long-line check pending |
| `FR-GC-020` | header setup/name/size/revision/version/index/primary; visual `M-LARGE` | I — manual pending |
| `FR-GC-021` | LF/CRLF sparse stable line numbers: `T-FE-GC` | V |
| `FR-GC-022` | comment/G-M/axis/feed-spindle/parameter/number tokenizer: GCodePreview highlight test | V |
| `FR-GC-023` | tokenizer invoked only for virtual `visible` rows; DOM verification `M-LARGE` | I — browser measurement pending |
| `FR-GC-024` | nowrap default, horizontal scroll and wrap checkbox: `M-LARGE` | I — manual pending |
| `FR-GC-025` | bounded line-number jump plus adaptive Range traversal across thinned-index gaps: `T-FE-GC`; keyboard flow `M-KEYBOARD` | P — keyboard manual pending |
| `FR-GC-026` | explicit empty state: GCodePreview test `shows an explicit empty state and setup context` | V |
| `FR-GC-027` | version error banner + attention/reload: `T-PATH`; UI `M-LARGE` | P — browser flow pending |
| `FR-GC-030` | Worker literal loop reads every Range block: `T-FE-GC`; near-end 10 GiB `M-LARGE` | P — large target pending |
| `FR-GC-031` | previous/next and case-sensitive controls: `M-KEYBOARD` | I — manual pending |
| `FR-GC-032` | Worker progress and running match count output: `T-FE-GC`; visual `M-LARGE` | P — visual pending |
| `FR-GC-033` | new query cancels previous AbortController and explicit cancel: `T-FE-GC` | V |
| `FR-GC-034` | compact bounded sparse/search pages with navigation beyond retained windows: `T-FE-GC`; browser `M-LARGE` | P — large browser pending |

### Setup Sheet lifecycle/viewer/security (21)

| ID | Control / exact evidence | Status |
|---|---|---|
| `FR-SS-001` | zero-or-one setup sheet schema/service invariant: `T-DB`, `T-IMPORT` | V |
| `FR-SS-002` | add/open/replace/delete service and visible controls: `T-IMPORT`; UI `M-OPS` | P — UI manual pending |
| `FR-SS-003` | PDF signature and prevalidated streaming sanitized standalone HTML: `T-IMPORT`, `T-HTML` | V |
| `FR-SS-004` | streaming stage + content/signature media detection: `T-IMPORT`, storage stage tests | V |
| `FR-SS-005` | SHA-256/size immutable atomic publish: storage publish tests, `T-IMPORT` | V |
| `FR-SS-006` | original basename only display; DTO/path suite: `T-DOM`, `T-PATH` | V |
| `FR-SS-007` | failed/stale replacement preserves previous object/revision: `T-IMPORT` | V |
| `FR-SS-008` | add/replace/delete changes ready to draft: `TestArtifactMutationAtomicityRevisionVersionAndSetupSheet` | V |
| `FR-SS-009` | duplicate shares object but setup-scoped logical artifact: `T-OPS`, `T-LIFE` | V |
| `FR-SS-020` | sheet indicator in summary/detail/G-code header action; `M-UI` | I — visual pending |
| `FR-SS-021` | modal overlays retained detail state: `T-FE-APP`, SetupSheetViewer test; full `M-VIEWER` | P — browser pending |
| `FR-SS-022` | viewer name/format/scale/fullscreen/close controls: `M-VIEWER` | I — manual pending |
| `FR-SS-023` | PDF.js scroll/page count/jump/zoom controls: `M-VIEWER` | I — browser pending |
| `FR-SS-024` | inline corrupt/password/version error, close/replace: code path; `M-VIEWER` | I — browser pending |
| `SEC-SS-001` | iframe/canvas only, no `dangerouslySetInnerHTML`: SetupSheetViewer test, `T-HTML` | V |
| `SEC-SS-002` | empty iframe sandbox, no same-origin/scripts/forms/etc: `T-HTML` | V |
| `SEC-SS-003` | allowlist strips active elements/attrs/URLs/meta refresh: `T-HTML` | V |
| `SEC-SS-004` | exact document CSP sandbox/default/connect/frame-ancestors: `TestHTTPHTMLSetupSheetIsSanitizedAndSandboxed` | V |
| `SEC-SS-005` | nosniff/no cookie, originless sandbox/no credentials: `T-HTML`; browser `M-VIEWER` | P — runtime observation pending |
| `SEC-SS-006` | local PDF.js canvas, eval/XFA/annotation action layer disabled: `M-VIEWER` | I — malicious-PDF run pending |
| `SEC-SS-007` | no annotation/link layer; external-link attempt `M-VIEWER` | I — malicious-PDF run pending |

### Search, history и UI state (9)

| ID | Control / exact evidence | Status |
|---|---|---|
| `FR-SEARCH-001` | case-insensitive setup name/description/program name SQL search: `T-LIFE` | V |
| `FR-SEARCH-002` | status/sheet/current filters in service and UI: `T-LIFE`, `T-FE-APP` | V |
| `FR-SEARCH-003` | query/filter predicates included before opaque cursor LIMIT: `T-LIFE`, `T-FE-APP` | V |
| `FR-HIS-001` | opening card/program upserts recent by stable ID: `T-LIFE`; UI `M-STATE` | P — browser flow pending |
| `FR-HIS-002` | setup-ID dedupe, last-opened sort and configured bound: `TestRecentAndUIStateAreBoundedStableIDState` | V |
| `FR-HIS-003` | last artifact/line/opened timestamp persistence: `TestRecentAndUIStateAreBoundedStableIDState` | V |
| `FR-HIS-004` | open/remove/clear recent without setup mutation: backend `T-LIFE`, UI `M-STATE` | P — UI manual pending |
| `FR-HIS-005` | library/client-keyed screen/setup/filters/program state in SQLite: `T-LIFE` | V |
| `FR-HIS-006` | stable-ID restore/no same-name substitution: backend `T-LIFE`; process/browser restart `M-STATE` | P — restart pending |

### Storage path/race security (18)

| ID | Control / exact evidence | Status |
|---|---|---|
| `SEC-PATH-001` | setup/artifact membership join rejects foreign IDs: `TestHTTPPathAttackMatrixCannotReadExternalSentinel` | V |
| `SEC-PATH-002` | absolute/UNC/volume/`..`/NUL/separators rejected: `T-DOM`, `T-PATH` | V |
| `SEC-PATH-003` | single framework decode only; encoded/double-encoded attacks: HTTP path matrix | V |
| `SEC-PATH-004` | domain JSON/internal fields and responses hide key/layout/path: `T-DOM`, `T-PATH` | V |
| `SEC-PATH-005` | React text/token nodes do not interpret names/content as HTML: GCodePreview injection test; `M-UI` | P — complete UI injection check pending |
| `SEC-PATH-010` | held library/state directory FDs and relative operations: storage root/store tests | V |
| `SEC-PATH-011` | real openat2 beneath/no-magic/no-symlink and fallback suite: `TestOpenLibraryRejectsTraversalAndSymlinkSentinel` | V |
| `SEC-PATH-012` | exclusive creation under held handle with generated/validated component: storage stage/publish tests | V |
| `SEC-PATH-013` | post-open fstat regular-only: `T-PATH` | V |
| `SEC-PATH-014` | FIFO/socket/special nonblocking stable error + attention: `TestHTTPRejectsSymlinkFIFOAndSocketWithoutBlocking` | V |
| `SEC-PATH-015` | no-follow read/stage/hash/duplicate/delete and sentinel unchanged: `T-PATH` | V |
| `SEC-PATH-016` | exclusive no-follow temp and non-executable configured mode: storage stage tests, `T-CFG` | V |
| `SEC-PATH-017` | no shell execution path; adversarial names processed as data: `T-DOM`, `T-PATH`; operator check `M-LOG` | P — inspection/log run pending |
| `SEC-RACE-001` | opaque version device/inode/size/mtime/ctime: storage object tests, `T-PATH` | V |
| `SEC-RACE-002` | before/after stream identity verification rejects change: storage substitution tests, `T-PATH` | V |
| `SEC-RACE-003` | replace requires expected artifact version and stale leaves old link: `T-IMPORT` | V |
| `SEC-RACE-004` | concurrent revision exactly one winner: `TestConcurrentMetadataMutationHasSingleWinner`, HTTP stale test | V |
| `SEC-RACE-005` | same size/mtime but new inode cannot inherit identity: `TestHTTPPreviewRejectsSameSizeSameMtimeReplacement` | V |

### SQLite rules (10)

| ID | Control / exact evidence | Status |
|---|---|---|
| `DATA-001` | FK/WAL/busy timeout/FULL on every pool connection: `TestPragmasApplyToEveryConnection` | V |
| `DATA-002` | embedded numbered checksums including migration 002, transactional before ready: `T-DB` | V |
| `DATA-003` | consistent exclusive SQLite backup primitive tested: `TestOnlineBackupIsConsistentAndNeverOverwrites`; irreversible-upgrade drill `M-POWER` | P — future migration drill pending |
| `DATA-004` | unmanaged/newer/checksum/failing migration never overwritten/downgraded: `T-DB` | V |
| `DATA-005` | startup quick-check and no auto-delete: DB open/recovery tests; corrupt restore runbook scenario `M-POWER` | P — operator drill pending |
| `DATA-006` | exclusive process lock, release, symlink/special rejection: `T-DB` | V |
| `DATA-007` | reservation checkpoints plus domain/terminal-job/outer-upload-claim commit: `T-IMPORT`, `T-REC`, `T-ATOMIC`; D-016/D-020 | V |
| `DATA-008` | interrupted states reconcile; committed async outcomes survive reopen unchanged: `T-DB`, `T-REC`, `T-ATOMIC` | V |
| `DATA-009` | all public/reference relationships stable IDs not display names: `T-DOM`, `T-LIFE` | V |
| `DATA-010` | DB trigger/service reservation prevent ref/active-journal GC: `T-DB`, `T-REC` | V |

### API contract (7)

| ID | Control / exact evidence | Status |
|---|---|---|
| `API-001` | stable code/message/requestId/details/retryable envelope: `T-DOM`, `T-FE-API`, `T-PATH` | V |
| `API-002` | safe error mapping omits internal error/SQL/stack/key/path: `T-CFG`, `T-PATH`, `T-FE-API` | V |
| `API-003` | mutation parsing requires idempotency and applicable revision/version: `T-LIFE`, `T-IMPORT`, `T-HTTP` | V |
| `API-004` | HEAD/ETag/If-Match/Range plus 404/409/412/416 contracts: `T-HTTP`, `T-PATH`, `T-LARGE` | V |
| `API-005` | signed/opaque query+sort+filter cursor and stale/mismatch rejection: `T-LIFE`, `T-FE-APP` | V |
| `API-006` | cancellation cannot mask committed success; queued validation worker exits: `T-OPS`, `T-ATOMIC` | V |
| `API-007` | all terminal states immutable; success/error/cancel results survive restart: `T-DOM`, `T-OPS`, `T-ATOMIC` | V |

### Network/application security (8)

| ID | Control / exact evidence | Status |
|---|---|---|
| `SEC-NET-001` | loopback default and implicit-local session config: `T-CFG`, `T-RUN` | V |
| `SEC-NET-002` | remote requires explicit flag + matching non-root PAM account + PAM build + TLS/proxy; optional Bearer ≥32: `T-CFG`, `T-AUTH`, `T-RUN`, direct deployment `M-TLS` | V |
| `SEC-NET-003` | exact HTTPS Host/Origin for session login/logout/mutation, per-session CSRF, no wildcard CORS: `T-RUN`, `T-FE-API`, `M-TLS` | V |
| `SEC-NET-004` | opaque `__Host-` cookie, memory-only ordinary sessions, hash-only remembered SQLite rows, per-session CSRF and no Basic: `T-AUTH`, `T-RUN`, `T-FE-AUTH`, `M-TLS` | V |
| `SEC-NET-005` | separate header/connection and sliding request-read/response-write deadlines + heavy semaphore: `T-RUN`, `TestPrepareArtifactHonorsHeavyWorkLimitBeforeReading`; live slow-client `M-SLOW` | P — live network test pending |
| `SEC-NET-006` | app CSP/COOP/CORP/permissions/frame policy headers: `T-RUN`; browser `M-VIEWER` | P — browser observation pending |
| `SEC-NET-007` | route inventory rejects `/fs`/unknown arbitrary reads: `TestStaticSPAAndNoFilesystemAPI`, `T-PATH` | V |
| `SEC-NET-008` | content responses private/no-store and no shared-cache eligibility: HTTP content tests; proxy observation `M-PROXY` | P — proxy check pending |

### Performance/reliability/logging NFR (18)

| ID | Control / exact evidence | Status |
|---|---|---|
| `NFR-PERF-001` | cold start budget ≤2 s: `X-HW` | X — not measured on target |
| `NFR-PERF-002` | idle RSS ≤80 MiB and CPU ≈1%: `X-HW` | X — not measured on target |
| `NFR-PERF-003` | one streaming operation delta memory ≤16 MiB: bounded-read tests; numeric `X-HW` | P — 16 MiB target measure pending |
| `NFR-PERF-004` | 10k single bounded SQL query: `TestListSetupsTenThousandRowsUsesBoundedSQLPagination`; ≤500 ms `X-HW` | P — target SLA pending |
| `NFR-PERF-005` | first independent Range block architecture; ≤1 s SSD `M-LARGE`/`X-HW` | X — target timing pending |
| `NFR-PERF-006` | Worker/heavy-job semaphore architecture; responsiveness `M-LARGE` | I — browser measure pending |
| `NFR-PERF-007` | int64/sparse 10 GiB HEAD/end Range, no app constant: `T-LARGE` | V |
| `NFR-PERF-008` | 8×1 MiB cache + sparse Worker index/virtual rows: `T-FE-GC`; heap `M-LARGE` | P — browser heap pending |
| `NFR-REL-001` | interrupted replace preserves old revision; committed replace/job is one transaction: `T-IMPORT`, `T-ATOMIC` | V |
| `NFR-REL-002` | BeginShutdown rejects/cancels tracked operations (`TestBeginShutdownCancelsTrackedOperation`), closes jobs/HTTP/SQLite; signal drill `M-POWER` | P — process signal drill pending |
| `NFR-REL-003` | pre-commit recovery, actual lifecycle SIGKILL and post-commit async reopen: `T-REC`, `T-ATOMIC`; target power cut `M-POWER` | P — target power-loss drill pending |
| `NFR-REL-004` | preview error is local component state; retained card/selection check `M-LARGE` | I — browser failure run pending |
| `NFR-REL-005` | external identity change -> attention/no silent ID reuse: `T-PATH`, `T-OPS` | V |
| `NFR-REL-006` | current row/revision durable by stable IDs: `T-LIFE`; full restart `M-STATE` | P — process/browser restart pending |
| `NFR-LOG-001` | structured HTTP/job/lifecycle field schema and captured JSON safety: `A-LOG` | V — automated check + code audit |
| `NFR-LOG-002` | create/import/validate/current/replace/duplicate/archive/restore/delete call-site matrix and sampled history: `A-LOG`, `T-LIFE`, `T-OPS` | V — code audit complete |
| `NFR-LOG-003` | call-site allowlist omits body/token/SQL/path/storage key; physical-path capture test: `A-LOG` | V — automated check + code audit |
| `NFR-LOG-004` | route/method/entity allowlists plus structured slog escaping and bounded IDs: `A-LOG`, `T-RUN` | V — code audit complete |

## AC-01–AC-20

| AC | Exact automated evidence / remaining scenario | Status |
|---|---|---|
| `AC-01` | `TestStaticSPAAndNoFilesystemAPI`; App test `loads the pinned current area and setup library without exposing a file browser` | V |
| `AC-02` | `TestSetupLifecycleCreateListSearchUpdateAndIdempotency`; App test `creates a draft from a focus-managed dialog` | V |
| `AC-03` | `TestImportThreeProgramsAndPDFPublishesExactlyOneSetup`; wizard atomic multi-file test | V |
| `AC-04` | `TestCancelImportInterruptsStreamingAndPublishesNoSetup`, cancellation monitor/staging cleanup; several-GiB live cancel in `M-LARGE` | P — large live transfer pending |
| `AC-05` | revision-bound service/HTTP validation plus atomic validation-run/setup/job restart evidence in `T-ATOMIC` | V |
| `AC-06` | current persistence/audit service test, HTTP workflow, explicit no-execution App test and absence of execute route | V |
| `AC-07` | schema one setup-level sheet, multi-program import, preview sheet action; full every-program UI route in `M-OPS` | P — browser walkthrough pending |
| `AC-08` | failed/truncated replacement preserves old object; committed replace ID/version/revision/job survives restart in `T-ATOMIC`; live disconnect `M-OPS` | P — live disconnect pending |
| `AC-09` | independent IDs/refs plus atomic duplicate result across reopen: `T-OPS`, `T-ATOMIC` | V |
| `AC-10` | archive/content-change rules plus atomic restore job/audit across reopen: `T-LIFE`, `T-ATOMIC` | V |
| `AC-11` | HTTP external-sentinel matrix + storage traversal/substitution tests; sentinel readback unchanged | V |
| `AC-12` | sparse 10 GiB bounded backend test + bounded Worker/cache architecture; target DOM/RSS/interactive timing `M-LARGE` | P — target browser/hardware pending |
| `AC-13` | Worker block-boundary literal/progress/cancel/compact tests; match near 10 GiB end `M-LARGE` | P — large near-end run pending |
| `AC-14` | `TestHTTPPreviewRejectsSameSizeSameMtimeReplacement` proves no mixed block and attention; visible refresh flow `M-LARGE` | P — UI walkthrough pending |
| `AC-15` | same test replaces same-size/same-mtime object with new inode and preserves artifact identity while marking attention | V |
| `AC-16` | sanitizer/header end-to-end + originless empty-sandbox component test; actual browser network/code observation `M-VIEWER` | P — controlled browser pending |
| `AC-17` | `TestHTTPRejectsSymlinkFIFOAndSocketWithoutBlocking` checks stable error, ≤2 s response, attention and sentinel | V |
| `AC-18` | concurrent HTTP stale test, service single-winner test, App test preserving input/retrying reloaded revision | V |
| `AC-19` | `TestRestartReconcilesInterruptedImportReplaceAndDuplicate` covers pre-commit durable states; `TestCommittedJobResultsAndUploadClaimsSurviveRestart` covers committed add/replace/sheet, validation, duplicate and restore with stable job/idempotency results; marshal-failure rollback and actual archive/delete/current subprocess kill also pass; `M-POWER` remains | P — target import/replace/duplicate power-loss drill pending |
| `AC-20` | focus-trap automated tests and keyboard-open App test; complete no-mouse flow `M-KEYBOARD` | P — controlled browser pending |

## External/target deviations (не скрыты как pass)

| Deviation | Development evidence | Необходимое закрытие |
|---|---|---|
| arm64 PAM build/runtime | прежний non-PAM cross-build не применим к текущей auth-версии | PAM-linked build and `X-ARM64` on an actual supported host |
| numeric machine budgets | bounded algorithms, sparse 10 GiB backend, 10k bounded SQL | `X-HW` on agreed LinuxCNC workstation |
| browser visual/accessibility | Vitest/Testing Library unit/component baseline | `M-UI`, `M-LARGE`, `M-VIEWER`, `M-KEYBOARD` in controlled production browser |
| PDF/HTML active-content observation | backend sanitizer/CSP and no-action canvas architecture | instrumented malicious documents with network/console observation |
| trusted-proxy/certificate trust | PAM/config/session tests and direct TLS `M-TLS` passed; current host certificate is self-signed | `M-PROXY` only if selected, plus managed client trust for the production certificate |
| hard process/power loss | durable intermediate fixtures, atomic post-commit reopen, and actual SIGKILL rollback for archive/delete/current | repeated import/replace/duplicate power-loss `M-POWER` on target |
| operator deployment/restore | catalog cold generation, four archive checksums and full extract/diff `RESTORE_CHECK_OK` | повторить перед следующей несовместимой migration |

## Финальные quality gates

После PAM/browser-session integration записано следующее evidence:

- `go test ./... -count=1` — passed;
- `go test -race ./... -count=1 -timeout=10m` — passed
  (`internal/service` 53.682 s);
- `CGO_ENABLED=1 go test -tags pam ./...` — passed;
- `CGO_ENABLED=1 go test -race -tags pam ./...` — passed;
- `CGO_ENABLED=1 go vet -tags pam ./...` — passed;
- frontend lint/typecheck — passed; Vitest — 15 files/103 tests passed;
  TypeScript/Vite production build — passed;
- полный `scripts/build.sh` baseline прошёл; refinement commit повторил
  lint/typecheck/103 tests/build и все Go gates отдельно, а committed clean
  worktree — `npm ci` и production frontend/Go build;
- `CGO_ENABLED=1 go build -tags "production pam"` — passed; installed amd64
  binary metadata фиксирует Go 1.26.5, `CGO_ENABLED=1`, tags `production,pam`, а
  `ldd` разрешает `libpam.so.0`;
- отдельная non-PAM remote-сборка завершилась fail-closed: exit 1 и stable
  `AUTHENTICATION_UNAVAILABLE`;
- SHA-256 clean build и установленного catalog binary совпадает:
  `5d50c3b708eff7ba2262d3958d7caa9c533745d351a76de99d24d4c120cfc202`;
  release `/opt/websetupmanager/releases/266917d3ed04` from source commit
  `266917d3ed04b3245f7e0f3461128a6d0d0bea0d`;
- `websetupmanager.service` — enabled/active, `User=user`, direct TLS listener
  `10.0.1.136:443`; live `/healthz` и `/readyz` — 200;
- `M-TLS` — guest/API gate, normal PAM login/logout, hash-only remembered login
  across graceful restart and final logout passed; TLS 1.2 handshake rejected.

Formatting, final `git diff --check` and clean-worktree checks выполняются ещё
раз после последней документационной правки и фиксируются в final report, а не
предполагаются здесь заранее.

Ранний headless Firefox same-origin loading-state screenshot остаётся
историческим smoke, а не controlled visual/keyboard/viewer или PAM acceptance.

Full target Definition of Done cannot be called complete until the explicit `X`
and AC partial scenarios above have recorded evidence or an approved deviation.

</details>
