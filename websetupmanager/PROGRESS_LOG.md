# Web Setup Manager — progress log

Последнее обновление: 2026-08-20. Ветка: `codex/web-setup-manager`.

Лог отделяет успешные автоматические проверки development environment от
непроведённой target qualification. Статусы требований и точное evidence
находятся в [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md).

Важно: результаты этапов 1–7 ниже относятся к baseline до добавления
PAM/browser-session auth. Они сохраняются как история, но не считаются
самостоятельным evidence новой auth-версии; повторный integrated прогон и live
deployment записаны в этапе 8.

## Discovery и этап 1 — каркас приложения

- Полностью прочитан `functional-requirements.ru.md`; извлечено ровно 221 P0 ID.
- Проверены repository/Git/Go/React patterns; создана development branch.
- Созданы Go module, embedded migrations/SPA, config, storage roots,
  `/healthz`, `/readyz`, security middleware, build/dev/test commands.
- Созданы базовые domain DTO/error/name/revision validators и React shell.
- Evidence baseline: backend unit/integration, frontend, local amd64
  health/readiness smoke были зелёными. Текущая arm64 PAM build/runtime
  qualification не выполнялась.

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
- Evidence на pre-auth baseline: App 16, API client 11, ImportWizard 10,
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
- Pre-auth baseline tree: `gofmt -l` empty, `git diff --check` passed,
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
- Pre-auth production smoke passed healthz/readyz, embedded index/current asset, `/fs`
  404, CSRF create+library list, HEAD and graceful signal exit code 0.
- Headless Firefox loaded same-origin API/assets and captured a loading-state
  screenshot; this is only a smoke and does not close visual/keyboard/viewer ACs.
- Mechanical matrix check found exactly 221 unique P0 IDs and 20 unique AC IDs,
  with no missing/extra P0 identifier in `IMPLEMENTATION_PLAN.md`.
- Не закрыты внешние target gates: arm64 runtime, численные NFR на LinuxCNC
  workstation, controlled-browser AC-12/16/20, conditional trusted-proxy/client
  certificate-trust deployment и repeated import/replace/duplicate power-loss
  AC-19. Они перечислены отдельно в plan/operator docs и не названы пройденными.

## Этап 8 — PAM login и browser sessions

- Добавлена fail-closed remote policy: configured non-root allowed user должен
  совпасть с process EUID; production remote требует Linux/cgo PAM adapter и
  TLS 1.3 либо явно trusted TLS proxy. Сборка без PAM остаётся только local-dev
  вариантом и remote mode не запускает.
- Добавлены публичные SPA/auth session/login/logout routes без HTTP Basic;
  domain API защищён opaque `__Host-` Secure/HttpOnly/SameSite-Strict cookie или
  optional Bearer automation secret. Session mutations используют exact HTTPS
  Host/Origin и отдельный CSRF; login ограничен по IP/имени, concurrency и
  общей session capacity.
- PAM проверяет только configured Linux account (`authenticate` +
  `account management`) и не хранит password. Обычные sessions memory-only;
  «Запомнить меня» в migration 002 сохраняет только SHA-256 token hash, CSRF,
  username, timestamps и deployment scope. Raw cookie token/password в SQLite
  отсутствуют.
- Frontend начинает с session discovery, показывает доступную русскую форму
  входа, очищает/refocus password при общей ошибке, обрабатывает expiry/401 для
  fetch и streaming XHR и предоставляет явный logout.
- Добавлены PAM policy template и обновлён operator deployment contract:
  service account равен `ALLOWED_USER`; build использует
  `CGO_ENABLED=1 -tags "production pam"` и системный libpam.
- Untagged `go test ./... -count=1` и race suite прошли; в race run
  `internal/service` завершился за 53.682 s. Полные PAM-tagged Go test/race/vet
  также прошли. Отдельный non-PAM remote binary ожидаемо завершился с exit 1 и
  stable `AUTHENTICATION_UNAVAILABLE`.
- Frontend lint/typecheck прошли; Vitest — 12 files/83 tests; TypeScript/Vite
  production build прошёл. Полный `scripts/build.sh` и
  `CGO_ENABLED=1 -tags "production pam"` Go build прошли.
- Build и установленный `/opt/websetupmanager/current/bin/websetupmanager`
  совпадают: SHA-256
  `5df67ec084ec30e0f253f6fd38f565adbe9e4eb8656edc180f3fa2454be8469d`;
  metadata подтверждает Go 1.26.5/cgo/`production,pam`, runtime — `libpam.so.0`.
- `/etc/pam.d/websetupmanager` root-owned `0644`, env root-owned `0600`, TLS key
  root:`user` `0640`; systemd unit enabled/active от `user` и слушает
  `10.0.1.136:443`. Live health/readiness вернули 200, TLS 1.2 отклонён.
- Live auth sequence: guest session 200 при capabilities 401; normal PAM login,
  authenticated session/capabilities и logout; remembered PAM login с только
  SHA-256 token hash в SQLite, graceful service restart, восстановленная
  authenticated session и logout с удалением row — passed.
  HTTP logs содержали только безопасные route/status/size fields, без password,
  raw cookie или username.
- Current certificate self-signed для `microb.int`/`10.0.1.136`; client trust,
  controlled-browser walkthrough и optional trusted-proxy deployment остаются
  отдельными operational/target gates.
