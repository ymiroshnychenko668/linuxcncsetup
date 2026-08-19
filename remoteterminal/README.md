# Remote Terminal

Remote Terminal provides authenticated browser access to persistent tmux terminals and on-demand code-server workspaces on a LinuxCNC workstation. A Go service authenticates the configured local account through PAM, serves the React application, manages both workloads in private tmux servers, and proxies their Unix-domain sockets over authenticated same-origin routes.

The first supported deployment target is Debian/LinuxCNC on AMD64 with tmux 3.2 or newer. The installer validates the configured tmux executable before deployment.

## Implementation status

The application, React frontend, Ansible source-build/deployment role, setup-menu entry, systemd unit, PAM policy and verifier, TLS management, terminal and code-server lifecycle management, and uninstall path are implemented in this repository. Backend and frontend automated tests are included, together with strictly opt-in real-process integration coverage.

Clean-machine Debian/LinuxCNC installation, browser behavior, reboot, and uninstall still require the target acceptance checks listed below. This document does not claim those target-only checks have been run.

## Security model

Remote Terminal grants the browser the same shell access as the configured Linux account. It is intended for a trusted LAN and must not be exposed directly to the public internet.

- The service and terminal processes run as one configured non-root account.
- The account's current password is checked once through PAM and is never stored.
- Browser authentication uses a server-side session, CSRF protection, and same-origin checks. HTTPS sessions use a `Secure` `__Host-` cookie; HTTP uses a separate host-only cookie. **Remember me** persists only a hash of the opaque token in the private application state; neither the raw browser token nor the Linux password is written to disk.
- The production appliance profile defaults to HTTP on an isolated machine LAN so certificates do not have to be provisioned on every workstation. It exposes the PAM password, terminal traffic, files, and editor traffic to observation or modification by anything able to intercept that LAN. HTTPS remains available when a trusted certificate can be deployed.
- ttyd and code-server listen only on per-instance private Unix-domain sockets.
- Dedicated terminal and code-server tmux sockets keep application sessions separate from the user's other tmux sessions.
- code-server's own authentication is disabled because the Remote Terminal proxy authenticates every HTTP and WebSocket request. Its built-in port proxy is disabled in this release.
- The embedded editor is intentionally part of the trusted application boundary: it shares the Remote Terminal browser origin and runs extensions and workspace tasks as the configured Linux account. Open only trusted workspaces and install only trusted extensions; editor content has the same effective workstation authority as a terminal for that account.

The installer binds only the selected non-loopback LAN IPv4 address. It does not modify host, router, or network firewalls; those controls remain the operator's responsibility and define which LAN clients can reach the service.

The generated certificate encrypts traffic but is not automatically trusted by client devices. Verify the SHA-256 fingerprint displayed during installation before importing it. A certificate and key issued by an existing trusted or private CA can be supplied to Ansible instead; no purchased certificate is required.

code-server webviews, service workers, the asynchronous Clipboard API, and some extension features require a browser secure context. Remote plain HTTP is not a secure context by itself. In the HTTP appliance profile, every client that needs full editor webviews must treat Remote Terminal as secure using the browser configuration below. Alternatives are a trusted HTTPS certificate or accessing the HTTP listener through a `localhost` SSH tunnel. Chromium offers a documented, port-scoped managed policy. Firefox offers the advanced `dom.securecontext.allowlist` preference, but it is hostname-wide rather than port-scoped. See code-server's [webview FAQ](https://github.com/coder/code-server/blob/v4.132.0/docs/FAQ.md#why-do-web-views-not-work), the browser [secure-context requirement for Service Workers](https://developer.mozilla.org/en-US/docs/Web/API/Service_Worker_API#secure_context), Chromium's [origin exception policy](https://chromeenterprise.google/policies/override-security-restrictions-on-insecure-origin/), and Firefox's [secure-context allowlist implementation](https://searchfox.org/firefox-main/source/dom/security/nsMixedContentBlocker.cpp).

## Installation from the TUI

Use the LinuxCNC Setup TUI application and choose **Install Remote Terminal**. The TUI asks for a machine display name, the local system user, LAN IPv4 address, transport, and port, then runs the bundled Ansible installer. HTTP is selected by default for the isolated appliance LAN; HTTPS can be selected when trusted TLS material is available.

The repository-root `setup.sh` shell menu is deprecated. It remains functional for compatibility but should not be used for new installations or updates.

The installer builds the React application, Go service, and a pinned ttyd revision from source. ttyd's bundled browser client is patched to remove ClipboardAddon and its terminal-controlled OSC 52 clipboard surface. The same-origin Remote Terminal wrapper supplies only ttyd's stock selection-change copy trigger while asynchronous clipboard access remains denied. `src/html.h` is regenerated with checksum-pinned Corepack 0.29.4 and Yarn 3.6.3 before the C build. The patch, complete patched `html/` manifest, web toolchain, compiler packages, archive, and commit all participate in the content-addressed ttyd build identity. The installer downloads the official Go 1.26.5 Linux AMD64 toolchain and code-server 4.132.0 AMD64 standalone archives, verifies both with fixed SHA-256 checksums, validates their contents, and promotes root-owned tools atomically beneath `/opt/remoteterminal/tools`. Debian's Go package and code-server's upstream systemd service are not installed. Network access is initially required for Debian packages, the pinned archives, and source dependencies.

The default transport is HTTP and the default port is `8443`. After installation, open:

```text
http://LAN_ADDRESS:8443/
```

Log in with the selected system username and its current password. **Remember me** is enabled by default and keeps that browser signed in for 30 days across browser, service, and machine restarts unless the user signs out, changes authentication transport/account, or removes application state.

## Direct Ansible invocation

The playbook can also be called directly from the repository root:

```bash
ansible-playbook \
  -i localhost, \
  --connection=local \
  --become \
  --extra-vars 'remoteterminal_user=linuxcnc' \
  --extra-vars '{"remoteterminal_machine_name":"Workshop Mill"}' \
  --extra-vars 'remoteterminal_listen_address=192.168.1.20' \
  remoteterminal/ansible/install.yml
```

Important variables include:

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `remoteterminal_user` | yes | none | Existing non-root account used for PAM and terminal processes |
| `remoteterminal_machine_name` | no | host name | Display name shown on the login page and terminal workspace header |
| `remoteterminal_listen_address` | yes | none | Specific non-loopback LAN IPv4 address |
| `remoteterminal_transport` | no | `http` | Plain HTTP appliance mode, or `https` when trusted TLS is available |
| `remoteterminal_port` | no | `8443` | Unprivileged listener port |
| `remoteterminal_max_sessions` | no | `8` | Maximum managed tmux sessions |
| `remoteterminal_max_code_servers` | no | `2` | Maximum active code-server instances; accepted range is 1–8 |
| `remoteterminal_tls_cert_source` | no | generated | Existing certificate copied during installation |
| `remoteterminal_tls_key_source` | no | generated | Matching private key copied during installation |
| `remoteterminal_generated_tls_days` | no | `825` | Lifetime of a generated self-signed certificate |
| `remoteterminal_generated_tls_renew_before_seconds` | no | `2592000` | Regenerate or reject TLS material within this validity window |
| `remoteterminal_idle_timeout` | no | `30m` | Browser-session idle timeout; accepts a Go duration |
| `remoteterminal_absolute_timeout` | no | `12h` | Browser-session maximum lifetime; accepts a Go duration |
| `remoteterminal_remember_timeout` | no | `720h` | Fixed lifetime for a **Remember me** session; accepts a Go duration |
| `remoteterminal_require_verified_pam_authentication` | no | `false` | Require a current interactive PAM-verification marker before deployment continues |
| `remoteterminal_validate_listen_address_is_local` | no | `true` | Require the selected IPv4 address to appear in host facts |

Certificate and key source variables must be supplied together in HTTPS mode and must be empty in HTTP mode. Plain HTTP protects neither the Linux password nor the resulting session from LAN interception. Normal code-server webviews require the exact origin to be allowlisted as secure in each managed Chromium client.
Leaving the timeout overrides empty omits them from the installed environment file, so the backend applies the effective defaults shown above: 30 minutes idle, 12 hours absolute, and 30 days for **Remember me**.

### Browser configuration for HTTP webviews

For the deployed endpoint in this repository, create a managed browser policy on every Chromium client that opens code-server. Use the exact origin, including scheme and port; do not use a wildcard:

```json
{
  "OverrideSecurityRestrictionsOnInsecureOrigin": [
    "http://dominant.int:8443/"
  ]
}
```

On Linux, save this JSON as a root-owned file such as `/etc/opt/chrome/policies/managed/remoteterminal.json` for Google Chrome or `/etc/chromium/policies/managed/remoteterminal.json` for Chromium. Restart every browser process, then confirm the policy is active in `chrome://policy`. If clients use the IP address instead of `dominant.int`, add that exact origin as a separate list entry.

For Firefox, open `about:config`, create the String preference `dom.securecontext.allowlist`, and set its comma-separated hostname list to `dominant.int` (no scheme or port). Restart Firefox. This exception applies to HTTP on every port for that hostname and can affect mixed-content upgrading, so the port-scoped Chromium policy is preferable on a managed programming station.

Both configurations deliberately grant secure-context APIs to plaintext content; use them only on the isolated trusted machine LAN.

### PAM verification checkpoint

Every install runs a non-secret PAM account-management probe as the exact systemd service account. Full password verification is deliberately interactive so a real password never enters Ansible variables, logs, environment variables, files, or process arguments.

After the helper has been installed, run this from a trusted local terminal:

```bash
sudo /usr/local/sbin/remoteterminal-pam-check
```

The helper invokes `pamtester` as the selected account, asks PAM for the password on the terminal, and performs both `authenticate` and `acct_mgmt`. On success it writes a root-only fingerprint marker at `/etc/remoteterminal/pam-authentication.verified`; the marker is tied to the selected user and effective PAM policy, and becomes invalid when those inputs change.

To make this checkpoint a hard installation gate, append the following to the install command and rerun it after the helper succeeds:

```bash
--extra-vars 'remoteterminal_require_verified_pam_authentication=true'
```

If strict mode is enabled on the first run, the playbook installs the helper and then stops at the gate. Run the helper locally and rerun the same playbook.

### TLS material and rotation

When HTTPS is selected, Ansible creates a self-signed RSA-3072 certificate with hostname, FQDN, and selected LAN-IP subject alternative names. It generates a new candidate when the requested TLS profile changes, the active material is invalid, or the certificate is within the renewal window (30 days by default).

When `remoteterminal_tls_cert_source` and `remoteterminal_tls_key_source` are supplied, Ansible copies and validates the pair. Changing either source file's contents on a later run triggers rotation. In both modes, the certificate/key match and validity window are checked before a complete versioned TLS set is activated atomically; a rejected candidate leaves the previous active set unchanged. A successful change marks the service for a controlled restart.

When `remoteterminal_transport=http`, TLS generation and validation are skipped. Existing managed TLS material is retained so switching back to HTTPS is reversible. Changing transport updates the service environment and performs one controlled Remote Terminal restart; it does not delete terminal tmux sessions.

## Workspace tabs and navigation

Open terminal and Code Server tabs can be given a local browser label with the pencil action, a double-click, or `F2`. The rename field is prefilled with a non-empty default, and custom labels are saved per signed-in user across tab closure, page reloads, and later logins. Renaming a browser tab does not rename or restart the underlying tmux session, folder, or Code Server.

On desktop, the hamburger button switches the left navigation between its expanded form and a compact icon-only strip; the preference is saved per user. On mobile, the same button continues to open the navigation drawer.

## Terminal sessions

- Create or open sessions from the vertical tab bar.
- Closing a browser tab detaches the view but leaves its tmux session running.
- Deleting a session is a separate confirmed operation and terminates that tmux session.
- Browser disconnects and logouts do not terminate tmux sessions.
- Sessions use an application-private tmux socket and do not affect normal tmux usage.
- Scroll the mouse wheel or trackpad to browse terminal output; tmux copy mode owns the session scrollback.

Managed tmux-session persistence in version 1 is limited to browser disconnects, tab closure, and logout while the deployed service remains running. Ordinary browser logins are held only in memory, while **Remember me** stores a token hash beneath `/var/lib/remoteterminal/auth` and survives service or machine restarts until its fixed deadline. Managed tmux sessions are still not guaranteed to survive a systemd service restart, upgrade, reboot, or uninstall. Save terminal work before those operations; workload restoration remains a future enhancement.

## Code-server workspaces

Choose **Launch Code Server**, browse from the configured account's home directory, and select a working folder. The picker can open any directory that account can traverse and read. Paths are canonicalized, so aliases and symbolic links that resolve to the same folder reuse and focus the existing instance instead of starting a duplicate. Each active workspace appears in the active-code-server list and in a visually distinct blue/cyan editor tab.

Each workspace runs in its own tmux session on the application-private code-server tmux socket. It uses an isolated persistent profile under `/var/lib/remoteterminal/code-server/profiles`, keyed by the canonical folder, so settings, extensions, and editor state do not leak between projects. On the first profile launch after this update, Remote Terminal selects code-server's **Dark Modern** theme while preserving unrelated JSONC settings; later manual theme choices are respected. The editor itself is served below its authenticated `/code/INSTANCE/` route; no code-server TCP listener or public upstream service is created.

- Closing an editor tab only detaches the browser view; the code-server keeps running.
- Logging out or closing the browser also leaves active code servers running.
- **Shutdown** is the explicit, confirmed action that stops an instance.
- A natural process exit removes the instance from the active list.
- Stopping or restarting `remoteterminal.service`, upgrading, rebooting, or uninstalling stops all active code servers. Per-folder profiles remain across those events unless application state is removed.
- Each active instance runs a separate editor process tree and consumes machine CPU and memory. Keep the configured limit appropriate for the LinuxCNC workstation; the default is two.

The code-server port-forwarding proxy is intentionally disabled. Its **Ports** view can still detect local listeners and offer notifications, but those links are not published through Remote Terminal. Set `remote.autoForwardPorts` to `false` in that workspace's Code Server settings to suppress automatic detection. Use separately administered LAN services when a project needs to expose another application port.

### Copy and paste

Remote Terminal now follows ttyd's native selection behavior. Hold `Shift` while dragging on Linux/Windows, or `Option` while dragging on macOS, and release the mouse. xterm leaves the selection highlighted and copies it immediately; there is no second **Copy selection** click, server request, tmux paste-buffer race, or retained copy-dialog text.

The modifier is required because tmux mouse handling remains enabled so wheel and trackpad scrolling continue to browse terminal history. If a managed browser blocks automatic copying, leave the highlight visible and use the browser's context-menu **Copy** command. `Cmd+C` can also copy the highlighted selection on macOS. `Ctrl+C` inside the terminal remains the normal interrupt key on Linux and Windows.

This path uses xterm's synchronous browser copy event, like stock ttyd, rather than the secure-context-only asynchronous Clipboard API. Clipboard reads, terminal-controlled OSC 52 clipboard writes, and iframe Clipboard API permission remain disabled.

To paste into the terminal, use `Ctrl+Shift+V` on Linux/Windows, `Shift+Insert` as the keyboard alternative, or `Cmd+V` on macOS. Browser security or managed workstation policy can still disable clipboard reads; in that case use the browser's context menu if it offers **Paste**.

## Local development

Required development dependencies are Go 1.26.5 or newer, Node.js/npm, PAM headers, tmux 3.2 or newer, ttyd, and code-server.

Build everything:

```bash
cd remoteterminal
./scripts/build.sh
```

Or use Make:

```bash
make test
make build
```

The backend expects configuration through environment variables. The installed systemd unit and Ansible role provide these in production, including `REMOTE_TERMINAL_TRANSPORT`, `REMOTE_TERMINAL_MACHINE_NAME`, `REMOTE_TERMINAL_CODE_SERVER_BINARY`, `REMOTE_TERMINAL_STATE_DIR`, and `REMOTE_TERMINAL_MAX_CODE_SERVERS`.

### Opt-in real process integration tests

The normal test suite skips real process integration. Run it only with an explicit enable flag and absolute binary paths:

```bash
REMOTETERMINAL_RUN_PROCESS_INTEGRATION=1 \
REMOTETERMINAL_TMUX_BINARY=/absolute/path/to/tmux \
REMOTETERMINAL_TTYD_BINARY=/absolute/path/to/ttyd \
go test -run '^TestManagerRealProcessLifecycle$' -count=1 -v ./internal/sessions
```

A dynamically linked ttyd extracted outside its normal system library layout may also need `LD_LIBRARY_PATH=/absolute/path/to/ttyd/libraries`. The test uses a short private temporary directory, a private managed tmux socket, and a separate unrelated-tmux canary. It exercises initialization, create/connect, HTTP and WebSocket proxying, ttyd's `tty` subprotocol, command execution, disconnect/reconnect persistence, deletion, and process/socket isolation.

The code-server lifecycle has a separate opt-in test using the same enable flag:

```bash
REMOTETERMINAL_RUN_PROCESS_INTEGRATION=1 \
REMOTETERMINAL_TMUX_BINARY=/absolute/path/to/tmux \
REMOTETERMINAL_CODE_SERVER_BINARY=/absolute/path/to/code-server \
go test -run '^TestCodeServerManagerRealProcessLifecycle$' -count=1 -v ./internal/codeservers
```

It launches code-server with the production arguments, verifies managed-config isolation, canonical-folder reuse, the instance limit, private socket permissions, HTTP proxying, graceful deletion, persistent profile state, shell-metacharacter safety, and isolation from an unrelated tmux server.

## Operations

Check service status and logs:

```bash
systemctl status remoteterminal.service
journalctl -u remoteterminal.service
```

Restart the web service:

```bash
sudo systemctl restart remoteterminal.service
```

Restarting the service invalidates ordinary in-memory browser logins but restores unexpired **Remember me** sessions from hashed private state. It cleanly stops active code servers; their per-folder profiles remain. A restart is still outside the terminal tmux-persistence guarantee. Re-run the install playbook to upgrade from the current source checkout. The role uses content-addressed application and dependency installs plus durable change markers, so unchanged inputs do not request a restart; target-machine second-run idempotence remains part of manual acceptance.

Uninstall with:

```bash
ansible-playbook \
  -i localhost, \
  --connection=local \
  --become \
  --extra-vars 'remoteterminal_user=linuxcnc' \
  remoteterminal/ansible/uninstall.yml
```

The uninstall playbook documents and reports which session or state data it preserves.

Uninstall always stops and disables the service, terminates only the application-private terminal and code-server tmux servers and their ttyd/code-server processes, removes the runtime directory, systemd unit, PAM policy, and PAM helper, and therefore does not preserve live Remote Terminal sessions. It leaves unrelated tmux servers, the selected user's home files, LinuxCNC configuration, and installed Debian dependency packages alone.

The following optional variables preserve otherwise application-owned files:

| Variable | Preserved when `true` |
| --- | --- |
| `remoteterminal_uninstall_preserve_installation` | `/opt/remoteterminal` releases and pinned Go/ttyd/code-server tools |
| `remoteterminal_uninstall_preserve_config` | `/etc/remoteterminal`, including TLS material and the PAM verification marker |
| `remoteterminal_uninstall_preserve_state` | `/var/lib/remoteterminal`, including per-folder code-server profiles and unexpired remembered-session hashes |
| `remoteterminal_uninstall_preserve_build_cache` | `/var/lib/remoteterminal-build` |

For example, add `--extra-vars 'remoteterminal_uninstall_preserve_config=true'` to preserve configuration and TLS material. All four flags default to `false`.

## Pending manual acceptance checklist

These checks are intentionally unchecked and must be performed on a clean AMD64 Debian/LinuxCNC target; they have not been inferred from local automated tests.

- [ ] Install from the setup TUI on a clean target; verify the selected transport and endpoint, certificate fingerprint in HTTPS mode, selected non-root service identity, selected-address listener, private ttyd/code-server sockets, and exact pinned Go/ttyd/code-server versions.
- [ ] Run the same install a second time with unchanged inputs and confirm an idempotent Ansible recap and no unnecessary service restart.
- [ ] Reboot, confirm the enabled service becomes healthy, and confirm that pre-reboot tmux sessions are not presented as guaranteed persistent state.
- [ ] In supported Chrome/Chromium and Firefox, create and reconnect sessions; verify normal-drag and Shift/Option-drag copy through the explicit dialog, its Ctrl/Cmd+C and context-menu paths, and Ctrl+Shift+V/Shift+Insert/Cmd+V paste for plain text, multiline text, Unicode, and Ukrainian text.
- [ ] Trust the deployment certificate on supported clients; launch two different folders, verify editor webviews/workers and extensions, confirm same-folder aliases deduplicate, and confirm the configured active-instance limit.
- [ ] Install with the default HTTP appliance profile; confirm the plaintext warning and non-`Secure` host-only session cookie, exact HTTP Origin/CSRF/WebSocket checks, failure of unconfigured remote-origin webviews, and successful webviews only after the exact origin is added to a disposable managed-browser insecure-origin policy.
- [ ] Close and reopen an editor tab and log out/in to verify the instance remains active; then use **Shutdown** and verify it disappears without affecting terminal or unrelated tmux sessions.
- [ ] Restart the service and reboot with active editors; verify their processes stop, no zombies remain, and their isolated per-folder profiles are retained.
- [ ] Verify PAM login with the selected account's correct password, a wrong password, a wrong username, and a locked/expired test account where available; confirm failures remain generic and credentials do not appear in logs.
- [ ] Uninstall with default flags, confirm Remote Terminal sessions and owned paths are removed, and confirm unrelated tmux, home files, LinuxCNC configuration, firewall policy, and dependency packages remain untouched.
- [ ] Repeat uninstall as needed with each preservation override and confirm only the requested installation, config/TLS, state, or build-cache path remains.

## Design and acceptance criteria

See [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) for the implemented architecture, current version 1 decisions, verification coverage, and remaining acceptance criteria.
