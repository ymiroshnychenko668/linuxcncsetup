# Remote Terminal Implementation Plan

## Status

The agreed version 1 design is implemented in this repository. The source tree now contains the Go backend, React/TypeScript frontend, tmux/ttyd manager and authenticated proxy, setup-menu integration, Ansible source-build/deployment and uninstall role, systemd/PAM/TLS templates, and automated tests.

Source-level unit tests and a strictly opt-in real tmux/ttyd lifecycle test are available. Clean AMD64 Debian/LinuxCNC installation, interactive PAM behavior, supported-browser clipboard/input behavior, second-run idempotence, reboot, and uninstall remain target-machine acceptance work. Nothing in this document claims those target-only checks have run.

This document is retained as the architecture, security contract, implementation record, and acceptance plan. Future choices are identified separately from current version 1 defaults.

## Confirmed scope

- AMD64 Debian/LinuxCNC systems only for the first version.
- Install and build from source code on the target machine.
- Go backend.
- React and TypeScript frontend.
- tmux and ttyd integration.
- Multiple terminal sessions presented as vertical tabs.
- Terminal copy and paste support.
- LAN access only; no Tailscale integration.
- Authentication using a Linux system user's current password.
- Installation and service configuration performed by an Ansible playbook invoked directly by the existing TUI.
- Runtime managed as a systemd system service.

## Assumptions for version 1

- One non-root LinuxCNC system account is selected during installation.
- PAM authenticates only that configured account.
- The Go service, tmux server, ttyd processes, and terminal shells all run as that account.
- Browser disconnection does not destroy tmux sessions.
- Persistence across a systemd service restart, upgrade restart, or machine reboot is not provided in version 1; the persistence guarantee covers browser disconnects, tab closure, and logout while the service remains running.
- The browser connects over HTTPS because the login uses the real system password.
- The default maximum number of sessions is eight.
- The default service port is `8443`.
- Browser authentication sessions are in-memory, with a 30-minute idle timeout and 12-hour absolute timeout by default.

Supporting arbitrary local users with different shell identities is outside version 1. It would require a narrowly scoped privileged broker or separate per-user workers; the web service must not be changed to run as root as a shortcut.

## Architecture

```text
Browser over HTTPS
        |
        v
Go service
  |- PAM login and server-side authentication sessions
  |- React static application
  |- tmux session-management API
  `- authenticated HTTP/WebSocket reverse proxy
                         |
                  Unix-domain sockets
                         |
                  ttyd per session
                         |
             dedicated tmux server socket
```

Only the Go HTTPS listener is exposed to the LAN. ttyd listens on permission-restricted Unix-domain sockets and cannot be contacted directly over the network.

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
  --extra-vars 'remoteterminal_port=8443' \
  remoteterminal/ansible/install.yml
```

Optional TLS paths and advanced overrides are supported by direct Ansible invocation; prompting for them in the TUI is a future UI choice.

The playbook is responsible for:

1. Verifying AMD64, Debian, systemd, the source directory, and the selected non-root account.
2. Installing build and runtime dependencies:
   - Go toolchain.
   - Node.js and npm.
   - tmux.
   - PAM runtime and development packages.
   - Git, compiler, CMake, pkg-config, and ttyd library dependencies.
3. Obtaining a pinned ttyd source revision and verifying it before building.
4. Building ttyd from source.
5. Running `npm ci` and building the React application.
6. Running Go tests and compiling the Go service with PAM/cgo support.
7. Installing versioned artifacts below `/opt/remoteterminal`.
8. Installing configuration, PAM policy, and TLS material below `/etc/remoteterminal`.
9. Installing, enabling, and starting the systemd service.
10. Checking the HTTPS health endpoint before reporting success.

Third-party source builds must run as the selected non-root account. Root privileges are used only for package installation and deployment of system-owned files.

The playbook uses checksum-pinned sources, content-addressed application releases, atomic release/TLS links, and durable restart markers to make unchanged reruns idempotent and changed-source reruns controlled upgrades. Second-run idempotence still requires confirmation on the supported target. The uninstall playbook stops and disables the service, removes application-owned files by default, and explicitly reports preservation behavior.

Source installation initially requires network access for APT, npm modules, Go modules, and the pinned ttyd source. A fully vendored/offline installation can be added later.

## Authentication design

### Login

- The React login screen submits the configured username and password once over HTTPS.
- The backend authenticates through Linux PAM using `/etc/pam.d/remoteterminal`.
- Both PAM authentication and account-management checks are required so locked or expired accounts are rejected.
- Only the account selected during installation is accepted.
- Authentication failures return a generic message that does not reveal whether the username exists.
- Passwords must never be stored or logged.

### Browser session

After successful PAM authentication, the backend creates a cryptographically random, opaque, server-side session and sends a cookie with:

- `HttpOnly`.
- `Secure`.
- `SameSite=Strict`.
- An idle expiration.
- An absolute expiration.

State-changing requests require a CSRF token and a valid same-origin `Origin` header. WebSocket upgrades also validate the authenticated session and origin. Logout immediately invalidates the server-side session.

Failed logins are rate-limited by source IP and username. Request bodies and concurrent authentication attempts are bounded.

Browser sessions live only in service memory. Their default idle lifetime is 30 minutes and their non-sliding absolute lifetime is 12 hours. Ansible overrides `remoteterminal_idle_timeout` and `remoteterminal_absolute_timeout` accept Go duration strings and must keep the idle timeout at or below the absolute timeout. A service restart invalidates existing browser logins.

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

GET    /terminal/{id}/...
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
- Interrupted connections display a reconnect state without destroying the tmux session.

### Accessibility

- Use `role="tablist"` with `aria-orientation="vertical"`.
- Use proper `tab`, `tabpanel`, selected, and controlled relationships.
- Support Up/Down, Home/End, Enter, and Delete/close keyboard behavior where appropriate.
- Maintain visible focus indicators and sufficient contrast.
- Collapse the sidebar cleanly on narrow screens.

### Copy and paste

Version 1 uses the pinned ttyd/xterm browser client inside a same-origin terminal frame.

- Copy selected text with `Ctrl+Shift+C`.
- Paste with `Ctrl+Shift+V` or `Shift+Insert`.
- Permit clipboard access on the terminal frame.
- Test plain text, multiline text, Unicode, and Ukrainian keyboard input in supported browsers.

This plan interprets "copy/paste" as standard terminal shortcuts. Visible React Copy and Paste buttons would require either a maintained ttyd client bridge or a direct React/xterm implementation of ttyd's WebSocket protocol and therefore need a separate explicit requirement.

## Systemd deployment

- Run the service as the selected non-root account.
- Set the account's home directory and shell environment explicitly.
- Use systemd-managed runtime and state directories with restrictive permissions.
- Load non-secret settings, including `REMOTE_TERMINAL_MACHINE_NAME`, from `/etc/remoteterminal`.
- Restart on unexpected failure with bounded restart frequency.
- Use graceful shutdown for HTTP, WebSockets, and ttyd child processes.
- Do not apply sandboxing directives that silently prevent the terminal account from performing its normal work.
- Do not grant additional capabilities or privileges to the service.
- Ensure only the configured HTTPS address and port are exposed.

## LAN and TLS behavior

- The installer receives a specific LAN address from the TUI instead of exposing every interface by default.
- The default port is `8443`; it is configurable.
- The backend serves HTTPS directly.
- If certificate and key paths are supplied together, Ansible validates, copies, and uses them. Changed source-file checksums cause an atomic rotation on a later run.
- Otherwise, Ansible generates a self-signed RSA-3072 certificate for 825 days with hostname, FQDN, and selected LAN IP subject alternative names, and reports its SHA-256 fingerprint.
- Active and candidate TLS material is checked for validity, key matching, and a 30-day default renewal window. Generated material is replaced when its profile/address changes, it becomes invalid, or it enters that window. A complete validated candidate is atomically activated; failure retains the prior active set and a successful change requests a controlled service restart.
- Automatic firewall modification is excluded to avoid interfering with LinuxCNC network configuration. The role verifies a specific non-loopback IPv4 address and, by default, that the address belongs to the host. Binding plus host/router/firewall policy define LAN reachability; the service must not be exposed directly to the public internet.
- Plain HTTP with a system password is not an accepted default.

## Uninstall behavior

Uninstall always stops and disables the service, stops only the tmux server addressed through `/run/remoteterminal/tmux.sock`, removes the runtime directory, and removes the systemd unit, PAM policy, and interactive PAM helper. Live Remote Terminal tmux/ttyd state is therefore not preserved. Unrelated tmux servers, the selected user's home files, LinuxCNC configuration, firewall policy, and Debian dependency packages remain untouched.

Application-owned files are removed by default. Four explicit boolean overrides retain selected categories:

- `remoteterminal_uninstall_preserve_installation=true` retains `/opt/remoteterminal`.
- `remoteterminal_uninstall_preserve_config=true` retains `/etc/remoteterminal`, including TLS and the PAM verification marker.
- `remoteterminal_uninstall_preserve_state=true` retains `/var/lib/remoteterminal`.
- `remoteterminal_uninstall_preserve_build_cache=true` retains `/var/lib/remoteterminal-build`.

These flags do not retain live processes or `/run/remoteterminal`; all default to `false`.

## Build constraints

- Pin all npm dependencies and commit `package-lock.json`.
- Pin all Go modules and commit `go.sum`.
- Pin ttyd to an exact source revision and verify its archive or commit.
- Keep the Go source compatible with the toolchain installed by the Debian target, or have Ansible install an explicitly pinned newer toolchain.
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
- Clipboard, Unicode, and Ukrainian input in Chrome/Chromium and Firefox remain manual browser acceptance checks.

### Phase 2: Go backend — implemented

- Configuration/startup validation, PAM login, in-memory authentication sessions, CSRF/origin enforcement, throttling, and security headers are implemented.
- Concurrency-safe tmux session management, ttyd process/socket lifecycle, authenticated reverse proxying, graceful HTTP shutdown, cleanup, logging, and health checks are implemented.

### Phase 3: React frontend — implemented

- Login/logout, session loading/creation/deletion, error and reconnect states, accessible vertical tabs, mounted terminal frames, responsive styling, and clipboard permissions/help are implemented.
- Actual clipboard and keyboard behavior remains subject to supported-browser acceptance.

### Phase 4: Ansible and TUI integration — implemented

- The source-build role, deployment templates, setup-menu entry, selected user/address/port handoff, content-addressed upgrades, systemd health verification, and uninstall path are implemented.
- Supplied TLS paths and advanced variables are available through direct Ansible invocation; additional TUI prompts are a future enhancement.

### Phase 5: Verification and documentation — partially complete

- Backend and frontend automated suites and the opt-in real tmux/ttyd integration test are present.
- Operation, configuration, logs, recovery, persistence limits, TLS, PAM verification, and security boundaries are documented.
- Clean-target installation, second-run idempotence, upgrade/restart, reboot, supported-browser behavior, interactive PAM positive/negative cases, and uninstall remain manual acceptance work.

## Verification plan and coverage

### Automated Go tests

- PAM interface behavior with a fake authenticator.
- Authentication expiration, logout, throttling, and generic failures.
- CSRF and origin enforcement.
- Session-name and identifier validation.
- Safe tmux and ttyd argument construction.
- Concurrent create/connect/delete operations.
- WebSocket proxy authorization and path handling.
- Process cleanup and stale-state recovery.

### Automated React tests

- Login submission without credential persistence and generic rejection messaging.
- Multiple vertical-tab creation, selection, ordering, Arrow-key navigation, and mounted-panel behavior.
- Close-tab behavior versus confirmed session deletion.
- Lazy connection and restoration of non-sensitive per-user tab state.
- In-memory CSRF propagation and rejection of terminal URLs outside the same-origin proxy path.

### Opt-in real-process integration

The Linux-only test is skipped unless all three controls are explicit:

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

### Pending integration and browser tests

- Verify multiple simultaneous vertical tabs.
- Verify copy and paste of plain, multiline, Unicode, and Ukrainian text.
- Verify behavior in supported Chrome/Chromium and Firefox versions.
- Verify successful and failed PAM authentication interactively without placing a production password in automated fixtures.

### Pending target installation tests

- Install on a clean AMD64 Debian target.
- Confirm the service runs as the selected non-root account.
- Confirm service startup after reboot.
- Confirm only the Go HTTPS port is listening on the selected LAN address.
- Confirm ttyd uses only private Unix sockets.
- Apply Ansible a second time and confirm idempotency.
- Upgrade after a source change and verify a controlled service restart.
- Uninstall without modifying unrelated tmux sessions or LinuxCNC configuration.

## Acceptance criteria

These remain the release criteria. Source implementation and local automation do not substitute for the pending clean-target and supported-browser checks in the README checklist.

- The TUI invokes the Ansible playbook directly without a root-level shell installer.
- The playbook builds the application and ttyd from source on AMD64.
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
- Re-running the playbook is idempotent.
- Existing LinuxCNC setup behavior remains working.

## Current version 1 decisions and defaults

1. **TLS:** A generated self-signed certificate is accepted as the default. Operators must verify its reported fingerprint; a supplied certificate/key pair is the supported override. Requiring a trusted certificate or adding certificate prompts to the TUI remains a future policy/UI choice.
2. **Clipboard:** Standard terminal shortcuts are the version 1 interface: `Ctrl+Shift+C` to copy and `Ctrl+Shift+V` or `Shift+Insert` to paste. Visible React Copy/Paste buttons remain a future feature if explicitly required.
3. **Port:** `8443` is the default HTTPS port. `remoteterminal_port` overrides it within the validated unprivileged port range.
4. **Session limit:** Eight managed sessions is the default. `remoteterminal_max_sessions` overrides it from 1 through 64.
5. **Persistence:** Version 1 preserves tmux sessions across browser disconnect, tab closure, and logout while the service continues running. Service restart, upgrade restart, reboot, and uninstall are outside that guarantee. Durable restart/reboot persistence is a future design choice.
6. **Authentication lifetime:** Browser sessions default to 30 minutes idle and 12 hours absolute. Both are configurable with validated duration overrides; persistent authentication across service restart is not a version 1 feature.

## References

- ttyd project and command-line capabilities: <https://github.com/tsl0922/ttyd>
- Linux-PAM used by the direct cgo adapter: <https://github.com/linux-pam/linux-pam>
- Ansible systemd service module: <https://docs.ansible.com/projects/ansible/latest/collections/ansible/builtin/systemd_service_module.html>
- Ansible verified download module: <https://docs.ansible.com/projects/ansible/latest/collections/ansible/builtin/get_url_module.html>
