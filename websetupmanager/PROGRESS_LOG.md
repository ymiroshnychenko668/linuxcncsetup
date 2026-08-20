# Web Setup Manager — progress log

Последнее обновление: 2026-08-20. Ветка: `codex/web-setup-manager`.

Лог отделяет успешные автоматические проверки development environment от
непроведённой target qualification. Статусы требований и точное evidence
находятся в [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md).

## Discovery и этап 1 — каркас приложения

- Полностью прочитан `functional-requirements.ru.md`; извлечено ровно 221 P0 ID.
- Проверены repository/Git/Go/React patterns; создана development branch.
- Созданы Go module, embedded migrations/SPA, config, storage roots,
  `/healthz`, `/readyz`, security middleware, build/dev/test commands.
- Созданы базовые domain DTO/error/name/revision validators и React shell.
- Evidence: backend unit/integration, frontend baseline, production amd64 build,
  arm64 cross-build и local health/readiness smoke были зелёными. arm64 runtime
  не выполнялся.

## Этап 2 — домен и хранение данных

- SQLite schema включает setups/artifacts/storage objects, validation runs,
  current/recent, jobs, UI state, import sessions, journal, audit, idempotency и
  short-lived confirmations.
- Реализованы stable IDs, revision concurrency, draft/ready/attention/archived,
  current/history, jobs, idempotent claim/replay, audit и startup recovery.
- Journal reservation защищает опубликованный immutable object до DB adoption;
  логическая mutation и terminal journal state фиксируются одной transaction.
- Для upload/validation/duplicate/restore доменный результат и terminal job
  фиксируются одной transaction; upload в том же commit сохраняет content
  digests и внешний run-idempotency result. Import атомарно завершает setup,
  session, job, journal, audit и idempotency.
- Evidence: `TestOpenMigratesFullInitialSchema`, schema/pragmas/lock/migration/
  backup/recovery tests, domain ID/name/revision tests,
  `TestIdempotencyClaimReplayConflictAndExpiry`,
  `TestPersistentJobsProgressCancellationAndTerminalStability`, lifecycle tests.
- Проверки после этапа: `go test ./...`, `go vet ./...` — passed.

## Этап 3 — безопасное managed storage

- Roots удерживаются directory FDs; Linux resolver использует `openat2`
  beneath/no-symlink flags с root-anchored `*at` fallback.
- Upload потоково stage/hash/validate, проверяет limits/free space, публикует
  immutable SHA-256 object атомарно и очищает temp при error/cancel/restart.
- Content принимает только entity IDs; regular-file/version checks блокируют
  traversal, symlink, FIFO/socket/device и object substitution.
- GC удаляет только ref-free object без active journal reservation.
- Evidence: весь `internal/storage` suite; HTTP sentinel/path matrix;
  `TestHTTPRejectsSymlinkFIFOAndSocketWithoutBlocking`;
  reservation/GC race tests; sparse 10 GiB test.
- Проверки после этапа: `go test ./internal/storage ./internal/httpapi
  ./internal/service` — passed; package-level `-race` security suites — passed.

## Этап 4 — доменный API

- Реализованы library/detail/create/metadata, streaming atomic import,
  add/replace/rename/delete/set-primary, Setup Sheet, validation, current,
  duplicate, archive/restore/delete-plan/permanent-delete, jobs, recent/UI-state
  и audit endpoints.
- Mutations используют CSRF/Host/Origin, expected revision/version и
  `Idempotency-Key`; errors stable и не раскрывают storage details.
- Content реализует HEAD/Range/ETag/If-Match и корректные conditional errors.
- Network streaming использует sliding deadlines для request-body reads и
  response writes; absolute server ReadTimeout выключен, header timeout отделён.
- Evidence: service lifecycle/import/operations suites;
  `TestHTTPAcceptanceCreateUploadValidateCurrentAndRangeETag`;
  concurrent stale-revision, atomic multi-program и Range parser tests.
- Проверки после этапа: `go test ./...`, `go vet ./...` — passed.

## Этап 5 — основной UI

- Реализованы pinned current area, library search/filter/sort/cursor, setup
  detail, create/import, metadata/program/sheet/lifecycle dialogs, job progress,
  conflict/offline/error/empty states и recent/UI-state recovery.
- Modal trap/initial/return focus; actions are visible buttons and statuses are
  textual. Current selection requires explicit no-execution confirmation.
- Evidence on the current tree: App 16, API client 11, ImportWizard 10,
  program/setup operation dialogs 5, Modal 5, create-name preflight 1 and
  client-state 2 tests (50 stage-5 component/client tests).
- Проверки после финальной интеграции: весь frontend Vitest — 68/68;
  ESLint, TypeScript typecheck and Vite production build — passed.
- Полный visual/keyboard browser walkthrough остаётся target scenario; в
  development environment browser automation отсутствует.

## Этап 6 — просмотр G-code и Setup Sheet

- G-code preview использует 1 MiB Range blocks, bounded 8-block cache,
  virtualized visible rows/overscan, sparse Worker index, literal full-stream
  search/cancel/progress, line jump, wrap toggle and safe React highlighting.
  После thinning навигация достигает любой строки последовательными
  cancellable bounded Range-блоками, не полагаясь на скрытый 1 MiB gap.
- Every block uses `If-Match`; version mismatch triggers artifact-changed flow.
- PDF.js renders canvas with evaluation/XFA/annotation actions disabled. HTML is
  prevalidated before publication, streamed through 512 KiB backend reads,
  sanitized and loaded in an empty-sandbox originless iframe with document CSP
  and no credentials. Total document size has no hidden viewer cap; one HTML
  token is limited to 1 MiB for bounded sanitizer memory.
- Evidence: Range/ETag/sparse/identity HTTP integration tests; adaptive sparse
  traversal/cancellation tests; GCodePreview/SetupSheetViewer tests; oversized
  token pre-publication rejection, >3 MiB bounded-token streaming and malicious
  HTML sanitizer/end-to-end header tests (18 stage-6 frontend tests).
- Проверки после этапа: backend/frontend tests and production build — passed.
  Actual malicious PDF/browser network observation and 10 GiB DOM/RSS remain
  target-only checks.

## Этап 7 — acceptance, security и доводка

- Добавлены backend acceptance fixtures для AC-03/05/06/08/11/12/14/15/17/18
  и recovery fixtures AC-19; path matrix включает foreign IDs, absolute/UNC,
  traversal, single/double encoding, NUL, symlink, FIFO, socket and sentinel.
- 10 GiB sparse HEAD/Range проверяет 64-bit offsets и bounded allocation без
  выделения 10 GiB blocks. Reservation-vs-GC test проверяет конкуренцию adoption.
- Crash suite моделирует pre-commit durable journal states import/replace/
  duplicate и startup reconciliation. Atomic regression отдельно фиксирует
  committed add/replace/Setup Sheet, validation, duplicate и restore, закрывает
  job/result/run-idempotency до reopen и проверяет, что replay не повышает
  revision повторно. Ошибка сериализации terminal result откатывает доменную
  transaction; queued validation cancellation освобождает ожидающий worker.
- `TestProcessKillRollsBackArchiveDeleteAndCurrentSelection` выполняет настоящий
  subprocess SIGKILL внутри незавершённой transaction. Повторный target
  power-loss для import/replace/duplicate остаётся отдельной квалификацией.
- Full SHA reconcile безопасно rebind-ит идентичные bytes после cold copy к
  новой physical version, но оставляет setup не-ready до validation;
  `TestFullReconcileRebindsIdenticalColdCopyWithoutClearingAttention` passed.
- Финальный tree: `gofmt -l` empty, `git diff --check` passed,
  `go mod tidy -diff` empty, `go vet ./...` passed.
- `go test ./... -count=1 -timeout=3m` passed (`internal/httpapi` 2.577 s,
  `internal/service` 3.490 s); final JSON-count rerun recorded 7 packages,
  310 passed test/subtest events and 0 failed. `go test -race ./... -count=1 -timeout=8m`
  passed (`internal/httpapi` 13.911 s, `internal/service` 49.939 s).
- Fresh npm install, lint и typecheck passed; serial Vitest — 11 files, 68/68,
  55.63 s; Vite production build — 51 modules, passed.
- A clean `git archive` checkout completed `scripts/build.sh` from a missing
  `web/dist`, `node_modules` and `build` tree: npm restore, lint, typecheck,
  68/68 tests, Vite build, Go tests/vet and embedded production build passed.
- Static amd64 production SHA-256
  `f79750f8e8cf06313e76cddb6cffb0898badd9ce50f9c8092a8538064a486f67`;
  arm64 `CGO_ENABLED=0` cross-build SHA-256
  `fd8a4f2d086ca1f86dc2bcf8495804ff0fe87a30e72575692512065ef0e39008`.
- Production smoke passed healthz/readyz, embedded index/current asset, `/fs`
  404, CSRF create+library list, HEAD and graceful signal exit code 0.
- Headless Firefox loaded same-origin API/assets and captured a loading-state
  screenshot; this is only a smoke and does not close visual/keyboard/viewer ACs.
- Mechanical matrix check found exactly 221 unique P0 IDs and 20 unique AC IDs,
  with no missing/extra P0 identifier in `IMPLEMENTATION_PLAN.md`.
- Не закрыты внешние target gates: arm64 runtime, численные NFR на LinuxCNC
  workstation, controlled-browser AC-12/16/20, direct TLS/proxy deployment и
  repeated import/replace/duplicate power-loss AC-19. Они перечислены отдельно
  в plan/operator docs и не названы пройденными.
