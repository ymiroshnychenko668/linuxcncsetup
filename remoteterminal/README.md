# Remote Terminal

Remote Terminal provides authenticated browser access to persistent tmux sessions on a LinuxCNC workstation. A Go service authenticates the configured local account through PAM, serves the React application, manages tmux and ttyd, and proxies ttyd over authenticated same-origin routes.

The first supported deployment target is Debian/LinuxCNC on AMD64.

## Implementation status

The version 1 application, React frontend, Ansible source-build/deployment role, setup-menu entry, systemd unit, PAM policy and verifier, TLS management, and uninstall path are implemented in this repository. Backend and frontend automated tests are included, together with a strictly opt-in real tmux/ttyd process integration test.

Clean-machine Debian/LinuxCNC installation, browser behavior, reboot, and uninstall still require the target acceptance checks listed below. This document does not claim those target-only checks have been run.

## Security model

Remote Terminal grants the browser the same shell access as the configured Linux account. It is intended for a trusted LAN and must not be exposed directly to the public internet.

- The service and terminal processes run as one configured non-root account.
- The account's current password is checked once through PAM and is never stored.
- Browser authentication uses a secure server-side session, CSRF protection, and same-origin checks.
- The Go service exposes HTTPS; plain HTTP is not supported for system-password login.
- ttyd listens only on private Unix-domain sockets.
- A dedicated tmux socket keeps application sessions separate from the user's other tmux sessions.

The installer binds only the selected non-loopback LAN IPv4 address. It does not modify host, router, or network firewalls; those controls remain the operator's responsibility and define which LAN clients can reach the service.

The generated certificate encrypts traffic but is not automatically trusted by client devices. Verify the SHA-256 fingerprint displayed during installation before accepting or importing it. A certificate and key issued by an existing trusted CA can be supplied to Ansible instead.

## Installation from the TUI

Use the LinuxCNC Setup TUI application and choose **Install Remote Terminal (Ansible)**. The TUI asks for a machine display name, the local system user, LAN IPv4 address, and HTTPS port, then invokes `ansible/install.yml` directly. The host name is offered as the default machine name.

The repository-root `setup.sh` shell menu is deprecated. It remains functional for compatibility but should not be used for new installations or updates.

The installer builds the React application, Go service, and a pinned ttyd revision from source. Network access is initially required for Debian packages and source dependencies.

The default HTTPS port is `8443`. After installation, open:

```text
https://LAN_ADDRESS:8443/
```

Log in with the selected system username and its current password.

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
| `remoteterminal_port` | no | `8443` | Unprivileged HTTPS port |
| `remoteterminal_max_sessions` | no | `8` | Maximum managed tmux sessions |
| `remoteterminal_tls_cert_source` | no | generated | Existing certificate copied during installation |
| `remoteterminal_tls_key_source` | no | generated | Matching private key copied during installation |
| `remoteterminal_generated_tls_days` | no | `825` | Lifetime of a generated self-signed certificate |
| `remoteterminal_generated_tls_renew_before_seconds` | no | `2592000` | Regenerate or reject TLS material within this validity window |
| `remoteterminal_idle_timeout` | no | `30m` | Browser-session idle timeout; accepts a Go duration |
| `remoteterminal_absolute_timeout` | no | `12h` | Browser-session maximum lifetime; accepts a Go duration |
| `remoteterminal_require_verified_pam_authentication` | no | `false` | Require a current interactive PAM-verification marker before deployment continues |
| `remoteterminal_validate_listen_address_is_local` | no | `true` | Require the selected IPv4 address to appear in host facts |

Certificate and key source variables must be supplied together.
Leaving the two timeout overrides empty omits them from the installed environment file, so the backend applies the effective defaults shown above: 30 minutes idle and 12 hours absolute.

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

Generated TLS is the version 1 default. Ansible creates a self-signed RSA-3072 certificate with hostname, FQDN, and selected LAN-IP subject alternative names. It generates a new candidate when the requested TLS profile changes, the active material is invalid, or the certificate is within the renewal window (30 days by default).

When `remoteterminal_tls_cert_source` and `remoteterminal_tls_key_source` are supplied, Ansible copies and validates the pair. Changing either source file's contents on a later run triggers rotation. In both modes, the certificate/key match and validity window are checked before a complete versioned TLS set is activated atomically; a rejected candidate leaves the previous active set unchanged. A successful change marks the service for a controlled restart.

## Terminal sessions

- Create or open sessions from the vertical tab bar.
- Closing a browser tab detaches the view but leaves its tmux session running.
- Deleting a session is a separate confirmed operation and terminates that tmux session.
- Browser disconnects and logouts do not terminate tmux sessions.
- Sessions use an application-private tmux socket and do not affect normal tmux usage.
- Scroll the mouse wheel or trackpad to browse terminal output; tmux copy mode owns the session scrollback.

Persistence in version 1 is limited to browser disconnects, tab closure, and logout while the deployed service remains running. Browser authentication sessions are held in memory, and managed tmux sessions are not guaranteed to survive a systemd service restart, an upgrade that restarts the service, a machine reboot, or uninstall. Save work before those operations; reboot persistence is a future enhancement.

### Copy and paste

Select terminal text and use `Ctrl+Shift+C` to copy. Because tmux handles mouse scrolling, hold `Shift` while dragging when the browser needs to select text directly. Use `Ctrl+Shift+V` or `Shift+Insert` to paste. Clipboard access requires the HTTPS origin and may prompt for browser permission.

## Local development

Required development dependencies are Go, Node.js/npm, PAM headers, tmux, and ttyd.

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

The backend expects configuration through environment variables. The installed systemd unit and Ansible role provide these in production, including `REMOTE_TERMINAL_MACHINE_NAME` for the display name shown in the browser.

### Opt-in real tmux/ttyd integration test

The normal test suite skips real process integration. Run it only with an explicit enable flag and absolute binary paths:

```bash
REMOTETERMINAL_RUN_PROCESS_INTEGRATION=1 \
REMOTETERMINAL_TMUX_BINARY=/absolute/path/to/tmux \
REMOTETERMINAL_TTYD_BINARY=/absolute/path/to/ttyd \
go test -run '^TestManagerRealProcessLifecycle$' -count=1 -v ./internal/sessions
```

A dynamically linked ttyd extracted outside its normal system library layout may also need `LD_LIBRARY_PATH=/absolute/path/to/ttyd/libraries`. The test uses a short private temporary directory, a private managed tmux socket, and a separate unrelated-tmux canary. It exercises initialization, create/connect, HTTP and WebSocket proxying, ttyd's `tty` subprotocol, command execution, disconnect/reconnect persistence, deletion, and process/socket isolation.

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

Restarting the service invalidates in-memory browser logins and is outside the version 1 tmux-persistence guarantee. Re-run the install playbook to upgrade from the current source checkout. The role uses content-addressed releases and durable change markers so unchanged inputs do not request a restart; target-machine second-run idempotence remains part of manual acceptance.

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

Uninstall always stops and disables the service, terminates only the application-private tmux server and ttyd processes, removes the runtime directory, systemd unit, PAM policy, and PAM helper, and therefore does not preserve live Remote Terminal sessions. It leaves unrelated tmux servers, the selected user's home files, LinuxCNC configuration, and installed Debian dependency packages alone.

The following optional variables preserve otherwise application-owned files:

| Variable | Preserved when `true` |
| --- | --- |
| `remoteterminal_uninstall_preserve_installation` | `/opt/remoteterminal` releases and tools |
| `remoteterminal_uninstall_preserve_config` | `/etc/remoteterminal`, including TLS material and the PAM verification marker |
| `remoteterminal_uninstall_preserve_state` | `/var/lib/remoteterminal` |
| `remoteterminal_uninstall_preserve_build_cache` | `/var/lib/remoteterminal-build` |

For example, add `--extra-vars 'remoteterminal_uninstall_preserve_config=true'` to preserve configuration and TLS material. All four flags default to `false`.

## Pending manual acceptance checklist

These checks are intentionally unchecked and must be performed on a clean AMD64 Debian/LinuxCNC target; they have not been inferred from local automated tests.

- [ ] Install from the setup TUI on a clean target, verify the reported certificate fingerprint, HTTPS health, selected non-root service identity, selected-address listener, and private ttyd sockets.
- [ ] Run the same install a second time with unchanged inputs and confirm an idempotent Ansible recap and no unnecessary service restart.
- [ ] Reboot, confirm the enabled service becomes healthy, and confirm that pre-reboot tmux sessions are not presented as guaranteed persistent state.
- [ ] In supported Chrome/Chromium and Firefox, create and reconnect sessions; verify copy/paste of plain text, multiline text, Unicode, and Ukrainian text and keyboard input using the documented shortcuts.
- [ ] Verify PAM login with the selected account's correct password, a wrong password, a wrong username, and a locked/expired test account where available; confirm failures remain generic and credentials do not appear in logs.
- [ ] Uninstall with default flags, confirm Remote Terminal sessions and owned paths are removed, and confirm unrelated tmux, home files, LinuxCNC configuration, firewall policy, and dependency packages remain untouched.
- [ ] Repeat uninstall as needed with each preservation override and confirm only the requested installation, config/TLS, state, or build-cache path remains.

## Design and acceptance criteria

See [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) for the implemented architecture, current version 1 decisions, verification coverage, and remaining acceptance criteria.
