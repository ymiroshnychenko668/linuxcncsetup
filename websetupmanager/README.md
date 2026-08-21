# Web Setup Manager

Web Setup Manager — компактный каталог и upload-инструмент для
LinuxCNC-станка. Основная сущность — **Setup** с не более чем одной G-code
программой и одной PDF/HTML Setup Sheet. Setup может быть неполным и
группируется по реальным каталогам внутри настроенного LinuxCNC
`PROGRAM_PREFIX`.

Основной экран следует плотному UX-паттерну Visual Studio Code: viewer G-code/
Setup Sheet слева, дерево folders/setups справа. Приложение показывает, в каком
каталоге QtDragon найти загруженную программу. Оно не проверяет технологическую
готовность, не загружает и не исполняет G-code в LinuxCNC.

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
- компактный left-viewer/right-tree интерфейс с понятным destination;
- Web Worker индекс/поиск G-code, PDF.js и sandboxed sanitized HTML viewer;
- revision/version conflicts, audit, idempotency и recovery собственных temp;
- в remote mode — Linux PAM login, защищённые browser sessions, logout,
  «Запомнить меня», per-session CSRF и ограничение попыток входа; встроенного
  логина или пароля нет.

На целевом host root равен `/home/user/linuxcnc/nc_files`, а active INI —
`/home/user/linuxcnc/configs/corvuscnc/g540.ini`. Сервис обязан проверить их
совпадение до ready. Старый deployed artifact от 2026-08-20 доказал PAM/TLS, но
не считается acceptance evidence новой catalog/storage/UI модели; её текущий
статус ведётся в [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md).

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
SQLite. До открытия listener startup очищает только собственные stale catalog
temp, восстанавливает незавершённые операции и проверяет root/INI/file identity.
Legacy object reconciliation/GC сохраняется только для migration/rollback и не
публикует новые catalog files. Readiness не означает, что G-code проверен или
загружен в LinuxCNC.

PDF/HTML Setup Sheet загружаются потоково. Общий размер HTML
ограничивают только configured upload limit, filesystem и диск; один
незавершённый HTML token ограничен 1 MiB и проверяется до публикации,
чтобы sanitizer имел bounded memory.

## Проверенная PAM-сборка старого baseline

Следующее evidence подтверждает auth/TLS foundation, а не новую catalog-модель,
direct `PROGRAM_ROOT` publication или переработанный UI. После catalog transition
вся последовательность должна быть повторена integrated.

После интеграции прошли обычные и PAM-tagged backend-команды:

```bash
go test ./... -count=1
go test -race ./... -count=1 -timeout=10m
CGO_ENABLED=1 go test -tags pam ./...
CGO_ENABLED=1 go test -race -tags pam ./...
CGO_ENABLED=1 go vet -tags pam ./...
CGO_ENABLED=1 go build -tags "production pam" ./cmd/websetupmanager
```

Frontend lint и typecheck прошли; Vitest — 12 files/83 tests; Vite production
build прошёл и встроен в Go binary. Полный `scripts/build.sh` также прошёл.
Отдельная non-PAM сборка в remote mode ожидаемо завершилась с exit 1 и
`AUTHENTICATION_UNAVAILABLE`. Установленный amd64 artifact собран Go 1.26.5 с
`CGO_ENABLED=1`, tags `production,pam`, использует системный `libpam.so.0` и
имеет SHA-256
`5df67ec084ec30e0f253f6fd38f565adbe9e4eb8656edc180f3fa2454be8469d`.

На текущем host (2026-08-20) `websetupmanager.service` установлен как enabled и active от
пользователя `user`, слушает `https://microb.int:443`; `/healthz` и `/readyz`
возвращают 200. Live direct-TLS flow подтвердил guest session, защищённые
capabilities, реальный PAM login/logout и «Запомнить меня» через graceful
service restart с последующим logout. Сертификат текущего host self-signed для
`microb.int`/`10.0.1.136`, поэтому client должен явно доверять ему; это не
заменяет развёртывание доверенного PKI certificate.

Arm64 PAM build/runtime, численные NFR на целевом LinuxCNC-компьютере,
controlled-browser viewer/keyboard walkthrough, trusted-proxy variant и
repeated target power-loss остаются отдельной qualification; live host smoke их
не заменяет.

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
