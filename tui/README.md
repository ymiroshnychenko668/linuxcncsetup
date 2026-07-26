# LinuxCNC Setup TUI

Greenfield terminal interface for LinuxCNC workstation setup and diagnostics.

## Automatic installation

The installer uses an existing Go 1.25+ toolchain when available. Otherwise,
it downloads a checksum-verified Go toolchain into the current user's local
data directory. It never requires `sudo`.

```bash
./tui/install.sh
~/.local/bin/linuxcncsetup
```

Install and launch immediately:

```bash
./tui/install.sh --run
```

These commands are shown from the repository root. From inside `tui/`, omit
the `tui/` prefix.

## Requirements

- Linux on x86-64 or ARM64
- `curl` or `wget`
- `tar` and `sha256sum`

Manual development requires Go 1.25 or newer.

## Development

```bash
go run ./cmd/linuxcncsetup
go test ./...
CGO_ENABLED=0 go build -trimpath -o bin/linuxcncsetup ./cmd/linuxcncsetup
```

The initial scaffold uses Bubble Tea v2 for the application loop and Lip Gloss
v2 for terminal layout and styling.

## Developer tools installation

The **Development tools** submenu implements the root-level
`install-devtools.sh` workflow through one embedded, selection-aware Ansible
playbook. **Install all** preserves the combined workflow, while the remaining
items can be run independently:

- **Git & GitHub SSH** installs Git and OpenSSH, sets the global identity to
  `cnc <cnc@cnc.cn>`, and safely creates or recovers the Ed25519 public key.
  It never overwrites an existing private key and waits after displaying the
  public key so it can be copied to GitHub.
- **Visual Studio Code** adds Microsoft's signed amd64 APT repository and
  installs VS Code.
- **htop**, **Midnight Commander**, and **Terminator** each install only their
  selected Debian package.
- **User lingering** enables lingering for the target user without installing
  a developer package.

User-specific files are created as the regular target user; only package and
system configuration tasks run with elevated privileges. The VS Code
component—and therefore **Install all**—requires x86-64, while the other
components remain available on ARM64. The root `install-devtools.sh` remains
the all-in-one compatibility script.

## Wayland and Sway installation

The **Install Sway** action runs the embedded `install-sway.yml`
playbook. It implements the additive phases from the repository's
`sway-wayland-migration.md`:

- saves package baselines without overwriting earlier snapshots;
- simulates and installs the complete Sway desktop package set;
- backs up and manages Sway, Waybar, mako, portal, environment, and user
  systemd configuration;
- prepares PipeWire for future logins without stopping the current audio
  session; and
- validates the Sway and Waybar configurations.

It does not install greetd, change the active display manager, remove
XFCE/Xorg, or reboot. A real manual Sway login remains the safety gate before
selecting Sway automatic login.

## CorvusCNC configuration installation

The main-menu **Install CorvusCNC config** item, immediately after **Install
Ansible**, runs an embedded Ansible playbook that installs Git or OpenSSH only
when its executable is missing, creates the target user's
`~/linuxcnc/configs` directory, and clones
`git@github.com:ymiroshnychenko668/corvuscnc.git` directly to
`~/linuxcnc/configs/corvuscnc`. The repository root is the ready-to-use
LinuxCNC configuration directory; no additional nesting or copy step is
introduced.

The clone runs as the regular target user. It reuses the current SSH agent
socket when one is available and otherwise uses that user's on-disk GitHub SSH
configuration. Run **Development tools → Git & GitHub SSH** first and add the
displayed public key to GitHub when SSH access has not already been configured.
An existing checkout is verified as the expected repository and left
unchanged, preserving local machine configuration. An unrelated file,
directory, or Git checkout at the destination causes the playbook to stop
without overwriting it.

The playbook verifies that the installed repository root contains at least one
`.ini` file. It does not launch LinuxCNC or enable autostart. The existing
LinuxCNC autostart scanner discovers the installed INI files when that menu is
opened. The root-level `clonelinuxcncconfig.sh` remains as a compatibility
script.

## LinuxCNC autostart playbook

The **LinuxCNC autostart** menu first selects **Sway (Wayland)** or **X11**.
X11 is intentionally left as an unimplemented placeholder. The Sway path
recursively lists the current user's `~/linuxcnc/configs/*.ini` files by INI
basename and passes the selected absolute path to the embedded
`linuxcnc-autostart.yml` playbook.

The playbook validates the account, LinuxCNC tools, Sway configuration, and
the selected QtVCP INI. It then installs a safely quoted user wrapper and a
managed Sway snippet under `~/.config/sway/config.d/`. At the next Sway login,
the wrapper selects workspace 1 immediately before launching LinuxCNC; it does
not launch LinuxCNC or reload Sway during setup. Selecting another INI updates
the same managed files idempotently.

Automatic LinuxCNC startup can initialize connected machine hardware. The TUI
therefore requires a separate confirmation after the configuration is
selected.

## IRQ affinity playbook

The first **Configuration → IRQ affinity** item is a device-oriented view of
the real `/proc/interrupts` table. It displays every current IRQ vector,
per-CPU counters, totals, and the requested and effective affinity exposed by
the kernel. Read-only rows such as NMI and ERR remain visible as kernel
counters.

PCI MSI/MSI-X vectors are grouped by PCI BDF, so all queues for one NVMe
controller are edited together while separate USB controllers remain separate.
Safe non-PCI interrupts use an exact kernel action name. Shared or ambiguous
identities remain visible but read-only.

After selecting a device, the CPU editor offers three distinct operations:

- **Preview persistent rule** runs Ansible check mode.
- **Save for next boot** records a stable device selector and CPU list without
  touching live affinity.
- **Apply to device now** resolves the selected device again and writes only
  its currently matching IRQ vectors after a separate confirmation.

Numeric IRQ identifiers are diagnostic only and are never persisted. The live
operation uses a private, temporary root-owned helper and removes it afterward;
it does not install or modify the boot policy.

The optional **Default IRQ policy** assigns every online CPU to either a
protected LinuxCNC real-time role or a housekeeping/IRQ role. It can coexist
with device-specific rules, whose affinities override the default for their
matching vectors. The status view reports CPU isolation, default affinity,
irqbalance, the saved rules, and the latest boot result.

LinuxCNC and its RT processes must be stopped before a policy is installed or a
device is changed live. If irqbalance is active, the playbook stops with an
explanation instead of silently disabling it. **Disable managed tuning**
removes only files owned by LinuxCNC Setup and leaves live IRQs unchanged until
reboot. Timestamped Ansible safety backups from earlier overwrites are retained.

## Automatic-login playbook

The **Automatic login** submenu offers two modes. The TUI embeds
`internal/playbooks/autologin.yml` and launches it after confirmation by
running `ansible-playbook` through `sudo`:

- **LightDM:** preserves the selected/default desktop session and configures
  automatic login through a LightDM drop-in.
- **Sway:** installs Sway, greetd, and tuigreet, then configures one automatic
  Sway login per boot. Select it only after a manual Sway login has succeeded.

Neither mode starts or stops a display manager during the current session.
Service selection takes effect after reboot.

The root-level `autologin.sh` remains as a compatibility launcher. Its Sway
mode requires `LINUXCNCSETUP_SWAY_VALIDATED=1` to acknowledge the same
precondition.

The **Reboot system** menu item uses `systemctl reboot` after an explicit
save-work confirmation and hands the terminal to `sudo` when elevation is
required.
