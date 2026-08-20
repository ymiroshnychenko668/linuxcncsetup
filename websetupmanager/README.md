# Web Setup Manager

Web Setup Manager — локальная offline-библиотека технологических сетапов для
LinuxCNC-станка. Основная сущность — **Setup**: одна или несколько G-code
программ, одна общая PDF/HTML Setup Sheet, метаданные, revision и состояние
готовности. Это не файловый менеджер. Выбор текущего сетапа не исполняет G-code,
не запускает LinuxCNC и не копирует файлы в его конфигурацию.

## Что входит в production binary

- один непривилегированный Go-процесс и embedded SQLite;
- встроенная production-сборка React/TypeScript, Node.js на станке не нужен;
- доменный `/api/v1` без `/fs` и без физических путей;
- immutable managed object store, streaming upload, Range/ETag preview;
- библиотека/карточка сетапа, import, validation, current setup, archive/delete;
- Web Worker индекс/поиск G-code, PDF.js и sandboxed sanitized HTML viewer;
- journal, audit, idempotency, startup recovery, reconcile и reference-safe GC;
- атомарный commit доменного результата вместе с terminal persistent job; для
  upload в тот же commit входит run-idempotency result и content digest.
- в remote mode — Linux PAM login, защищённые browser sessions, logout,
  «Запомнить меня», per-session CSRF и ограничение попыток входа; встроенного
  логина или пароля нет.

Production targets: Linux amd64/arm64. Production build использует cgo и PAM;
на build host нужны PAM headers/library, на target — совместимый PAM runtime.
Development baseline зафиксирован в [DECISIONS.md](DECISIONS.md); arm64 PAM
build/runtime и численные target NFR требуют проверки на целевом станке.

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

Для разработки в двух терминалах используйте только disposable roots:

```bash
make dev-backend \
  LIBRARY_DIR=/absolute/development/library \
  STATE_DIR=/absolute/development/state
make dev-frontend
```

Vite проксирует `/api` на loopback Backend. Не направляйте тесты на production
данные оператора.

## Минимальный production-запуск

Заранее создайте два существующих, writable, абсолютных, disjoint каталога и
запускайте binary отдельным non-root пользователем:

```bash
WEB_SETUP_MANAGER_LIBRARY_DIR=/srv/websetupmanager/library \
WEB_SETUP_MANAGER_STATE_DIR=/var/lib/websetupmanager \
./build/websetupmanager
```

По умолчанию сервис слушает `127.0.0.1:8080`. `GET /healthz` проверяет liveness;
`GET /readyz` дополнительно проверяет SQLite и оба удерживаемых storage roots.
SIGTERM запрещает новые мутации, завершает graceful HTTP shutdown и закрывает
SQLite. До открытия listener startup выполняет journal/import recovery и
быструю проверку identity всех managed objects. Полная SHA-256 сверка,
expired cleanup и reference-safe GC стартуют в фоне после listener; readiness
при этом отражает доступность SQLite и удерживаемых roots, а не ожидание
первого фонового full scrub.

PDF/HTML Setup Sheet загружаются потоково. Общий размер HTML
ограничивают только configured upload limit, filesystem и диск; один
незавершённый HTML token ограничен 1 MiB и проверяется до публикации,
чтобы sanitizer имел bounded memory.

## Проверенная PAM-сборка и текущий host

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

`WEB_SETUP_MANAGER_LIBRARY_DIR` обязателен. `WEB_SETUP_MANAGER_STATE_DIR` по
умолчанию равен `$XDG_STATE_HOME/websetupmanager` или
`~/.local/state/websetupmanager`. Roots должны быть реальными каталогами, не
symlink, не совпадать и не быть вложенными друг в друга. Настройки читаются
только при старте.

| Переменная | Default / назначение |
|---|---|
| `WEB_SETUP_MANAGER_LISTEN_ADDRESS` | `127.0.0.1:8080` |
| `WEB_SETUP_MANAGER_LIBRARY_ALIAS` | `Сетапы` |
| `WEB_SETUP_MANAGER_GCODE_EXTENSIONS` | `.gcode,.nc,.ngc,.tap,.cnc` |
| `WEB_SETUP_MANAGER_RECENT_SETUPS_LIMIT` | `30` |
| `WEB_SETUP_MANAGER_MAX_PARALLEL_HEAVY_JOBS` | `2` |
| `WEB_SETUP_MANAGER_ARTIFACT_UPLOAD_LIMIT` | `0`, без application limit |
| `WEB_SETUP_MANAGER_IMPORT_TOTAL_LIMIT` | `0`, без application limit |
| `WEB_SETUP_MANAGER_REQUIRE_SETUP_SHEET_FOR_READY` | `false` |
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

- [functional-requirements.ru.md](functional-requirements.ru.md) — normative P0;
- [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) — архитектура, API и
  трассировка 221 P0 ID/AC-01–AC-20;
- [DECISIONS.md](DECISIONS.md) — безопасные решения неоднозначностей;
- [PROGRESS_LOG.md](PROGRESS_LOG.md) — проверки по этапам;
- [OPERATOR_GUIDE.ru.md](OPERATOR_GUIDE.ru.md) — эксплуатация и восстановление.
