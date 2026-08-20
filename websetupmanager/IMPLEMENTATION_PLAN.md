# Web Setup Manager — implementation plan

Последнее обновление: 2026-08-20. Ветка: `codex/web-setup-manager`.

Этот файл является живым планом и матрицей трассируемости. Статусы: `planned`,
`implemented`, `verified`, `blocked`. Требование переводится в `verified` только
после зелёного автоматического теста или выполнения указанного ручного сценария.

## Архитектура

Web Setup Manager будет отдельным модулем в `websetupmanager/` и отдельным
непривилегированным Go-процессом. React SPA обращается только к same-origin
`/api/v1`. Публичная модель содержит Setup, Artifact, revision, version, job и
устойчивые IDs; физические пути и ключи объектов остаются внутри backend.

```text
React SPA + Web Workers
        |
        | same-origin /api/v1 (CSRF, Origin/Host, stable errors)
        v
Go HTTP API -> application service -> SQLite repository
                         |                  |
                         v                  v
                managed object store   journal/audit/jobs
                (root FD + openat2)     current/recent/UI state
```

Основные пакеты:

- `cmd/websetupmanager`: startup, signal handling, HTTP timeouts;
- `internal/config`: environment/flag configuration and root validation;
- `internal/database`: process lock, SQLite pragmas, embedded migrations,
  quick-check, recovery and garbage-collection coordination;
- `internal/domain`: public/domain types, validation and stable errors;
- `internal/storage`: streaming staging, root-anchored immutable object store,
  version checks and safe garbage collection;
- `internal/service`: setup transactions, revision concurrency, jobs,
  idempotency, journal and audit;
- `internal/httpapi`: domain routes, Range content, safe sheet responses,
  security headers and embedded SPA;
- `web/src`: library, setup card, dialogs, import wizard, preview worker and
  controlled Setup Sheet viewers.

## Этапы реализации

1. **Каркас** — module/package scaffolding, configuration, embedded migrations
   and SPA, health/readiness, Makefile/scripts, baseline tests.
2. **Данные и домен** — schema, IDs, states, revisions, current/recent/UI state,
   jobs, audit, journal, idempotency, recovery and GC.
3. **Безопасное хранилище** — streaming staging, atomic immutable publication,
   root handles/openat2, regular-file/version verification, capacity limits.
4. **Доменный API** — library/detail/import/program/sheet/validate/current,
   duplicate/archive/restore/delete, jobs/recent/UI state, stable errors.
5. **Основной UI** — setup library/detail, mutations, import, status reasons,
   current setup, jobs and accessible dialogs/states.
6. **Просмотр** — Range/ETag backend, bounded Worker block cache, sparse line
   index, literal search, virtualized rows, PDF.js viewer and sanitized HTML.
7. **Доводка** — path/race/crash/large sparse suites, AC matrix, all quality
   gates, operator/developer docs, runtime smoke test, logical commits and push.

После каждого этапа выполняются относящиеся к нему проверки, обновляются эта
матрица и `PROGRESS.md`; коммит создаётся только при зелёных проверках.

## Модель данных

SQLite хранит метаданные, но не содержимое артефактов. Начальная migration
создаёт `schema_migrations`, `library_instances`, `setups`, `setup_artifacts`,
`storage_objects`, `validation_runs`, `current_setup`, `recent_setups`,
`import_sessions`, `import_artifacts`, `jobs`, `ui_state`, `operation_journal`,
`audit_events`, `idempotency_requests` и короткоживущие
`delete_confirmations`.

- IDs — случайные 128-bit URL-safe/hex значения, не производные от имени.
- `setups.revision` монотонно увеличивается в той же SQLite transaction, что и
  успешная пользовательская мутация.
- `setup_artifacts` хранит роль `program|setup_sheet`, display name, main flag,
  order and expected opaque object version.
- `storage_objects` адресуются только внутренним SHA-256 key, неизменяемы и
  могут разделяться дубликатами; ссылки определяются FK rows, а не доверенным
  вручную изменяемым счётчиком.
- journal state machine: `intent -> storage_published -> db_committed -> completed`;
  startup recovery reconciles all non-terminal rows before readiness.
- archived setup сохраняет `archived_from_status`; current setup сохраняет
  revision and selection time even if later becoming not-ready.

## Структура API

API prefix: `/api/v1`. JSON uses camelCase; every error has `code`, `message`,
`requestId`, optional `details`, and `retryable`. Mutations accept
`Idempotency-Key`; setup mutations require `expectedRevision`; artifact
replacement/content consistency additionally use opaque version/ETag.

| Area | Routes |
|---|---|
| Runtime | `GET /healthz`, `GET /readyz`, `GET /api/v1/capabilities` |
| Library | `GET/POST /api/v1/setups`, `GET/PATCH /api/v1/setups/{id}` |
| Import | `POST /api/v1/setup-imports`, `POST .../{id}/artifacts`, `POST .../{id}/commit`, `DELETE .../{id}` |
| Programs | `POST .../{id}/programs`, `PUT/PATCH/DELETE .../{id}/programs/{artifactId}`, `HEAD/GET .../content` |
| Setup Sheet | `GET/PUT/DELETE .../{id}/setup-sheet`, `GET .../setup-sheet/content` |
| Lifecycle | `POST .../{id}/validate|duplicate|archive|restore|delete-plan`, `DELETE .../{id}` |
| State | `GET/PUT /api/v1/current-setup`, `GET/DELETE /api/v1/recent-setups`, `GET/PUT /api/v1/ui-state` |
| Jobs | `GET/DELETE /api/v1/jobs/{jobId}` |

No `/fs`, path browse, arbitrary download, execution or LinuxCNC endpoint will
exist.

## Стратегия хранения

`library_dir` and `state_dir` are configured at startup, canonicalized and must
be disjoint and non-nested. Both roots are opened once as directory handles.
Linux operations use `openat2(RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_SYMLINKS)`;
the tested fallback walks generated path components with `openat` +
`O_NOFOLLOW`. `fstat` must report a regular file for content.

Uploads stream into an exclusive, non-executable staging file while computing
SHA-256, validating content and checking configured/free-space budgets. Publish
copies to an exclusive temporary object below the managed root, fsyncs file and
directory, then atomically renames to its immutable digest location. SQLite only
links the object after publication. Cancellation/error removes staging; recovery
and GC remove only unreferenced objects absent from active journal rows.

Opaque artifact version combines object identity, size, mtime and ctime. Reads
open by internal object ID, compare the stored version before/after streaming,
and expose only a digest-derived ETag. Replacement checks both setup revision
and artifact version before making the new DB link visible.

## Тестовая стратегия

- Go unit tests: config, name/content validation, state transitions, cursor and
  stable error mapping, HTML sanitizer and Range parser.
- Go integration tests: real SQLite/temp roots for every domain operation,
  atomic import/replace/duplicate, optimistic conflicts, idempotency, audit,
  jobs, recovery and GC.
- Path-security suite: external sentinel, foreign IDs, traversal variants,
  NUL/separators, symlink/magic-link attempts, FIFO/socket, object swap and
  concurrent mutation; run normally and with `-race`.
- Large-file suite: sparse 5–10 GiB file with bytes near beginning/end; verify
  HEAD/Range/ETag and bounded allocation without allocating real disk blocks.
- Frontend Vitest/Testing Library: library states, setup actions, import roles,
  conflict/offline handling, virtualized preview, search worker protocol,
  viewer sandbox and modal focus return.
- Production: lint, typecheck, tests, Vite build, embedded Go build, `go vet`,
  `go test`, supported `go test -race`, `git diff --check`, startup plus
  `/healthz` and `/readyz` smoke checks.

## Покрытие P0

Все 221 P0 ID перечислены ниже. Группировка не отменяет индивидуальную
трассируемость: каждый ID должен получить тот же итоговый статус и evidence,
либо будет вынесен отдельной строкой при обнаружении исключения.

| P0 IDs | Implementation / evidence | Status |
|---|---|---|
| `FR-DEP-001`–`FR-DEP-006` | process/embed/config/health tests + production smoke | verified |
| `FR-CFG-001`–`FR-CFG-008` | `internal/config`, startup integration tests | verified |
| `FR-SET-001`–`FR-SET-009` | schema/domain types verified; service operations pending | implemented |
| `FR-STATE-001`–`FR-STATE-007` | transition/reconciliation tests + status UI tests | planned |
| `FR-CURRENT-001`–`FR-CURRENT-006` | current setup service/API/UI/audit tests | planned |
| `FR-VAL-001`–`FR-VAL-007` | revision-bound validation job tests + UI disclaimer | planned |
| `FR-UX-001`–`FR-UX-008` | library component/API pagination state tests | planned |
| `FR-DETAIL-001`–`FR-DETAIL-006` | setup detail/action/job component tests | planned |
| `FR-A11Y-001`–`FR-A11Y-003` | keyboard and modal focus tests | planned |
| `FR-OP-001`–`FR-OP-008` | confirmation/idempotency/conflict/job/cancel tests | planned |
| `FR-CREATE-001`–`FR-CREATE-005` | create/patch validation and dialog tests | planned |
| `FR-IMPORT-001`–`FR-IMPORT-013` | streaming import session API/UI/recovery suite | planned |
| `FR-PROG-001`–`FR-PROG-006`, `FR-PROG-008` | program mutation/API/UI/atomicity tests | planned |
| `FR-DUP-001`–`FR-DUP-005` | duplicate transaction/recovery/UI tests | planned |
| `FR-DEL-001`–`FR-DEL-007` | archive/restore/delete-plan/ref-safe GC tests | planned |
| `FR-GC-001`–`FR-GC-004`, `FR-GC-010`–`FR-GC-017`, `FR-GC-020`–`FR-GC-027`, `FR-GC-030`–`FR-GC-034` | Range/security/large sparse + Worker/preview/search tests | planned |
| `FR-SS-001`–`FR-SS-009`, `FR-SS-020`–`FR-SS-024` | sheet lifecycle/API/PDF/HTML viewer tests | planned |
| `SEC-SS-001`–`SEC-SS-007` | sanitizer/CSP/sandbox/PDF link tests | planned |
| `FR-SEARCH-001`–`FR-SEARCH-003` | SQL query/cursor and library UI tests | planned |
| `FR-HIS-001`–`FR-HIS-006` | recent/UI-state persistence tests | planned |
| `SEC-PATH-001`–`SEC-PATH-005`, `SEC-PATH-010`–`SEC-PATH-017` | root-FD/openat2 base suite verified; domain content attack matrix pending | implemented |
| `SEC-RACE-001`–`SEC-RACE-005` | object version/substitution base tests pass; mutation races pending | implemented |
| `DATA-001`–`DATA-006` | database migration/pragma/backup/lock tests | verified |
| `DATA-007`–`DATA-010` | journal/recovery/ref-safe schema verified; service/GC cases pending | implemented |
| `API-001`–`API-007` | stable envelope/security scaffold verified; domain contracts pending | implemented |
| `SEC-NET-001`–`SEC-NET-008` | bind/remote fail-closed/auth/origin/CSRF/CSP/cache tests | verified |
| `NFR-PERF-001`–`NFR-PERF-008` | benchmark/smoke/10k/sparse resource scenarios | planned |
| `NFR-REL-001`–`NFR-REL-006` | graceful shutdown/startup recovery foundation verified; domain cases pending | implemented |
| `NFR-LOG-001`–`NFR-LOG-004` | safe structured request logging verified; operation audit pending | implemented |

## Другие обязательства P0 без ID

| Source | Coverage | Status |
|---|---|---|
| §4.1 / §4.3 product boundaries | route inventory, domain DTOs, library-only SPA; no file browser/execution API | implemented |
| §6.1 configuration defaults | config constants and unit tests | verified |
| §14.1 logical schema | embedded migration with all listed tables plus guarded support tables | verified |
| §15 recommended domain API | route contract implementation and integration tests | planned |
| §15.1 minimum error-code list | `domain.RequiredErrorCodes` unit test | verified |
| §17 empty/error states | React component scenarios | planned |
| §22 Definition of Done | final quality/acceptance checklist | planned |

## Покрытие AC-01–AC-20

| AC | Проверяемый сценарий / evidence | Status |
|---|---|---|
| AC-01 | SPA library test + route inventory asserts no `/fs` or browser | planned |
| AC-02 | create draft API integration + UI test | planned |
| AC-03 | four-artifact atomic import integration + wizard test | planned |
| AC-04 | cancelled streamed import cleanup/restart test | planned |
| AC-05 | revision-bound validate then replace transition test | planned |
| AC-06 | current selection persistence/audit/no-execution test | planned |
| AC-07 | one setup-level sheet visible from every program test | planned |
| AC-08 | cancelled/truncated replace preserves old object/revision test | planned |
| AC-09 | duplicate IDs/status/shared-immutable independence test | planned |
| AC-10 | archive/restore identity/composition/history test | planned |
| AC-11 | external sentinel and complete path attack matrix | planned |
| AC-12 | sparse >5 GiB Range + bounded backend/virtual DOM scenario | planned |
| AC-13 | Worker literal match near sparse-file end + cancellation test | planned |
| AC-14 | ETag/version mismatch during preview -> attention test | planned |
| AC-15 | remove/recreate same object name/identity -> attention test | planned |
| AC-16 | malicious HTML sanitizer/CSP/sandbox/no-network test | planned |
| AC-17 | FIFO/socket/symlink nonblocking reconciliation test | planned |
| AC-18 | stale revision API/UI conflict preserving form test | planned |
| AC-19 | recovery fixtures for import/replace/duplicate and journal test | planned |
| AC-20 | keyboard library/detail/preview/viewer/current + focus-return test | planned |
