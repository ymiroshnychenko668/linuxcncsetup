# Web Setup Manager — decisions

Неоднозначности требований фиксируются здесь до реализации. Решения выбирают
минимальную безопасную P0-модель и не добавляют файловый менеджер или P1/P2.

## D-001: production baseline

Первая сборка поддерживает Linux amd64/arm64, Go 1.26.5+, Node используется
только при сборке. Зафиксированный development/первичный acceptance baseline:
Debian 13.5 Trixie, kernel `6.12.95+deb13-rt-amd64` PREEMPT_RT, x86_64 и ext4.
Filesystem должна поддерживать atomic rename, fsync, sparse files и 64-bit
offsets. `openat2` с требуемыми resolve flags проверен реальным syscall;
root-anchored `*at` fallback покрывается тем же suite. Linux arm64 production
поддерживается как target, но новая cgo/PAM-сборка и runtime qualification
требуют arm64 toolchain/sysroot с libpam либо нативный arm64-хост; прежний
non-PAM cross-build не является evidence для текущей production-сборки.

## D-002: storage roots

`library_dir` и `state_dir` должны существовать, быть абсолютными реальными
каталогами, быть доступны текущему непривилегированному пользователю и не могут
совпадать или быть вложены друг в друга. Backend никогда не выбирает родительский
fallback. Это минимально и однозначно выполняет изоляцию state от content API.

## D-003: content-addressed immutable objects

Артефакт ссылается на неизменяемый SHA-256 storage object. Дубликаты могут
разделять object, но получают новые setup/artifact IDs; любая замена создаёт
новый object и меняет только одну логическую ссылку. Физический key не является
API field.

## D-004: import readiness

Успешный import публикует один целостный `draft`. Он не становится `ready`
автоматически: оператор запускает отдельную revision-bound validation. Это
следует общему правилу, что новый/изменённый setup требует проверки, и не
выдаёт распознавание типа файла за проверку LinuxCNC.

## D-005: local security and optional remote access

Default listener — `127.0.0.1:8080`. Local mode использует same-origin Host /
Origin checks и случайный CSRF token, читаемый только через same-origin
capabilities; login в этом режиме implicit. Remote mode разрешён только при
явной настройке, Linux PAM-capable production binary, одном настроенном non-root
`AllowedUser`, совпадающем с effective process user, и TLS 1.3 (или явно
доверенном TLS proxy); иначе startup завершается ошибкой.

SPA и узкие auth endpoints доступны до входа, чтобы показать собственную форму
вместо HTTP Basic prompt. PAM выполняет authentication и account-management
check только настроенного аккаунта и возвращает одинаковую внешнюю ошибку для
неверного имени/пароля. Успешный вход выдаёт случайную opaque
`__Host-websetupmanager_session` cookie (`Secure`, `HttpOnly`,
`SameSite=Strict`, `Path=/`); browser mutation требует точный HTTPS
Host/Origin и отдельный session CSRF. Обычные sessions живут только в памяти с
idle/absolute deadline. «Запомнить меня» имеет fixed deadline и сохраняет в
SQLite только SHA-256 token hash, CSRF, имя, времена и deployment scope — не
password и не raw token. Login ограничен по IP/имени, PAM concurrency и общей
session capacity. Случайный Bearer token длиной не менее 32 символов остаётся
необязательным credential для automation; HTTP Basic отсутствует.

## D-006: document viewers

HTML очищается allowlist sanitizer на backend и показывается только в iframe с
пустым `sandbox`; content response имеет отдельный максимально строгий CSP.
PDF отображается через локально собранный PDF.js canvas viewer без annotation /
JavaScript/action layer и без внешних переходов. Ни один viewer не получает
absolute path, cookie или API token.

## D-007: numerical NFR evidence

Автоматические benchmarks проверяют асимптотику, 10k-list query и sparse large
file behavior в development environment. Числа реального LinuxCNC компьютера
будут отдельно записаны как target acceptance evidence; код не будет объявлять
неизмеренное значение подтверждённым.

## D-008: SQLite implementation

SQLite реализован pure-Go `modernc.org/sqlite` v1.57.0 и не требует системной
SQLite library; он предоставляет online backup API для необратимых migrations.
Подключение включает WAL, foreign keys, busy timeout, synchronous FULL и process
lock. Однако production binary в целом намеренно собирается с
`CGO_ENABLED=1 -tags "production pam"` и зависит от системного libpam: это
необходимо для нормальной OS-аутентификации remote mode. Сборка без PAM может
использоваться для local development, но remote startup fail-closed.

Migration `002_auth_sessions.sql` добавляет только remembered browser-session
index. Первичный ключ — hex SHA-256 от opaque cookie token; raw token и password
никогда не попадают в SQLite. Username, CSRF, creation/expiry и deployment scope
нужны для проверки, logout и invalidation при изменении security-relevant
deployment state.

## D-009: names and collisions

Setup name ограничено 200 Unicode code points/800 UTF-8 bytes; artifact basename
— 255 code points и максимум 255 bytes. NUL/control, separators, volume paths,
`.`/`..`, trailing dot/space отвергаются. Collision key — NFC + Unicode
case-fold; display name хранится отдельно от внутреннего object key.

## D-010: TTL and irreversible cancellation

Idempotency result хранится 24 часа. Delete confirmation token живёт 5 минут и
привязан к setup/revision/name. После atomic DB commit job является terminal;
поздняя отмена возвращает стабильный terminal result и не выполняет опасную
компенсацию.

## D-011: current setup after mutation

Пользовательская или внешняя мутация не снимает current автоматически. Ссылка
остаётся, а UI показывает блокирующий `draft`/`attention`, пока оператор явно не
снимет current, не выберет другой или не провалидирует новую revision.

## D-012: operation jobs and external reconciliation

Import, prepared add/replace/Setup-Sheet upload, validation, duplicate и restore
имеют persistent job record; короткие metadata/state mutations остаются
синхронными. Permanent delete атомарно удаляет только доменные ссылки, а
физическую очистку выполняет reference-safe GC. Identity проверяется при
content/validation/mutation, на startup и ограниченным периодическим scanner;
неизвестный внешний object никогда не наследует artifact ID только из-за
совпадения display name.

## D-013: UI client identity

Browser создаёт один случайный client ID в localStorage. Содержательное state
хранится SQLite по `library_id + client_id`; только визуальные размеры могут
оставаться локальными. Отсутствующий setup восстанавливается как empty selection,
а не заменяется объектом с тем же именем.

## D-014: large-file fixtures

Sparse 10 GiB real file проверяет 64-bit HEAD/Range/RSS, но holes содержат NUL и
не являются валидным G-code. Поэтому preview/search отдельно тестируются на
generator-backed логическом ASCII stream с match у конца; эти сценарии нельзя
ошибочно объединять.

## D-015: sliding network I/O idle timeout

`WEB_SETUP_MANAGER_READ_TIMEOUT` исторически назван read timeout, но в P0
трактуется как общий sliding timeout отсутствия network I/O progress: для чтения
request body и записи response. Перед каждым body `Read` и response `Write`
Backend устанавливает новый соответствующий connection deadline; каждый
успешный блок сдвигает его дальше. Поэтому многогигабайтная загрузка или Range
response с постоянным прогрессом может длиться дольше значения настройки, а
зависший upload или slow/non-reading client освобождается после одного интервала.
`http.Server.ReadTimeout` намеренно равен нулю: его абсолютная семантика
конфликтует с потоковыми операциями. `ReadHeaderTimeout` остаётся отдельным;
также действуют connection `IdleTimeout`, header limit, upload/import limits и
лимит тяжёлых jobs.

## D-016: durable journal checkpoints are collapsed around the DB commit

Схема допускает состояния `intent`, `storage_applied`, `database_applied` и
terminal states. Публикация immutable storage object и reservation фиксируются
до доменной транзакции (`intent → storage_applied`). Логическая мутация,
audit-событие и перевод journal в terminal state выполняются одной SQLite
transaction. Поэтому обычный успешный путь не обязан оставлять наблюдаемую
durable запись `database_applied`: crash до commit не публикует revision, crash
после commit уже видит terminal row. Startup recovery принимает также legacy и
искусственно восстановленные промежуточные состояния и переводит неоднозначный
остаток в `conflict`; GC учитывает любую незавершённую reservation. Это безопасное
схлопывание DB step, а не утверждение атомарности SQLite с filesystem.

## D-017: development evidence is not target qualification

Development evidence получено на Debian 13.5, Linux
`6.12.95+deb13-rt-amd64` PREEMPT_RT, ext4, Go 1.26.5, Node 20.19.2. После auth
integration полный Go test/race/vet прошёл с `CGO_ENABLED=1 -tags pam`, frontend
lint/typecheck и 12 files/83 tests прошли, а production amd64 artifact собран с
tags `production,pam`; untagged Go/race и полный `scripts/build.sh` также прошли.
Non-PAM remote binary отдельно подтвердил fail-closed startup. На этом же host
direct TLS 1.3 проверен с реальным PAM login/logout и hash-only remembered
session через graceful restart; systemd service enabled/active. Это закрывает
конкретный integrated development/host gate, но не переносится автоматически на
другую машину или transport termination mode.

Headless Firefox ранее выполнил только same-origin asset/API smoke и screenshot
loading-state; подходящей controlled browser automation и реального
LinuxCNC-станка нет. Поэтому численные NFR, полный keyboard/visual walkthrough,
измерение browser memory/DOM на 10 GiB, активное содержимое PDF/HTML под
наблюдением сети, client trust для production certificate, trusted-proxy
variant, arm64 PAM build/runtime и повторяемый import/replace/duplicate
power-loss остаются отдельными target scenarios. Actual SIGKILL для
archive/delete/current уже проверен в development environment. Документация не
называет эти внешние проверки пройденными.

## D-018: operator backup, recovery and GC boundaries

Публичный API и UI не предоставляют произвольные backup/restore/GC операции.
Согласованная операторская резервная копия P0 — cold copy одновременно
`state_dir` и `library_dir` при остановленном процессе. Восстановление выполняется
только при остановленном Backend в подготовленные disjoint roots с тем же
владельцем; ручная замена отдельных SQLite/object-файлов запрещена. Reconcile,
очистка expired import/idempotency/delete-confirmation records и reference-safe
GC запускаются в background сразу после открытия listener и затем периодически;
до listener выполняются только
обязательная recovery и bounded identity-проверка ссылок. Частая проверка
identity не хэширует гигабайтные объекты; полная SHA-256 сверка выполняется при
первом background-проходе и раз в сутки. Оператор может безопасно инициировать
полный цикл рестартом и дождаться successful reconcile log;
`/readyz` не ожидает этот background-проход. Внутренние объекты нельзя
удалять вручную.
Cold copy может изменить inode/ctime. Такой объект rebind-ится к новой физической
версии только после полного совпадения ожидаемого SHA-256. Первый rebind-проход
оставляет Setup `attention`; в любом случае Setup не возвращается в `ready`
без явной повторной validation.

## D-019: HTML streaming and structural complexity

Общий размер самостоятельного HTML не ограничивается константой приложения:
upload/staging и sanitized response потоковые. Чтобы один незавершённый text или
attribute token не нарушил memory budget, один HTML token ограничен 1 MiB.
Тот же tokenizer и limit выполняются до публикации и при показе; неподдерживаемая
структура получает `INVALID_CONTENT`. Sanitized response читает accepted
неизменную версию 512 KiB Range-блоками и не буферизует целый input/output.
Документы произвольного общего размера поддерживаются при bounded tokens и в
пределах configured/storage limits.

## D-020: asynchronous domain result and terminal job are one commit

Успешная доменная часть prepared upload, validation, duplicate и restore не
фиксируется отдельно от terminal job. Setup/revision или validation result,
journal/audit, terminal job state/result/progress и применимая внутренняя
idempotency запись выполняются одной SQLite transaction. Для streaming upload
та же transaction завершает внешний `runUploadJob` claim и сохраняет точные
content digests; error/cancel terminal job и внешний claim также фиксируются
вместе. Import изначально применяет тот же принцип к setup, session, job,
journal, audit и commit-idempotency.

Поэтому startup не пытается эвристически повышать прерванный job до success:
до commit доменная мутация невидима и активный job получает
`PROCESS_INTERRUPTED`, после commit job уже имеет неизменяемый terminal result.
Поздняя отмена не маскирует commit, который успел победить; если terminal job
невозможно сериализовать или записать, вся доменная transaction откатывается.
Это проверяют `TestCommittedJobResultsAndUploadClaimsSurviveRestart`,
`TestAtomicJobMarshalFailureRollsBackDomainTransaction` и queued-validation
cancellation regression.
