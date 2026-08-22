# Web Setup Manager

Web Setup Manager — компактный каталог и upload-инструмент для
LinuxCNC-станка. Основная сущность — **Setup** с не более чем одной G-code
программой и одной PDF/HTML Setup Sheet. Setup может быть неполным и
группируется по реальным каталогам внутри настроенного LinuxCNC
`PROGRAM_PREFIX`.

Основной экран следует плотному UX-паттерну Visual Studio Code: слева находится
дерево folders → G-code → optional Setup Sheet, справа — общий inline viewer.
Приложение показывает, в каком каталоге QtDragon найти загруженную программу.
Оно не проверяет технологическую готовность, не загружает и не исполняет G-code
в LinuxCNC.

Актуальные требования:
[PRODUCT_REQUIREMENTS.ru.md](PRODUCT_REQUIREMENTS.ru.md). Прежняя
multi-program/validation/library-card модель сохранена только как исторический
baseline в [functional-requirements.ru.md](functional-requirements.ru.md).

## Актуальный production contract

- один непривилегированный Go-процесс и embedded SQLite;
- встроенная production-сборка React/TypeScript, Node.js на станке не нужен;
- scoped `/api/v1/catalog` без универсального `/fs` и без выбора host root;
- folders и setups с устойчивыми IDs и относительными путями;
- direct streaming/atomic publish именованных файлов в `PROGRAM_PREFIX`;
- cardinality `0..1 program + 0..1 setup_sheet`, без validation gate;
- компактный left-tree/right-inline-viewer интерфейс с понятным destination;
- «Добавить» сразу открывает native picker: один G-code обязателен, одну Sheet
  можно выбрать вместе с ним или напрямую прикрепить позже; имя Setup выводится
  из имени G-code, многошагового create popup нет;
- первый version-bound prefix G-code ограничен 64 КиБ и показывается до запуска
  Worker index; последующая навигация/поиск используют bounded Range blocks;
- index progress находится в editor header; Web Worker одним bounded проходом
  строит sparse line index, поиск и read-only lexical Tool Table;
- editor содержит намеренно пустую вкладку Toolpath и keyboard-доступную Tool
  Table; они не читают LinuxCNC `TOOL_TABLE`, не строят траекторию и не
  управляют станком;
- version-bound origin-private browser cache: Cache Storage для допустимых raw
  chunks/analysis и `window.localStorage` только для bounded manifest и
  digest-only auth/cache recovery markers;
- PDF.js и sandboxed sanitized HTML viewer; source CSS/title/head удаляются,
  вместо них Backend добавляет application-owned readable/print stylesheet с
  hash-only document CSP и terminal completion marker;
- revision/version conflicts, audit, idempotency и recovery собственных temp;
- в remote mode — Linux PAM login, защищённые browser sessions, logout,
  «Запомнить меня», per-session CSRF и ограничение попыток входа; встроенного
  логина или пароля нет.

Raw browser cache принимает G-code размером не более 32 MiB, полными chunks по
1 MiB; общий budget равен 128 MiB, TTL raw/analysis — 30 суток. Cache Storage
удерживает до 48 analysis records размером не более 4 MiB каждый, localStorage
— до 24 manifest records. Browser может evict данные раньше из-за quota;
cache/private-mode failure не ломает online preview. Большой G-code не получает
полную persistent raw-копию и продолжает читаться ограниченными Range blocks.
Успешный remote logout блокирует новые cache writes и очищает scope текущего
principal/library, включая позднюю запись из другой вкладки. Password, cookie,
CSRF, Bearer, absolute path и storage key в этом cache не сохраняются.
Durable marker заставляет повторный login того же scope сначала завершить
прерванную browser cleanup после crash/reload.

Explicit-login continuation отдельно защищена от гонок login/logout. До
capabilities и разрешения cache новая session должна получить durable
SHA-256-only quarantine proof; если browser не смог записать его ни в Cache
Storage, ни в per-fingerprint localStorage key, workspace не открывается и
session условно отзывается. После proof точный cookie+CSRF активирует на Backend
только эту provisional session через `/api/v1/auth/activate`; endpoint не
меняет browser cookie, а remembered activation сохраняется в SQLite. До
активации session discovery остаётся гостевым, поэтому crash после login не
восстанавливает незавершённый вход. После успешной continuation удаляются только её
snapshot/own markers: поздний результат A не стирает более новую B. Raw CSRF,
password и cookie в journal не записываются. Backend conditional revoke не
отправляет `Set-Cookie`, поэтому stale response не очищает свежую session.

Browser cache/derived tabs, HTML readability и stale-login recovery входят в
production generation 2026-08-22. Помимо targeted automation пройдены полный
gate, cold backup, versioned deployment и clean-profile Firefox PAM smoke.

На целевом host root равен `/home/user/linuxcnc/nc_files`, а active INI —
`/home/user/linuxcnc/configs/corvuscnc/g540.ini`. Сервис обязан проверить их
совпадение до ready. Catalog service/storage/HTTP automation, real subprocess
SIGKILL recovery, sparse 10 GiB Range, 0/1/N migration/provenance suites и
frontend lint/typecheck/15 files / 103 tests/Vite build прошли. Catalog release
развёрнут на production host; Firefox desktop/mobile production smoke прошёл.
Сквозной keyboard-only integration flow также прошёл; отдельный LAN client,
DHCP reservation, controlled target performance и ручной визуальный QtDragon
walkthrough остаются дополнительной qualification. Точный статус ведётся в
[IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md).

Production generation от 2026-08-22:

- release `/opt/websetupmanager/releases/393ddb68a550`, source commit
  `393ddb68a550eeb5e65cc607032d23d9ab8cc0a1`, binary SHA-256
  `294549740ffc2255720403c474beae5be01c652a8fddad93d020d4ec7b37bd48`;
- cold backup
  `/var/backups/websetupmanager/pre-workbench-393ddb68a550-20260822T125008Z`;
  state/library/program-root archive checksum прошли сверку; прежняя полностью
  restore-проверенная generation сохранена;
- active unit слушает только direct HTTPS `10.0.1.136:443`; TCP/80 отсутствует,
  `/healthz` и `/readyz` возвращают 200;
- schema v5 и catalog migration completed: 2 folders, 2 setups, 4 files,
  2 completed mappings, 4 copied manifests; SQLite integrity/FK, exact
  source↔target size/SHA и idempotent restart проверены; migration 005 создала
  отдельный automatic pre-v5 SQLite backup;
- legacy rows/objects сохранены, temp remnants отсутствуют; LinuxCNC snapshot
  остался `file=""`, `state/mode/interp/exec=1/1/1/2`.
- headless Firefox ESR/WebDriver BiDi прошёл guest, PAM login, ready/catalog/UI
  и logout на production HTTPS. После обнаруженного 0 px code viewport commit
  `18411e613b380c4b73837003b96c949a21661041` заменил editor grid→flex; rerun
  показал highlighted G-code/tree, 37 virtual rows, первую `%` и viewport
  `1030x516` на desktop `1366x768`; также проверен mobile `390x844`.
- commit `12aa6a2adf3c9908a2120c03ed310aa40ac1fecc` исправил возврат focus
  после portal dialog и перехода к строке. Полный keyboard-only App flow и
  87 frontend tests прошли; тот release после restart снова отдал
  `/healthz=200`, `/readyz=200` и guest PAM contract.
- commit `266917d3ed04b3245f7e0f3461128a6d0d0bea0d` внедрил left-tree,
  inline Sheet, direct picker и быстрый 64-КиБ prefix. Финальный production
  Firefox прошёл PAM login, 37 virtual rows, desktop/mobile render и logout;
  current release после restart имеет `NRestarts=0`, health/ready 200.
- release `393ddb68a550` прошла production Firefox PAM login/logout: header
  `Индекс 100%`, Cache Storage 2 chunks + 1 analysis, localStorage 1 complete
  manifest, пустой Toolpath, Tool Table T1/T8/T10 и читаемый sandboxed HTML
  Setup Sheet. После deployment `NRestarts=0`, health/ready 200, TCP/80 закрыт.
- read-only QtDragon audit той же `QFileSystemModel` подтвердил доступную
  цепочку `linuxcnc/nc_files/Импортировано/adssad` и строки `1002.ngc`/
  `1003.ngc`; вкладка File и selection работающего LinuxCNC не изменялись.

Production targets: Linux amd64/arm64. Production build использует cgo и PAM;
на build host нужны PAM headers/library, на target — совместимый PAM runtime.
Development baseline и смена продуктового решения зафиксированы в
[DECISIONS.md](DECISIONS.md); arm64 PAM build/runtime и численные target NFR
требуют отдельной проверки.

## Быстрый старт разработки

Требуются Go 1.26.5+, Node.js 18–22 с npm, C toolchain и PAM development files
(`libpam0g-dev` в Debian/Ubuntu). Из этого каталога:

```bash
make test
make test-pam
make test-race
make lint
make typecheck
make vet
make build
```

`make build` сначала создаёт Vite bundle, затем выполняет
`CGO_ENABLED=1 go build -tags "production pam"` и встраивает `web/dist` в
`build/websetupmanager`. `./scripts/build.sh` выполняет полную
последовательность проверок и PAM production-сборку. Makefile также обнаруживает
закреплённый toolchain репозитория, если `go` отсутствует в `PATH`. Сборка без
PAM допустима для локальной разработки, но намеренно отказывается запускать
remote mode.

Для разработки в двух терминалах используйте только disposable roots и
disposable INI, чей `PROGRAM_PREFIX` точно равен development program root:

```bash
WEB_SETUP_MANAGER_PROGRAM_ROOT=/absolute/development/nc_files \
WEB_SETUP_MANAGER_LINUXCNC_INI=/absolute/development/machine.ini \
make dev-backend \
  LIBRARY_DIR=/absolute/development/library \
  STATE_DIR=/absolute/development/state
make dev-frontend
```

Vite проксирует `/api` на loopback Backend. Не направляйте тесты на production
данные оператора.

## Минимальный production-запуск

Заранее создайте state/legacy-library roots и существующий writable
`PROGRAM_ROOT`, совпадающий с активным LinuxCNC INI. Запускайте binary тем же
non-root пользователем, что и LinuxCNC:

```bash
WEB_SETUP_MANAGER_LIBRARY_DIR=/srv/websetupmanager/library \
WEB_SETUP_MANAGER_STATE_DIR=/var/lib/websetupmanager \
WEB_SETUP_MANAGER_PROGRAM_ROOT=/home/user/linuxcnc/nc_files \
WEB_SETUP_MANAGER_LINUXCNC_INI=/home/user/linuxcnc/configs/corvuscnc/g540.ini \
WEB_SETUP_MANAGER_PROGRAM_ROOT_DISPLAY='~/linuxcnc/nc_files' \
./build/websetupmanager
```

`LIBRARY_DIR` сохраняется как legacy migration/rollback root и не является
destination новых catalog upload. Не направляйте новый upload в `objects/`.

По умолчанию сервис слушает `127.0.0.1:8080`. `GET /healthz` проверяет liveness;
`GET /readyz` дополнительно проверяет SQLite, state и удерживаемый program root,
включая соответствие активному INI.
SIGTERM запрещает новые мутации, завершает graceful HTTP shutdown и закрывает
SQLite. До открытия listener startup последовательно завершает legacy journal и
imports, проверяет legacy content identity, восстанавливает catalog operations и
запускает idempotent legacy→catalog migration. `manual_review` или любая ошибка
блокирует listener. При возобновлении незавершённой migration уже completed
per-source mappings сверяются с provenance/manifest; общий terminal state
`completed` при следующих стартах является no-op. Readiness не означает, что
G-code проверен или загружен в LinuxCNC.

PDF/HTML Setup Sheet загружаются потоково. Общий размер HTML
ограничивают только configured upload limit, filesystem и диск; один
незавершённый HTML token ограничен 1 MiB и проверяется до публикации,
чтобы sanitizer имел bounded memory.

## Catalog delivery evidence и PAM foundation

Catalog-specific automated suites подтверждают folder/setup CRUD, singular
components, direct placement, exact file preconditions, Range/ETag, path/race
protection, durable recovery, actual subprocess SIGKILL, sparse 10 GiB Range и
0/1/N no-data-loss migration с provenance/manual-review. Frontend lint/typecheck,
15 files / 103 tests и Vite production build прошли. Сквозной keyboard-only flow
закрывает login/tree/upload/preview-search/line-jump/logout и focus return;
production visual smoke дополнительно подтверждает layout. Controlled target
performance остаётся отдельной qualification.

Auth/TLS foundation сохранена в catalog release. Production remote login
использует только Linux/PAM account `user` и текущий системный password; его
значение не хранится в приложении и не должно попадать в документацию. Optional
Bearer в текущем deployment не задан.

После интеграции прошли обычные и PAM-tagged backend-команды:

```bash
go test ./... -count=1
go test -race ./... -count=1 -timeout=10m
CGO_ENABLED=1 go test -tags pam ./...
CGO_ENABLED=1 go test -race -tags pam ./...
CGO_ENABLED=1 go vet -tags pam ./...
CGO_ENABLED=1 go build -tags "production pam" ./cmd/websetupmanager
```

Frontend lint и typecheck прошли; Vitest — 17 files/197 tests; Vite production
build прошёл и встроен в Go binary. Полный `scripts/build.sh` также прошёл.
Для финального focus-only release его шаги повторены отдельно; clean detached
worktree выполнил `npm ci`, Vite build и production PAM Go build.
Отдельная non-PAM сборка в remote mode ожидаемо завершилась с exit 1 и
`AUTHENTICATION_UNAVAILABLE`. Текущий amd64 artifact собран Go 1.26.5 с
`CGO_ENABLED=1`, tags `production,pam`, использует системный `libpam.so.0` и
имеет SHA-256
`294549740ffc2255720403c474beae5be01c652a8fddad93d020d4ec7b37bd48` и установлен
как `/opt/websetupmanager/releases/393ddb68a550` из source commit
`393ddb68a550eeb5e65cc607032d23d9ab8cc0a1`.

Catalog `websetupmanager.service` enabled/active от пользователя `user`, слушает
`https://microb.int:443`, а `/healthz` и `/readyz` возвращают 200. Listener или
redirect на port 80 отсутствует. Сертификат текущего host self-signed для
`microb.int`/`10.0.1.136`, поэтому client должен явно доверять ему; это не
заменяет развёртывание доверенного PKI certificate.

Production contract catalog deployment задаёт direct HTTPS на
`10.0.1.136:443` и адрес `https://microb.int/`. Сам Backend не слушает port 80 и не выполняет
HTTP→HTTPS redirect: `http://microb.int/` должен получить connection refused,
если отдельный proxy не настроен. Browser HTTPS-first/HSTS может локально
переписать введённый URL — это не redirect приложения. Login — Linux/PAM account
из `ALLOWED_USER` (на текущем host `user`) и его Linux password; отдельного
Web Setup Manager password нет.

Arm64 PAM build/runtime, численные NFR и controlled 10 GiB browser performance,
trusted-proxy variant и repeated target power-loss остаются отдельной
qualification; production visual smoke их не заменяет.

## Основная конфигурация

`WEB_SETUP_MANAGER_PROGRAM_ROOT` и `WEB_SETUP_MANAGER_LINUXCNC_INI` обязательны
для catalog mode и должны описывать один и тот же canonical `PROGRAM_PREFIX`.
`WEB_SETUP_MANAGER_STATE_DIR` по умолчанию равен
`$XDG_STATE_HOME/websetupmanager` или `~/.local/state/websetupmanager`.
`LIBRARY_DIR` остаётся обязательным на переходный период только для сохранения
legacy objects/migration rollback. Roots должны быть real directories, не
symlink; настройки читаются только при старте.

| Переменная | Default / назначение |
|---|---|
| `WEB_SETUP_MANAGER_PROGRAM_ROOT` | обязательно; canonical LinuxCNC `PROGRAM_PREFIX` |
| `WEB_SETUP_MANAGER_LINUXCNC_INI` | обязательно; active regular INI для проверки root |
| `WEB_SETUP_MANAGER_PROGRAM_ROOT_DISPLAY` | `~/linuxcnc/nc_files`; безопасный operator-facing hint |
| `WEB_SETUP_MANAGER_LIBRARY_DIR` | legacy migration/rollback root; не destination нового upload |
| `WEB_SETUP_MANAGER_STATE_DIR` | SQLite, auth, staging и служебное состояние |
| `WEB_SETUP_MANAGER_LISTEN_ADDRESS` | `127.0.0.1:8080` |
| `WEB_SETUP_MANAGER_LIBRARY_ALIAS` | `Сетапы` |
| `WEB_SETUP_MANAGER_GCODE_EXTENSIONS` | legacy hint; target catalog allowlist соответствует active INI: `.ngc,.nc,.tap` |
| `WEB_SETUP_MANAGER_RECENT_SETUPS_LIMIT` | `30` |
| `WEB_SETUP_MANAGER_MAX_PARALLEL_HEAVY_JOBS` | `2` |
| `WEB_SETUP_MANAGER_ARTIFACT_UPLOAD_LIMIT` | `0`, без application limit |
| `WEB_SETUP_MANAGER_IMPORT_TOTAL_LIMIT` | `0`, без application limit |
| `WEB_SETUP_MANAGER_REQUIRE_SETUP_SHEET_FOR_READY` | legacy only; readiness workflow отсутствует в catalog UI |
| `WEB_SETUP_MANAGER_ARTIFACT_FILE_MODE` | `0640`, execution bits запрещены |
| `WEB_SETUP_MANAGER_READ_HEADER_TIMEOUT` | `10s` |
| `WEB_SETUP_MANAGER_READ_TIMEOUT` | `30s`, sliding request-read/response-write I/O idle timeout |
| `WEB_SETUP_MANAGER_IDLE_TIMEOUT` | `2m` |
| `WEB_SETUP_MANAGER_MAX_HEADER_BYTES` | `16384` |
| `WEB_SETUP_MANAGER_SHUTDOWN_TIMEOUT` | `15s` |
| `WEB_SETUP_MANAGER_IDEMPOTENCY_TTL` | `24h` |
| `WEB_SETUP_MANAGER_DELETE_CONFIRMATION_TTL` | `5m` |
| `WEB_SETUP_MANAGER_IMPORT_SESSION_EXPIRY` | `24h` |
| `WEB_SETUP_MANAGER_RECONCILE_INTERVAL` | `1m` |
| `WEB_SETUP_MANAGER_REMOTE_ACCESS` | `false`; включает защищённый remote mode |
| `WEB_SETUP_MANAGER_ALLOWED_USER` | нет; обязательный non-root Linux user для remote mode |
| `WEB_SETUP_MANAGER_PAM_SERVICE` | `websetupmanager` |
| `WEB_SETUP_MANAGER_AUTH_IDLE_TIMEOUT` | `30m` |
| `WEB_SETUP_MANAGER_AUTH_ABSOLUTE_TIMEOUT` | `12h` |
| `WEB_SETUP_MANAGER_AUTH_REMEMBER_TIMEOUT` | `720h` (30 суток) |
| `WEB_SETUP_MANAGER_AUTH_CONCURRENCY` | `4` |
| `WEB_SETUP_MANAGER_LOGIN_ATTEMPTS` | `5` на окно/IP и имя |
| `WEB_SETUP_MANAGER_LOGIN_WINDOW` | `10m` |
| `WEB_SETUP_MANAGER_AUTH_SESSION_CAPACITY` | `128` |
| `WEB_SETUP_MANAGER_REMOTE_AUTH_TOKEN` | нет; optional Bearer для automation, минимум 32 символа |
| `WEB_SETUP_MANAGER_TLS_CERT_FILE`, `WEB_SETUP_MANAGER_TLS_KEY_FILE` | нет; direct TLS pair |
| `WEB_SETUP_MANAGER_TRUSTED_TLS_PROXY` | `false` |

Remote mode требует `WEB_SETUP_MANAGER_REMOTE_ACCESS=true`, настроенный
`WEB_SETUP_MANAGER_ALLOWED_USER`, PAM-capable production binary и TLS
certificate/key либо явно доверенный TLS proxy. Процесс обязан работать именно
от `ALLOWED_USER`. Браузер показывает обычную форму входа и использует Linux
пароль этого пользователя; optional Bearer secret нужен только API automation.
В loopback local mode вход не требуется. Полный список, PAM policy, systemd
example, TLS/auth, backup/restore, upgrade, recovery, GC и incident runbook
находятся в [OPERATOR_GUIDE.ru.md](OPERATOR_GUIDE.ru.md).

Перед первым catalog deployment остановите legacy service и сделайте одну cold
generation из `STATE_DIR`, legacy `LIBRARY_DIR`, всего `PROGRAM_ROOT`, active INI
и unit/env. Rollback после schema/data migration выполняется только
восстановлением всей этой generation вместе со старым binary; смешивать SQLite и
program files разных backup generation нельзя. Пошаговая процедура и поля для
финального live evidence находятся в
[OPERATOR_GUIDE.ru.md](OPERATOR_GUIDE.ru.md) и
[MIGRATION_PLAN.md](MIGRATION_PLAN.md).

## Документы

- [PRODUCT_REQUIREMENTS.ru.md](PRODUCT_REQUIREMENTS.ru.md) — актуальные
  `CAT-P0-*` и `CAT-AC-*`;
- [MIGRATION_PLAN.md](MIGRATION_PLAN.md) — no-data-loss переход к
  `PROGRAM_PREFIX`;
- [functional-requirements.ru.md](functional-requirements.ru.md) — архивный
  multi-program/validation baseline;
- [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) — актуальный план и свёрнутая
  историческая матрица 221 P0/AC-01–AC-20;
- [DECISIONS.md](DECISIONS.md) — безопасные решения неоднозначностей;
- [PROGRESS_LOG.md](PROGRESS_LOG.md) — проверки по этапам;
- [OPERATOR_GUIDE.ru.md](OPERATOR_GUIDE.ru.md) — эксплуатация и восстановление.
