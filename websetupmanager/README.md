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

Production targets: Linux amd64/arm64. Development baseline зафиксирован в
[DECISIONS.md](DECISIONS.md); arm64 runtime и численные target NFR требуют
проверки на целевом станке.

## Быстрый старт разработки

Требуются Go 1.26.5+ и Node.js 18–22 с npm. Из этого каталога:

```bash
make test
make test-race
make lint
make typecheck
make vet
make build
```

`make build` сначала создаёт Vite bundle, затем встраивает `web/dist` в один
binary `build/websetupmanager`. `./scripts/build.sh` выполняет обычную полную
последовательность проверок и сборки. Makefile также обнаруживает закреплённый
toolchain репозитория, если `go` отсутствует в `PATH`.

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

## Проверенный development build

На финальном tree прошли `gofmt`, `git diff --check`, `go mod tidy -diff`,
`go vet ./...`, обычный и race Go suites, frontend lint/typecheck, 68/68 Vitest,
Vite production build, статические amd64/arm64 builds и production smoke с
health/readiness, embedded assets, CSRF mutation, HEAD, отсутствием `/fs` и
graceful signal shutdown. SHA-256 binaries:

- amd64: `f79750f8e8cf06313e76cddb6cffb0898badd9ce50f9c8092a8538064a486f67`;
- arm64 cross-build: `fd8a4f2d086ca1f86dc2bcf8495804ff0fe87a30e72575692512065ef0e39008`.

Arm64 runtime, численные NFR на целевом LinuxCNC-компьютере, controlled-browser
viewer/keyboard walkthrough, real TLS/proxy deployment и repeated target
power-loss остаются отдельной qualification; cross-build и headless smoke их не
заменяют.

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

Не-loopback bind разрешён только при явном
`WEB_SETUP_MANAGER_REMOTE_ACCESS=true`, token длиной не менее 32 символов и
TLS certificate/key либо явно доверенном TLS proxy. Небезопасного remote mode
нет. Полный список, systemd example, TLS/auth, backup/restore, upgrade, recovery,
GC и incident runbook находятся в
[OPERATOR_GUIDE.ru.md](OPERATOR_GUIDE.ru.md).

## Документы

- [functional-requirements.ru.md](functional-requirements.ru.md) — normative P0;
- [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) — архитектура, API и
  трассировка 221 P0 ID/AC-01–AC-20;
- [DECISIONS.md](DECISIONS.md) — безопасные решения неоднозначностей;
- [PROGRESS_LOG.md](PROGRESS_LOG.md) — проверки по этапам;
- [OPERATOR_GUIDE.ru.md](OPERATOR_GUIDE.ru.md) — эксплуатация и восстановление.
