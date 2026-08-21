# Web Setup Manager — progress log

Последнее обновление: 2026-08-21. Ветка: `codex/web-setup-manager`.

Лог отделяет успешные automated/production-host проверки от оставшейся
внешней LAN/browser/QtDragon qualification. Статусы требований и точное evidence
находятся в [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md).

Важно: этапы 1–8 ниже относятся к superseded managed-library продукту. Они
сохраняются как история реализованной техники и PAM deployment, но не являются
evidence новой catalog-модели. Актуальные требования и статусы находятся в
[PRODUCT_REQUIREMENTS.ru.md](PRODUCT_REQUIREMENTS.ru.md) и в верхней части
[IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md).

## Этап 9 — product direction reset и LinuxCNC catalog

- Владелец станка отверг validation/readiness workflow, multi-program Setup и
  dashboard/card UI. Новая модель: компактный catalog/upload tool, один Setup =
  `0..1` программа + `0..1` Setup Sheet, неполный Setup допустим, folders
  соответствуют filesystem.
- Read-only discovery подтвердил, что LinuxCNC 2.9.10 сейчас запущен от `user`
  с `/home/user/linuxcnc/configs/corvuscnc/g540.ini`; фактический
  `PROGRAM_PREFIX` и сохранённый QtDragon user directory —
  `/home/user/linuxcnc/nc_files`. Root пуст, real ext4 directory `user:user`;
  отдельный `/home/user/linuxcnc/nc_programs` существует, но не используется.
- Текущая deployed legacy-версия хранит bytes в
  `~/.local/share/websetupmanager/library/objects` и потому не делает программы
  видимыми в QtDragon. Unit также имеет `ProtectHome=read-only` и пока разрешает
  запись только в state/library roots.
- Созданы актуальные требования
  [PRODUCT_REQUIREMENTS.ru.md](PRODUCT_REQUIREMENTS.ru.md) и безопасный
  [MIGRATION_PLAN.md](MIGRATION_PLAN.md). Исходные 221 P0 и `AC-01`–`AC-20`
  помечены архивными; pass новой модели ими не заявляется.
- Зафиксированы config contract `PROGRAM_ROOT`/`LINUXCNC_INI`/display hint,
  каталоговые IDs/relative paths, singular file roles, direct atomic named
  publish и left-viewer/right-tree UX.
- Этап direction reset не изменял существующие legacy objects, SQLite и
  production service; реализация catalog-модели продолжена отдельными этапами
  ниже.

## Этап 10 — catalog schema, domain и direct storage

- Добавлена forward-only migration `003_catalog_workspace.sql`: folders,
  setups, unique singular file roles, catalog operation journal, migration
  state/mappings/file manifest и provenance key для migration-owned folders.
- Реализованы устойчивые catalog IDs/revisions, неполные Setup, физическая
  hierarchy, CRUD/move/delete и `0..1 program + 0..1 setup_sheet` без
  validation/readiness/current workflow.
- Новый `CatalogStore` удерживает `PROGRAM_ROOT` FD, разрешает операции beneath
  root, запрещает traversal, `ngcgui_lib`, symlink, hardlink и special files.
  Prepared folder/file publication использует exclusive hidden entry,
  no-replace/atomic rename, identity/digest checks и parent `fsync`.
- Automated evidence прошло в catalog storage/service suites: publish,
  replacement commit/rollback, post-publish substitution, move/quarantine,
  external sentinel, traversal, symlink/hardlink/FIFO/reserved tree, root path
  replacement и prepared-folder callback failure.

## Этап 11 — catalog API, preconditions и recovery

- Публичный production surface ограничен `/api/v1/catalog`; legacy setup,
  validation/current/jobs API не монтируется. DTO возвращают только opaque IDs,
  `relativePath` и configured `rootDisplay`, без absolute root/storage key/
  physical identity.
- Component create требует ровно `If-None-Match: *`; replace/delete — ровно один
  exact quoted lowercase 64-hex `If-Match`. Все filesystem mutations также
  связаны expected revision, idempotency claim и durable catalog journal.
- Startup до listener выполняет legacy operation/import recovery, legacy
  content inspection, catalog operation recovery и затем migration. Любая
  ошибка или persisted `manual_review` блокирует startup.
- Automated HTTP evidence: CRUD/direct placement, nullable root moves,
  Range/ETag replacement, sanitized HTML, filename decoding/traversal,
  catalog-only production routes, idempotent create/stable conflicts и
  unknown-length upload.
- Recovery evidence покрывает `intent`, `storage_applied`, `db_applied`,
  prepared folder, uncommitted delete, substituted targets и special/symlink
  journal entries. Отдельный subprocess действительно получает `SIGKILL` после
  filesystem publication; restart убирает uncommitted target и допускает
  безопасный retry с тем же key.
- Sparse 10 GiB fixture проверяет 64-bit metadata и bounded tail Range без
  выделения 10 GiB реального пространства или полного чтения файла.

## Этап 12 — idempotent no-data-loss migration

- Startup migrator преобразует legacy Setup с 0/1/N программами соответственно
  в один incomplete/один/несколько catalog Setup; общая sheet fan-out копируется
  для каждого однозначного target.
- Legacy rows/objects не изменяются. Каждая target file получает manifest с
  source artifact/object provenance, role, relative target, size/SHA-256 и
  catalog file ID; migration-owned folders получают unique source key.
- При resume общего `pending`/`running` run уже `completed` per-source mapping
  повторно проверяет manifest cardinality, source IDs/digest/size, catalog
  linkage/version/physical identity. Общий terminal `completed` затем является
  no-op. Same-name folder без ожидаемой provenance не усваивается; collision или
  неоднозначность фиксируется как `manual_review` и fail-closed блокирует listener.
- Automated migration suites прошли для 0/1/N split, sheet fan-out, restart,
  completed provenance, logical/physical collision и unowned same-name folder.
- Migration 004 сохраняет checksum v3 и backfill-ит setup source key только из
  единственной exact same-library mapping с совпавшим legacy setup ID;
  inconsistent/orphan/ambiguous linkage транзакционно fail-closed. Populated
  v3→v4 reopen/resume после истечения idempotency TTL покрыт integration test.
- Live schema v4 migration завершилась: 2 folders, 2 setups, 4 files,
  2 completed mappings и 4 copied manifests. Source/target sizes и SHA-256
  совпали, legacy rows/objects не удалены; повторный restart сохранил counts.

## Этап 13 — compact catalog frontend

- Основной Workbench заменён на compact left-viewer/right-explorer layout с
  tabs/breadcrumbs/statusbar, resizable divider и mobile drawer. Tree имеет
  folder/setup hierarchy, selection, filtering и roving keyboard navigation.
- Dialogs поддерживают folder/setup CRUD, destination preview, создание
  incomplete Setup и add/replace/delete одного program/sheet. После upload
  показывается persistent operator-facing location; retryable ambiguous error
  сохраняет idempotency key.
- G-code Range/Worker/virtualization/search и PDF canvas viewer переиспользованы
  через catalog content URLs. Sanitized HTML сначала fetch-ится trusted SPA,
  затем показывается как revocable Blob URL в iframe с пустым sandbox и opaque
  origin.
- Frontend lint/typecheck прошли; Vitest — 15 files / 87 tests, включая App/Workbench,
  catalog API/dialog/tree, authentication и viewers; Vite production build
  прошёл. Production desktop/mobile screenshots записаны на этапе 14; сквозной
  keyboard-only flow и найденные им focus-регрессии закрыты на этапе 15.

## Этап 14 — production cutover и проверка generation

- Перед cutover создана cold generation
  `/var/backups/websetupmanager/pre-catalog-20260821T145214Z`. Все четыре
  archives прошли сверку записанных SHA-256; полный extract/diff завершился
  `RESTORE_CHECK_OK`.
- Установлен versioned release
  `/opt/websetupmanager/releases/18411e613b380c4b` из source commit
  `18411e613b380c4b73837003b96c949a21661041`; binary SHA-256
  `8b3b758d248f2bdb0270cf06215d9e944542b83b3fc8fd1da972d79da84be3cf`.
  Full Go/PAM/race/vet/build и frontend lint/typecheck/15 files / 84 tests/Vite
  gates прошли.
- `websetupmanager.service` enabled/active от `user`, direct HTTPS listener —
  `10.0.1.136:443`; port 80 не слушается. `/healthz` и `/readyz` вернули 200.
  PAM account — `user`, используется текущий системный Linux password, который
  не записывается; optional Bearer не задан.
- SQLite: `quick_check=ok`, foreign-key violations 0, catalog state completed.
  Exact source/target hash и size verification прошла; temp remnants нет;
  legacy data сохранены. Restart idempotent и не изменил 2/2/4/2/4 counts.
- LinuxCNC stat до/после остался `file=""`,
  `state/mode/interp/exec=1/1/1/2`: WSM не загружал и не запускал программу.
- Production HTTPS проверен headless Firefox ESR/WebDriver BiDi: guest, PAM
  login `user`, ready/catalog/UI и logout прошли. Записаны desktop `1366x768` и
  mobile `390x844` screenshots в `/tmp/wsm-catalog-evidence.ZSP7Ft`.
- Первый visual run обнаружил G-code viewport 0 px. Commit
  `18411e613b380c4b73837003b96c949a21661041` заменил catalog editor grid→flex;
  после redeploy/rerun видны подсвеченный G-code и tree. Authenticated desktop
  assert: 37 virtual rows, первая `%`, viewport `1030x516` для файла 1.7 MiB.
- Screenshot SHA-256 login desktop/mobile:
  `fbf1e313ec372d6f87473860a8e87263c4682868e0357a4903426088d2087773` /
  `cdb6610b62b4c4bcd4812efa50d6ebf13e81a278cb8616ecc1cb259db368f0ae`;
  authenticated desktop/mobile:
  `0b4c7f29b2761fb78fe0dedb7aac6d1a91bcfc99ab93f89ef5d797bf6c6c305d` /
  `d036fa0664c7be11ff074d7ca42a2074d4a20102a9a0b0669d97b8901cb04ebc`.
- На момент первого cutover внешними оставались отдельный LAN client, DHCP
  reservation, полный keyboard-only flow, controlled 10 GiB browser perf и
  ручной QtDragon inspection. Keyboard flow закрыт следующим этапом.

## Этап 15 — keyboard acceptance, QtDragon audit и focus release

- Один App integration scenario прошёл только keyboard events через PAM login
  view, roving catalog tree, upload dialog/file choice, G-code preview literal
  search, line jump и UI logout. Вместе с tree/modal/viewer component suites это
  закрывает `CAT-P0-020` и `CAT-AC-11`; frontend итог — 15 files / 87 tests.
- Тест обнаружил две реальные регрессии. `Modal` фиксировал инициатор после
  portal child `autoFocus` и возвращал focus в удалённый node; теперь инициатор
  запоминается до commit, а fallback идёт в `#catalog-editor`. Line-jump input
  имел изменяющийся React key и remount после Enter; теперь это controlled input
  без потери focus.
- Commit `12aa6a2adf3c9908a2120c03ed310aa40ac1fecc` прошёл frontend
  lint/typecheck/87 tests/build, обычные и PAM-tagged Go test/vet, PAM race
  (`internal/service` 83.571 s), `gofmt` и `go mod tidy -diff`.
- Clean production binary Go 1.26.5/cgo/`production,pam`, 14,933,016 bytes,
  SHA-256 `ee2f2afe0e0f3cf50ca79a57a36d94c4f1cbd971ea85474b599e11dd7bd9872a`
  установлен как `/opt/websetupmanager/releases/12aa6a2adf3c`. Symlink switched
  atomically; предыдущий release сохранён. Unit active от `user`, `NRestarts=0`,
  direct 443; `/healthz` и `/readyz` вернули 200, guest session —
  `authenticated=false`, `loginRequired=true`; TCP/80 по-прежнему закрыт.
- Read-only audit работающего QtDragon подтвердил PID/INI/local
  `_CORVUS_FILE_MANAGER`, exact `PROGRAM_PREFIX` и тем же `QFileSystemModel`
  видимую цепочку `linuxcnc/nc_files/Импортировано/adssad` с читаемыми строками
  `1002.ngc` и `1003.ngc`. Вкладка File не открывалась, input/selection не
  отправлялись, LinuxCNC ничего не загрузил и не исполнил.
- Все `CAT-P0-001`–`CAT-P0-024` и `CAT-AC-01`–`CAT-AC-12` имеют `V` в актуальной
  матрице. LAN/DHCP, target-hardware performance и ручной screenshot скрытой
  QtDragon File tab остаются дополнительной qualification, а не незакрытыми
  текущими AC.

## Этап 16 — file-first tree, inline Sheet и быстрый первый paint

- По прямой корректировке владельца станка split развёрнут: file tree находится
  слева, единая editor surface справа. G-code является родительской строкой
  Setup, а существующая Setup Sheet — дочерней строкой и отдельным editor tab;
  PDF/очищенный HTML показываются inline без viewer popup.
- Многошаговый upload dialog удалён из основного Workbench. «Добавить» запускает
  native multi-file picker с одним обязательным G-code и одной optional Sheet;
  program-only Setup остаётся leaf и получает прямое действие attach Sheet.
  Исторические sheet-only записи сохраняются и восстанавливаются добавлением
  G-code, а не удаляются.
- Повторяющиеся breadcrumb/commandbar/filename/path строки удалены; имя файла
  остаётся в tab, точный destination — в единственной status bar. Primary CTA
  получил спокойный тёмно-зелёный фон и светлый текст.
- Первый preview делает ровно один 64-КиБ Range, показывает законченные начальные
  строки и только после этого запускает Worker index. Pending-offset dedupe не
  допускает параллельного повторного main-thread запроса; exact ETag/If-Match и
  bounded последующие блоки сохранены.
- Frontend `lint`, `typecheck`, 15 files / 103 tests и Vite production build
  прошли. Обновлённый App scenario снова использует только keyboard events для
  login → left tree/child Sheet → native picker trigger → search/line jump →
  logout. Regression suites дополнительно закрывают stable-key replay только
  после ambiguous failure, fresh key/revision после deterministic conflict,
  late Range generation, newline boundary, search-effective expansion, mobile
  focus trap/inert и bounded/cancellable PDF text stream.
- Firefox BiDi на disposable roots подтвердил desktop `1366x768`: дерево слева,
  G-code справа с первой строкой `%`, child Sheet, одна status path, отсутствие
  breadcrumb/commandbar и primary contrast `6.02:1`; HTML Sheet показалась
  inline без dialog. Первый smoke обнаружил белый iframe: application CSP не
  разрешал созданный viewer-ом sanitized `blob:`. `frame-src 'self' blob:`
  добавлен с empty sandbox и document CSP без script/network; повторный smoke
  показал содержимое Sheet. Mobile `390x844` подтвердил left drawer, inert
  editor, Tab wrap и focus return после Escape.
- Финальные screenshots: `/tmp/wsm-ui-evidence-csp.OedI4k`; SHA-256 program /
  Sheet / mobile — `f83cedd4ab5b494717b45d5acf3724a520e65ffbcb9c11f0241edb08e08ef971` /
  `feadf0f6a31790df77376fb34657f533788242ef00d993caa48d38c76c6d536d` /
  `7ea399e3695d3922e71bd4170a14648a01f8de3c92f72e421fc1dd2acc9d0aa0`.
- Feature commit `266917d3ed04b3245f7e0f3461128a6d0d0bea0d` пересобран из
  committed clean source; PAM binary SHA-256
  `5d50c3b708eff7ba2262d3958d7caa9c533745d351a76de99d24d4c120cfc202`
  установлен отдельной root-owned release
  `/opt/websetupmanager/releases/266917d3ed04`. Symlink переключён атомарно,
  предыдущая release сохранена. Service active от `user`, `NRestarts=0`, direct
  443; port 80 закрыт, 8443 не изменён, health/ready 200, guest contract —
  `authenticated=false`, `loginRequired=true`.
- Post-deploy Firefox на `https://microb.int/` прошёл PAM login, catalog/G-code,
  ready и logout; 37 rows, первая `%`, viewport `1030x625`. Evidence:
  `/tmp/wsm-production-auth-refinement.OfbZwm`; authenticated desktop/mobile
  SHA-256 `ec322875a2a8aceadd719cd61623ac76a6b71010949f9c91066c75d7edf6310b` /
  `854acd9fe3036114e2b280cbd8f7cb64b99b584be0818d98a208484ee7ccbc0d`.

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
- Frontend lint/typecheck прошли; Vitest — 15 files/87 tests; TypeScript/Vite
  production build прошёл. Полный `scripts/build.sh` и
  `CGO_ENABLED=1 -tags "production pam"` Go build прошли.
- На этапе 8 build и установленный pre-catalog binary совпадали: SHA-256
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
