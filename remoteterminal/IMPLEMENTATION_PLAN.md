# Remote Terminal Implementation Plan

## Status

The agreed design is implemented in this repository. The source tree contains the Go backend, React/TypeScript frontend, tmux/ttyd terminal manager, tmux/code-server workspace manager, authenticated proxies, setup-menu integration, Ansible source-build/deployment and uninstall role, systemd/PAM/TLS templates, and automated tests.

Source-level unit tests and strictly opt-in real tmux/ttyd and tmux/code-server lifecycle tests are available. Clean AMD64 Debian/LinuxCNC installation, interactive PAM behavior, supported-browser clipboard/input behavior, second-run idempotence, reboot, and uninstall remain target-machine acceptance work. Nothing in this document claims those target-only checks have run.

This document is retained as the architecture, security contract, implementation record, and acceptance plan. Future choices are identified separately from current version 1 defaults.

## Confirmed scope

- AMD64 Debian/LinuxCNC systems only for the first version.
- Install and build from source code on the target machine.
- Go backend.
- React and TypeScript frontend.
- tmux and ttyd integration.
- On-demand code-server workspaces for user-selected folders.
- Multiple terminal sessions presented as vertical tabs.
- Terminal copy and paste support.
- LAN access only; no Tailscale integration.
- Authentication using a Linux system user's current password.
- Installation and service configuration performed by an Ansible playbook invoked directly by the existing TUI.
- Runtime managed as a systemd system service.

## Assumptions for version 1

- One non-root LinuxCNC system account is selected during installation.
- PAM authenticates only that configured account.
- The Go service, private tmux servers, ttyd processes, terminal shells, and code-server processes all run as that account.
- Browser disconnection does not destroy tmux sessions.
- Managed terminal/editor workload persistence across a systemd service restart, upgrade restart, or machine reboot is not provided in version 1; **Remember me** authentication is the explicit exception and uses private durable state.
- The production appliance profile defaults to plaintext HTTP on an isolated machine LAN so client certificates are not required. It is never presented as equivalent security: the real system password and all session traffic are observable on that LAN. HTTPS remains selectable when trusted TLS material is available.
- The default maximum number of sessions is eight.
- The default maximum number of active code servers is two, configurable from one through eight.
- The default service port is `8443`.
- Ordinary browser authentication sessions are in-memory, with a 30-minute idle timeout and 12-hour absolute timeout by default. **Remember me** uses a persistent browser cookie, a fixed 30-day lifetime, and durable server-side token-hash state.

Supporting arbitrary local users with different shell identities is outside version 1. It would require a narrowly scoped privileged broker or separate per-user workers; the web service must not be changed to run as root as a shortcut.

## Architecture

```text
Browser over selected HTTP/HTTPS transport
        |
        v
Go service
  |- PAM login and server-side authentication sessions
  |- React static application
  |- terminal tmux session-management API
  |- code-server folder/lifecycle API
  `- authenticated HTTP/WebSocket reverse proxies
                         |
                  Unix-domain sockets
                    /           \
          ttyd per terminal   code-server per folder
                    |             |
             dedicated private tmux server sockets
```

Only the Go listener is exposed to the LAN. ttyd and code-server listen on permission-restricted Unix-domain sockets and cannot be contacted directly over the network.

## Implemented source layout

```text
remoteterminal/
|- IMPLEMENTATION_PLAN.md
|- README.md
|- Makefile
|- go.mod
|- go.sum
|- cmd/
|  `- remoteterminal/
|     `- main.go
|- internal/
|  |- auth/
|  |- codeservers/
|  |- config/
|  |- httpapi/
|  `- sessions/
|- web/
|  |- package.json
|  |- package-lock.json
|  `- src/
|- ansible/
|  |- install.yml
|  |- uninstall.yml
|  `- roles/
|     `- remoteterminal/
|        |- defaults/main.yml
|        |- handlers/main.yml
|        |- tasks/main.yml
|        `- templates/
`- scripts/
   `- build.sh
```

There is no root-level `install-remoteterminal.sh`. The supported TUI embeds the Remote Terminal production sources and materializes `ansible/install.yml` for its Ansible runner, so the installed TUI does not depend on the repository checkout. The repository-root `setup.sh` shell menu is deprecated and retained only as a compatibility launcher for the TUI.

## TUI and Ansible installation

The implemented TUI path is responsible for:

- Ensuring Ansible is available before launching the playbook.
- Requesting privilege escalation.
- Selecting or supplying the target system user.
- Selecting the LAN listen address and port.
- Selecting the default plaintext HTTP appliance transport or optional HTTPS transport.
- Displaying Ansible progress and the final service URL.
- Invoking the local playbook with the selected user, address, and port, equivalent to:

```bash
ansible-playbook \
  -i localhost, \
  --connection=local \
  --become \
  --extra-vars 'remoteterminal_user=linuxcnc' \
  --extra-vars '{"remoteterminal_machine_name":"Workshop Mill"}' \
  --extra-vars 'remoteterminal_listen_address=192.168.1.20' \
  --extra-vars 'remoteterminal_transport=http' \
  --extra-vars 'remoteterminal_port=8443' \
  remoteterminal/ansible/install.yml
```

Optional TLS paths and advanced overrides are supported by direct Ansible invocation.

The playbook is responsible for:

1. Verifying AMD64, Debian, systemd, the source directory, and the selected non-root account.
2. Installing build and runtime dependencies:
   - Node.js and npm.
   - tmux.
   - PAM runtime and development packages.
   - Git, compiler, CMake, pkg-config, and ttyd library dependencies.
3. Downloading the official Go 1.26.5 Linux AMD64 archive, verifying its fixed SHA-256 digest and archive-member safety, checking its exact reported version as the selected account, and atomically promoting the root-owned tool below `/opt/remoteterminal/tools`.
4. Obtaining a pinned ttyd source revision, verifying it, and building ttyd from source.
5. Downloading the official code-server 4.132.0 AMD64 standalone archive, verifying its fixed SHA-256 digest and archive-member safety, validating it as the selected account, and atomically promoting the root-owned tool. Repair writes a durable restart marker before mutation, stops only the validated private code-server tmux server after exchange, and retains the displaced tree until deployment health verification.
6. Running `npm ci` and building the React application.
7. Running Go tests and compiling the Go service with PAM/cgo support using only the pinned application-owned toolchain with automatic toolchain downloads disabled.
8. Installing versioned artifacts below `/opt/remoteterminal`.
9. Installing configuration and PAM policy below `/etc/remoteterminal`, plus atomic TLS material when HTTPS is selected.
10. Installing, enabling, and starting the systemd service.
11. Checking the selected-transport health endpoint before reporting success.

Third-party source builds must run as the selected non-root account. Root privileges are used only for package installation and deployment of system-owned files.

The playbook uses checksum-pinned sources, content-addressed application and tool releases, atomic promotion/TLS links, deferred cleanup of displaced dependency trees, and durable restart markers to make unchanged reruns idempotent and changed-source reruns controlled upgrades. Second-run idempotence still requires confirmation on the supported target. The uninstall playbook stops and disables the service, removes application-owned files by default, and explicitly reports preservation behavior.

Source installation initially requires network access for APT, npm modules, Go modules, the checksum-pinned Go toolchain archive, the pinned ttyd source, and the checksum-pinned code-server archive. A fully vendored/offline installation can be added later.

## Authentication design

### Login

- The React login screen submits the configured username, password, and explicit **Remember me** choice once. HTTPS encrypts it; the default HTTP appliance mode warns that it is plaintext on the network.
- The backend authenticates through Linux PAM using `/etc/pam.d/remoteterminal`.
- Both PAM authentication and account-management checks are required so locked or expired accounts are rejected.
- Only the account selected during installation is accepted.
- Authentication failures return a generic message that does not reveal whether the username exists.
- Passwords must never be stored or logged.

### Browser session

After successful PAM authentication, the backend creates a cryptographically random, opaque, server-side session and sends a cookie with:

- `HttpOnly`.
- `Secure` and a `__Host-` name in HTTPS mode; HTTP uses a distinct host-only non-`Secure` cookie because browsers reject the secure cookie on plaintext origins.
- `SameSite=Strict`.
- No browser persistence attributes for an ordinary login, or a fixed expiration and `Max-Age` when **Remember me** is selected.
- Server-side idle and absolute expiration for an ordinary login, or one fixed remembered-session expiration.

State-changing requests require a CSRF token and a valid same-origin `Origin` header. WebSocket upgrades also validate the authenticated session and origin. Logout immediately invalidates the server-side session.

Failed logins are rate-limited by source IP and username. Request bodies and concurrent authentication attempts are bounded.

Ordinary browser sessions live only in service memory and have a default idle lifetime of 30 minutes plus a non-sliding absolute lifetime of 12 hours. Remembered sessions have a fixed 30-day lifetime with no shorter idle deadline; only their SHA-256 token hashes, CSRF state, account/scope binding, and deadlines are atomically stored in a mode-`0600` file beneath `/var/lib/remoteterminal/auth`. The raw cookie token and password are never persisted. Ansible overrides `remoteterminal_idle_timeout`, `remoteterminal_absolute_timeout`, and `remoteterminal_remember_timeout` accept Go duration strings; the idle timeout must stay at or below the ordinary absolute timeout. Service and machine restarts restore unexpired remembered sessions, while logout durably removes the selected token.

### PAM feasibility checkpoint

The checkpoint is implemented without making the HTTP service root. Every deployment exercises `acct_mgmt` through `pamtester` as the exact selected systemd account. Full password verification is performed locally and interactively with:

```bash
sudo /usr/local/sbin/remoteterminal-pam-check
```

The fixed-argument helper uses `runuser` and `pamtester` to perform `authenticate` and `acct_mgmt`; it accepts no password argument or environment value. Success writes `/etc/remoteterminal/pam-authentication.verified`, containing only a root-readable fingerprint of the selected user and effective PAM-policy inputs. A changed user or policy invalidates the marker.

`remoteterminal_require_verified_pam_authentication=false` is the default. Setting it to `true` makes a matching marker a strict deployment gate. If strict mode stops an initial run, the helper has already been installed; run it from a trusted local terminal and rerun the playbook. If the selected account cannot safely authenticate itself through the distribution's PAM configuration, deployment must stop for a design review rather than running the HTTP service as root.

## tmux and ttyd integration

- Use an application-owned tmux socket so existing user tmux sessions remain invisible and untouched.
- Validate public session names and use an internal identifier for process paths and URLs.
- Execute tmux and ttyd with argument arrays; do not construct shell command strings.
- Enforce a configurable maximum session count.
- Make create, connect, and delete operations concurrency-safe.
- Start one ttyd process for each terminal session when it is first connected.
- Bind each ttyd process to a private Unix-domain socket.
- Launch ttyd in writable mode and enable origin checking.
- Configure ttyd with the reverse-proxy base path for its session.
- Run a fixed command that attaches ttyd to the selected tmux session; never accept command arguments from browser URLs.
- Enable tmux mouse handling before each attach so wheel input scrolls tmux history instead of becoming shell Up/Down input.
- Stop the ttyd process when its tmux session is deleted.
- Preserve tmux sessions when a browser tab closes or the user logs out.
- On backend startup, discover valid sessions on the dedicated tmux socket and clean up stale ttyd state.

The Go reverse proxy must support ttyd's normal HTTP requests and WebSocket upgrade traffic. Authentication is enforced by Go before any request is proxied.

The version 1 persistence boundary is intentionally narrow. The manager leaves tmux state alone on an ordinary browser disconnect and its shutdown path does not issue a broad/default-server kill, but the deployed systemd cgroup, runtime-directory lifecycle, upgrade restart, and reboot do not promise managed-session survival. Operators must treat service restart, upgrade, reboot, and uninstall as session-ending boundaries. Durable restart/reboot restoration is a future design choice.

## tmux and code-server integration

- Browse directories as the configured account, starting at its home directory, and accept only canonical absolute directories that account can traverse and read.
- Canonicalize symbolic links before identity and profile decisions. Reuse the active instance when a requested folder resolves to the same canonical directory.
- Enforce `remoteterminal_max_code_servers`, default two and validated from one through eight.
- Launch each instance through direct tmux argument arrays on `/run/remoteterminal/code-server.tmux.sock`; never interpolate a folder into a shell command.
- Bind each code-server HTTP listener and session IPC endpoint to a private Unix-domain socket below `/run/remoteterminal/code-server/INSTANCE/`.
- Disable code-server authentication only because the Go proxy authenticates every HTTP and WebSocket request. Disable telemetry, update checks, and code-server's built-in port proxy.
- Give each canonical folder an isolated persistent user-data and extensions profile under `/var/lib/remoteterminal/code-server/profiles`.
- Migrate each profile once to the built-in **Dark Modern** color theme while preserving unrelated JSONC settings and respecting later user theme changes.
- Keep an instance running across tab closure, browser closure, and logout. Stop it only through explicit **Shutdown**, natural process exit, or Remote Terminal service shutdown/restart/upgrade/reboot.
- On startup and list operations, reconcile private tmux state with runtime sockets so stale processes and entries are not presented as active.

The reverse proxy strips `/code/INSTANCE/`, preserves unrelated editor cookies, removes Remote Terminal credentials before forwarding, supplies trusted forwarding headers, and rewrites upstream root paths and cookie/service-worker scopes back beneath the instance prefix. It applies authentication and exact-origin validation to both HTTP and WebSocket traffic. The SPA content-security policy is not applied to editor responses; upstream editor policy is preserved with same-origin framing and standard containment headers.

The embedded editor is deliberately inside the trusted same-origin application boundary. Workspace content, editor webviews, extensions, and tasks execute with the configured account's effective workstation authority and can interact with authenticated Remote Terminal APIs through that origin. Operators must open only trusted workspaces and install only trusted extensions; this version does not claim a security boundary between code-server content and a terminal for the same account.

## Backend API

```text
GET    /api/config
POST   /api/auth/login
GET    /api/auth/session
POST   /api/auth/logout

GET    /api/sessions
POST   /api/sessions
DELETE /api/sessions/{id}
POST   /api/sessions/{id}/connect

GET    /api/directories?path=/absolute/path
GET    /api/code-servers
POST   /api/code-servers
DELETE /api/code-servers/{id}

GET    /terminal/{id}/...
GET    /code/{id}/...
GET    /healthz
```

API rules:

- Use JSON for API requests and responses.
- Return a consistent structured error format.
- Reject unknown JSON fields and oversized bodies.
- Validate session ownership and existence on every request.
- Do not expose filesystem paths, process arguments, or detailed dependency errors to unauthenticated clients.
- Keep `/healthz` minimal when unauthenticated.

Closing a frontend tab only detaches that browser view. Deleting a session is a separate confirmed action that kills the tmux session.

Closing a code-server tab also only detaches the browser view. Shutting down a code server is a separate confirmed action. Requests for canonical folders that already have an active instance return that instance so the frontend can focus it.

## React frontend

### Main layout

- A login view is shown until an authenticated browser session exists.
- The configured machine display name is shown on the login view and terminal workspace header.
- The application uses a left-side vertical tab list.
- Each tab shows its name and connection state.
- Controls allow creating a session, opening an existing session, closing a browser tab, and deleting a tmux session.
- Deleting requires confirmation and is visually distinct from closing a tab.
- The selected tab and tab order may be stored locally, scoped to the authenticated username; credentials and authentication tokens are never stored in browser storage.
- Open terminal frames remain mounted while switching tabs so live connections, terminal state, and scrollback are preserved.
- Editor tabs have a distinct icon and blue/cyan treatment, and editor frames remain mounted after first activation so switching does not reload the workspace.
- The folder picker supports breadcrumb/up navigation, directory rows, and a validated absolute-path entry. The active-code-server list offers open/focus and confirmed shutdown actions.
- Non-sensitive tab state uses typed terminal/editor records, with migration from the terminal-only storage format; paths, URLs, credentials, and tokens are not stored.
- Interrupted connections display a reconnect state without destroying the tmux session.

### Accessibility

- Use `role="tablist"` with `aria-orientation="vertical"`.
- Use proper `tab`, `tabpanel`, selected, and controlled relationships.
- Support Up/Down, Home/End, Enter, and Delete/close keyboard behavior where appropriate.
- Maintain visible focus indicators and sufficient contrast.
- Keep the desktop sidebar compact and information-dense, and collapse it cleanly on narrow screens.

### Copy and paste

Version 1 uses the pinned ttyd/xterm browser client inside a same-origin terminal frame.

- Use `Shift` while dragging on Linux/Windows, or `Option` on macOS, to force
  an xterm selection while tmux mouse handling remains enabled for scrollback.
- On every nonempty xterm selection change, synchronously trigger the document
  copy command. xterm's built-in copy event supplies the exact selected text,
  matching stock ttyd behavior without a server clipboard request or modal.
- Keep the selection visible so a browser context-menu Copy action can repeat
  the operation or serve as the fallback when policy blocks automatic copy.
- Keep `Ctrl+C` as terminal interrupt input. Do not advertise browser-reserved
  key chords as the primary Linux/Windows copy workflow.
- Paste with `Ctrl+Shift+V` or `Shift+Insert` on Linux/Windows, and `Command+V`
  on macOS.
- Explicitly deny asynchronous Clipboard API access to the terminal frame and
  keep tmux OSC 52 integration disabled.
- Test plain text, multiline text, Unicode, and Ukrainian keyboard input in supported browsers.

The pinned ttyd build remains patched to remove ClipboardAddon and its
`navigator.clipboard`/OSC 52 surface. The same-origin terminal wrapper restores
only ttyd's selection-change copy trigger; xterm core owns the resulting copy
event. This remains usable on ordinary HTTP and requires no iframe Clipboard
API permission.

## Systemd deployment

- Run the service as the selected non-root account.
- Set the account's home directory and shell environment explicitly.
- Use systemd-managed runtime and state directories with restrictive permissions.
- Load non-secret settings, including `REMOTE_TERMINAL_MACHINE_NAME`, from `/etc/remoteterminal`.
- Restart on unexpected failure with bounded restart frequency.
- Use graceful shutdown for HTTP, WebSockets, ttyd child processes, and managed code-server instances.
- Do not apply sandboxing directives that silently prevent the terminal account from performing its normal work.
- Do not grant additional capabilities or privileges to the service.
- Ensure only the configured HTTP or HTTPS address and port are exposed.

## LAN and TLS behavior

- The installer receives a specific LAN address from the TUI instead of exposing every interface by default.
- The default port is `8443`; it is configurable.
- The backend serves the explicitly selected transport directly. The production role and TUI select HTTP by default; HTTPS is the optional trusted-certificate mode.
- If certificate and key paths are supplied together, Ansible validates, copies, and uses them. Changed source-file checksums cause an atomic rotation on a later run.
- Otherwise, Ansible generates a self-signed RSA-3072 certificate for 825 days with hostname, FQDN, and selected LAN IP subject alternative names, and reports its SHA-256 fingerprint.
- Active and candidate TLS material is checked for validity, key matching, and a 30-day default renewal window. Generated material is replaced when its profile/address changes, it becomes invalid, or it enters that window. A complete validated candidate is atomically activated; failure retains the prior active set and a successful change requests a controlled service restart.
- Automatic firewall modification is excluded to avoid interfering with LinuxCNC network configuration. The role verifies a specific non-loopback IPv4 address and, by default, that the address belongs to the host. Binding plus host/router/firewall policy define LAN reachability; the service must not be exposed directly to the public internet.
- Plain HTTP with a system password is the accepted appliance default only for the isolated trusted machine LAN requested by the product. It has a persistent login-page and installer warning, and cannot provide normal remote code-server webviews unless the exact origin is placed in a managed Chromium secure-origin policy or reached as `localhost` through an encrypted tunnel.

## Uninstall behavior

Uninstall always stops and disables the service, stops only the tmux servers addressed through `/run/remoteterminal/tmux.sock` and `/run/remoteterminal/code-server.tmux.sock`, removes the runtime directory, and removes the systemd unit, PAM policy, and interactive PAM helper. Live terminal and code-server state is therefore not preserved. Unrelated tmux servers, the selected user's home files, LinuxCNC configuration, firewall policy, and Debian dependency packages remain untouched.

Application-owned files are removed by default. Four explicit boolean overrides retain selected categories:

- `remoteterminal_uninstall_preserve_installation=true` retains `/opt/remoteterminal`, including the pinned Go, ttyd, and code-server tools.
- `remoteterminal_uninstall_preserve_config=true` retains `/etc/remoteterminal`, including TLS and the PAM verification marker.
- `remoteterminal_uninstall_preserve_state=true` retains `/var/lib/remoteterminal`, including isolated code-server profiles and unexpired remembered-session hashes.
- `remoteterminal_uninstall_preserve_build_cache=true` retains `/var/lib/remoteterminal-build`.

These flags do not retain live processes or `/run/remoteterminal`; all default to `false`.

## Build constraints

- Pin all npm dependencies and commit `package-lock.json`.
- Pin all Go modules and commit `go.sum`.
- Pin ttyd to an exact source revision and verify its archive or commit.
- Pin the official code-server standalone archive to an exact version and SHA-256 digest; validate archive paths/types before extraction and activate only a complete root-owned candidate.
- Build and test only with the checksum-pinned application-owned Go 1.26.5 Linux AMD64 toolchain; disable automatic Go toolchain switching/downloads.
- Keep the frontend build compatible with the selected Node.js version.
- Embed or atomically install the production React assets so the service cannot serve a frontend from a different application version.
- Do not use unpinned `curl | shell` installation steps.

## Implementation phases and current state

### Phase 0: Prototype reconciliation — implemented

- The frontend was reconciled with the agreed architecture and API.
- The final source layout, formatting, locked dependencies, and build commands are present.

### Phase 1: Security and dependency checkpoints — implemented with target checks pending

- The PAM adapter, fake-authenticator coverage, installed account probe, interactive verifier, and optional strict gate are implemented; real target credentials remain an interactive acceptance check.
- The checksum-pinned ttyd 1.7.7 source-build path is implemented; compilation on the clean supported target remains an acceptance check.
- The opt-in real-process test covers ttyd HTTP and WebSocket proxying through a Unix socket and session base path.
- A separate opt-in real-process test covers canonical-folder reuse, limits, private tmux/socket isolation, HTTP proxying, profile persistence, and graceful code-server shutdown with the production argument set.
- Clipboard, Unicode, and Ukrainian input in Chrome/Chromium and Firefox remain manual browser acceptance checks.

### Phase 2: Go backend — implemented

- Configuration/startup validation, PAM login, ordinary in-memory and durable hashed remembered sessions, CSRF/origin enforcement, throttling, and security headers are implemented.
- Concurrency-safe tmux session management, ttyd process/socket lifecycle, authenticated reverse proxying, graceful HTTP shutdown, cleanup, logging, and health checks are implemented.
- Canonical-folder code-server management, per-instance private sockets and profiles, authenticated HTTP/WebSocket proxying, active limits, reconciliation, and graceful shutdown are implemented.

### Phase 3: React frontend — implemented

- Login/logout, session loading/creation/deletion, error and reconnect states, accessible vertical tabs, mounted terminal frames, responsive styling, and native-copy help are implemented.
- Folder browsing, launch/reuse, active-code-server management, confirmed shutdown, visually distinct mounted editor tabs, and responsive editor controls are implemented.
- Actual clipboard and keyboard behavior remains subject to supported-browser acceptance.

### Phase 4: Ansible and TUI integration — implemented

- The source-build role, deployment templates, setup-menu entry, selected user/address/transport/port handoff, content-addressed upgrades, systemd health verification, and uninstall path are implemented.
- Supplied TLS paths and advanced variables are available through direct Ansible invocation.

### Phase 5: Verification and documentation — partially complete

- Backend and frontend automated suites and opt-in real tmux/ttyd and tmux/code-server integration tests are present.
- Operation, configuration, logs, recovery, persistence limits, TLS, PAM verification, and security boundaries are documented.
- Clean-target installation, second-run idempotence, upgrade/restart, reboot, supported-browser behavior, interactive PAM positive/negative cases, and uninstall remain manual acceptance work.

## Verification plan and coverage

### Automated Go tests

- PAM interface behavior with a fake authenticator.
- Authentication expiration, logout, throttling, and generic failures.
- CSRF and origin enforcement.
- Session-name and identifier validation.
- Safe tmux, ttyd, and code-server argument construction.
- Concurrent create/connect/delete and launch/list/shutdown operations.
- Terminal and code-server HTTP/WebSocket proxy authorization and path handling.
- Process cleanup and stale-state recovery.

### Automated React tests

- Login submission without credential persistence and generic rejection messaging.
- Multiple vertical-tab creation, selection, ordering, Arrow-key navigation, and mounted-panel behavior.
- Close-tab behavior versus confirmed session deletion.
- Lazy connection and restoration of non-sensitive per-user tab state.
- Code-server folder browsing, launch/reuse, active-list refresh, mounted editor tabs, and confirmed shutdown.
- In-memory CSRF propagation and rejection of terminal URLs outside the same-origin proxy path.

### Opt-in real-process integration

The Linux-only terminal test is skipped unless all three controls are explicit:

```bash
REMOTETERMINAL_RUN_PROCESS_INTEGRATION=1 \
REMOTETERMINAL_TMUX_BINARY=/absolute/path/to/tmux \
REMOTETERMINAL_TTYD_BINARY=/absolute/path/to/ttyd \
go test -run '^TestManagerRealProcessLifecycle$' -count=1 -v ./internal/sessions
```

An extracted dynamically linked ttyd may additionally require `LD_LIBRARY_PATH`. The test uses private temporary tmux and ttyd sockets plus a separate unrelated-tmux canary, and covers:

- Create a real tmux session, connect through ttyd, execute a command, disconnect, and reconnect to the same session.
- Verify that deleting a session removes its tmux session and ttyd process.
- Verify HTTP through the manager's Unix-socket reverse proxy and a WebSocket `101` handshake with ttyd's `tty` subprotocol.
- Verify prior terminal output and shell state after reconnect, and verify process/socket cleanup without touching the canary tmux server.

The code-server lifecycle has a separate Linux-only test using the same explicit enable flag:

```bash
REMOTETERMINAL_RUN_PROCESS_INTEGRATION=1 \
REMOTETERMINAL_TMUX_BINARY=/absolute/path/to/tmux \
REMOTETERMINAL_CODE_SERVER_BINARY=/absolute/path/to/code-server \
go test -run '^TestCodeServerManagerRealProcessLifecycle$' -count=1 -v ./internal/codeservers
```

It launches code-server with the production Unix-socket, session-socket, isolated-profile, proxy-disabled, and working-folder arguments. It verifies managed-config isolation, canonical-folder reuse, the active-instance limit, private socket modes, HTTP proxying, graceful deletion, persistent profile state, shell-metacharacter safety, and isolation from an unrelated tmux server.

### Pending integration and browser tests

- Verify multiple simultaneous vertical tabs.
- Verify copy and paste of plain, multiline, Unicode, and Ukrainian text.
- Verify behavior in supported Chrome/Chromium and Firefox versions.
- Verify successful and failed PAM authentication interactively without placing a production password in automated fixtures.

### Pending target installation tests

- Install on a clean AMD64 Debian target.
- Confirm the service runs as the selected non-root account.
- Confirm service startup after reboot.
- Confirm only the Go listener is present on the selected LAN address and uses the requested transport.
- Confirm ttyd uses only private Unix sockets.
- Confirm code-server uses only private Unix sockets, no upstream systemd unit, and the reported pinned version.
- Apply Ansible a second time and confirm idempotency.
- Upgrade after a source change and verify a controlled service restart.
- Uninstall without modifying unrelated tmux sessions or LinuxCNC configuration.

## Acceptance criteria

These remain the release criteria. Source implementation and local automation do not substitute for the pending clean-target and supported-browser checks in the README checklist.

- The TUI invokes the Ansible playbook directly without a root-level shell installer.
- The playbook builds the application and ttyd from source with the exact checksum-pinned official Go 1.26.5 Linux AMD64 toolchain and installs the checksum-pinned official code-server standalone dependency.
- Installation completes with a healthy, enabled systemd service.
- Only the configured system account can log in with its current PAM password.
- The service and all terminal processes run as that non-root account.
- Authentication credentials never appear in logs, process arguments, files, or browser storage.
- The chosen machine name appears on the login view and terminal workspace header.
- Users can create and use multiple tmux sessions from vertical tabs.
- Switching or closing a browser tab does not kill its tmux session.
- Session deletion is explicit and confirmed.
- Copy and paste work with documented terminal shortcuts.
- ttyd is never directly exposed to the LAN.
- code-server is never directly exposed to the LAN; every HTTP/WebSocket request passes through the authenticated same-origin proxy.
- Re-running the playbook is idempotent.
- Existing LinuxCNC setup behavior remains working.

## Current version 1 decisions and defaults

1. **Transport:** HTTP is the production appliance default for the isolated machine LAN. It skips TLS, uses a separate cookie name, exposes credentials/session traffic on the LAN, and requires an exact managed Chromium secure-origin exception for normal remote code-server webviews. HTTPS remains available; operators then verify the generated certificate fingerprint or supply a trusted certificate/key pair.
2. **Clipboard:** `Shift`-drag on Linux/Windows or `Option`-drag on macOS creates a visible xterm selection and copies it automatically, matching stock ttyd behavior without a server request or copy dialog. Browser context-menu Copy repeats or falls back while the selection remains highlighted. `Ctrl+C` remains terminal interrupt input. Paste uses `Ctrl+Shift+V`/`Shift+Insert` on Linux/Windows or `Command+V` on macOS.
3. **Port:** `8443` is the default listener port. `remoteterminal_port` overrides it within the validated unprivileged port range.
4. **Session limit:** Eight managed sessions is the default. `remoteterminal_max_sessions` overrides it from 1 through 64.
5. **Persistence:** Version 1 preserves tmux sessions across browser disconnect, tab closure, and logout while the service continues running. Service restart, upgrade restart, reboot, and uninstall are outside that guarantee. Durable restart/reboot persistence is a future design choice.
6. **Authentication lifetime:** Ordinary browser sessions default to 30 minutes idle and 12 hours absolute. **Remember me** defaults to a fixed 30-day lifetime and survives service/machine restarts through private token-hash state. All three durations are configurable with validated overrides.
7. **Code-server limit:** Two active code servers is the default. `remoteterminal_max_code_servers` overrides it from 1 through 8; a canonical folder has at most one active instance.
8. **Code-server lifecycle:** Tab closure, browser closure, and logout detach only. Explicit shutdown, service stop/restart/upgrade, reboot, and uninstall stop active editors; per-folder profiles persist unless application state is removed.
9. **Code-server proxy:** The built-in port proxy is disabled. Per-instance paths are served only through Remote Terminal's authenticated same-origin proxy.
10. **Code-server trust boundary:** The editor shares the Remote Terminal origin and the configured account's effective authority. Workspaces, webviews, tasks, and extensions are trusted content; users must open and install only content they trust. They are not isolated from authenticated Remote Terminal APIs or from a terminal running as that account.

## References

- ttyd project and command-line capabilities: <https://github.com/tsl0922/ttyd>
- code-server project and standalone releases: <https://github.com/coder/code-server>
- Linux-PAM used by the direct cgo adapter: <https://github.com/linux-pam/linux-pam>
- Ansible systemd service module: <https://docs.ansible.com/projects/ansible/latest/collections/ansible/builtin/systemd_service_module.html>
- Ansible verified download module: <https://docs.ansible.com/projects/ansible/latest/collections/ansible/builtin/get_url_module.html>
