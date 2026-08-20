# Web Setup Manager — progress log

## 2026-08-20 — discovery and planning

- Полностью прочитан `functional-requirements.ru.md` (859 строк).
- Проверены структура репозитория, отсутствие применимого root `AGENTS.md`,
  существующие Go/React/Vite/Vitest/Testing Library patterns и Git state.
- Создана ветка `codex/web-setup-manager` от актуального `origin/main`.
- Зафиксированы архитектура, модель данных, API, storage/test strategy и
  исходная матрица всех 221 P0 ID и AC-01–AC-20.
- Проверки этапа: documentation review; implementation tests ещё не применимы.

## 2026-08-20 — stage 1: application foundation

- Создан отдельный Go 1.26.5 module, static pure-Go SQLite production binary,
  graceful signal shutdown и production-tag embedding Vite output.
- Реализованы строгая конфигурация, disjoint root validation, stable library ID,
  `/healthz`, `/readyz`, remote fail-closed/auth/TLS policy, Host/Origin/CSRF и
  application CSP/security headers.
- Добавлены полная initial SQLite schema, checksummed transactional migrations,
  WAL/FK/FULL/busy timeout, process lock, quick-check, online backup и startup
  recovery.
- Добавлен root-FD/openat2 storage foundation: exclusive streaming staging,
  SHA-256 immutable publication, atomic rename, free-space/limit checks,
  regular-file/version/symlink/special-file protection and cleanup.
- Добавлены domain DTOs/errors/IDs/NFC-casefold names/revision transitions and
  bounded streaming ASCII/UTF-8/BOM G-code validation.
- Создан React/TypeScript/Vite shell setup library/current setup, typed API
  client, offline/unavailable states and accessible focus-trapping Modal.
- Green checks: `go test ./...`, `go vet ./...`, `go test -race ./...`;
  frontend lint/typecheck and 12/12 Vitest tests; Vite production build;
  static Linux amd64 build; Linux arm64 cross-build; `git diff --check`;
  local production runtime returned 200 for health/readiness/capabilities/SPA
  and exited cleanly on SIGINT.
