# Web Setup Manager — decisions

Неоднозначности требований фиксируются здесь до реализации. Решения выбирают
минимальную безопасную P0-модель и не добавляют файловый менеджер или P1/P2.

## D-001: production baseline

Первая сборка поддерживает Linux amd64/arm64, Go 1.26.5+, Node используется
только при сборке. Базовая целевая ОС — Debian 12/LinuxCNC с ext4 или другой
локальной POSIX filesystem, поддерживающей atomic rename, fsync, sparse files и
64-bit offsets. На Linux предпочитается `openat2`; root-anchored `*at` fallback
покрывается тем же security suite.

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
capabilities. Не-loopback bind будет разрешён только при явной remote настройке
с backend authentication и TLS (или явно доверенным TLS proxy); иначе startup
завершается ошибкой. Детали конфигурации будут закреплены вместе с config tests.

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
