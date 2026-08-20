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
binary cross-build проходит, но runtime qualification требует arm64-хост.

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
capabilities. Не-loopback bind разрешён только при явной remote настройке,
Bearer token длиной не менее 32 символов и TLS 1.3 (или явно доверенном TLS
proxy); иначе startup завершается ошибкой.

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

Используется pure-Go `modernc.org/sqlite` v1.57.0. Это даёт один статический
amd64/arm64 binary без системного SQLite/CGO и одновременно предоставляет
SQLite online backup API для необратимых migrations. Подключение включает WAL,
foreign keys, busy timeout, synchronous FULL и process lock.

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

Import, add/replace, validation, duplicate и permanent delete всегда имеют job
record; короткие metadata/state mutations остаются синхронными. Identity
проверяется при content/validation/mutation, на startup и ограниченным
периодическим scanner; неизвестный внешний object никогда не наследует artifact
ID только из-за совпадения display name.

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
