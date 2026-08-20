# Web Setup Manager

Web Setup Manager is a local, offline setup library for a LinuxCNC workstation.
The primary entity is a **Setup**: one or more G-code programs, one optional
setup-wide PDF or standalone HTML Setup Sheet, metadata, revision and readiness
state. It is not a filesystem browser and selecting the current setup never
executes G-code or starts LinuxCNC.

## Development baseline

- Debian 13.5 / Linux 6.12 PREEMPT_RT / ext4 (current development host);
- production targets Linux amd64 and arm64;
- Go 1.26.5 or newer;
- Node.js 18–22 and npm (build/test only; Node is not needed at runtime).

The repository host currently provides Go at
`/opt/remoteterminal/tools/go-1.26.5-5c2c3b16caef/bin/go`; the Makefile detects
that pinned toolchain when `go` is not on `PATH`.

## Configuration

`WEB_SETUP_MANAGER_LIBRARY_DIR` is required and must name an existing managed
library directory. `WEB_SETUP_MANAGER_STATE_DIR` defaults to
`$XDG_STATE_HOME/websetupmanager` or `~/.local/state/websetupmanager`. The roots
must be absolute, real, writable, disjoint and non-nested.

Important optional settings:

| Environment variable | Default |
|---|---|
| `WEB_SETUP_MANAGER_LISTEN_ADDRESS` | `127.0.0.1:8080` |
| `WEB_SETUP_MANAGER_LIBRARY_ALIAS` | `Сетапы` |
| `WEB_SETUP_MANAGER_GCODE_EXTENSIONS` | `.gcode,.nc,.ngc,.tap,.cnc` |
| `WEB_SETUP_MANAGER_RECENT_SETUPS_LIMIT` | `30` |
| `WEB_SETUP_MANAGER_MAX_PARALLEL_HEAVY_JOBS` | `2` |
| `WEB_SETUP_MANAGER_ARTIFACT_UPLOAD_LIMIT` | `0` (no application limit) |
| `WEB_SETUP_MANAGER_IMPORT_TOTAL_LIMIT` | `0` (no application limit) |
| `WEB_SETUP_MANAGER_REQUIRE_SETUP_SHEET_FOR_READY` | `false` |
| `WEB_SETUP_MANAGER_ARTIFACT_FILE_MODE` | `0640` (never executable) |

Non-loopback binding fails closed unless remote access is explicitly enabled
with a 32+ character authentication token and either local TLS certificate/key
or an explicitly trusted TLS proxy. There is no insecure remote mode.

## Build and test

From this directory:

```bash
make test
make test-race
make lint
make typecheck
make vet
make build
```

`make build` runs a reproducible Vite build first, then embeds `web/dist` in the
single production Go binary at `build/websetupmanager`. A clean backend test
does not require Node: the non-production Go build embeds a small fallback
page. `./scripts/build.sh` runs the complete ordinary test/build sequence.

For local development, use two terminals:

```bash
make dev-backend LIBRARY_DIR=/absolute/development/library \
  STATE_DIR=/absolute/development/state
make dev-frontend
```

The Vite server proxies `/api` to the loopback backend. Use disposable
development roots; never point tests at operator production data.

## Runtime

```bash
WEB_SETUP_MANAGER_LIBRARY_DIR=/srv/websetupmanager/library \
WEB_SETUP_MANAGER_STATE_DIR=/var/lib/websetupmanager \
./build/websetupmanager
```

Run as a dedicated non-root user with write access only to these two roots.
`GET /healthz` reports process liveness. `GET /readyz` additionally checks
SQLite and both held storage roots. SIGTERM stops accepting mutations, performs
graceful HTTP shutdown and closes SQLite.

Architecture, coverage and active implementation status are maintained in
`IMPLEMENTATION_PLAN.md`; security/product decisions are in `DECISIONS.md` and
short milestone results are in `PROGRESS.md`.
