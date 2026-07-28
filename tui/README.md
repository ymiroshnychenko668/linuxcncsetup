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

## Git setup

The main-menu **Git setup** submenu appears immediately after **Install
Ansible** and keeps source-control authentication separate from the broader
developer-tools installer:

- **Install Git tools** runs an embedded Ansible playbook that installs missing
  `git`, `openssh-client`, and `gh` packages and sets the global identity to
  `cnc <cnc@cnc.cn>`. It does not create, replace, recover, display, or upload
  SSH keys.
- **Sign in to GitHub with gh** suspends the TUI and runs GitHub CLI's
  interactive web login for `github.com` with the SSH Git protocol. GitHub CLI
  owns the prompts for selecting, creating, or uploading a key; Ansible never
  writes the key. The action must be run without `sudo` so the OAuth token and
  any GitHub CLI configuration belong to the desktop user.

The login action ignores inherited `GH_TOKEN`, `GITHUB_TOKEN`, enterprise-token,
host, config-directory, and prompt-disabling variables so a stale headless
credential cannot silently replace the requested interactive login. Existing
SSH files are left untouched unless the user explicitly approves an action in
GitHub CLI's own prompts.

## Developer tools installation

The **Development tools** submenu uses one embedded, selection-aware Ansible
playbook. **Install all** runs the complete TUI workflow, while the remaining
items can be run independently:

- **Visual Studio Code** adds Microsoft's signed amd64 APT repository and
  installs VS Code.
- **Codex CLI** downloads and runs
  [OpenAI's official standalone installer](https://chatgpt.com/codex/install.sh)
  as the regular target user. It installs the `codex` command in
  `~/.local/bin`; running `codex` for the first time asks the user to choose
  Sign in with ChatGPT or another available authentication method. The
  state-present action skips the installer when that command already exists.
- **Claude Code** downloads and runs
  [Anthropic's official native installer](https://claude.ai/install.sh) as the
  regular target user. It installs the `claude` command in `~/.local/bin` and
  uses Claude Code's native automatic-update mechanism. Run `claude` after
  installation and follow the available authentication flow; installation
  alone does not grant Claude Code service access.
- **Warp Terminal** downloads Warp's
  [official architecture-specific Debian package](https://docs.warp.dev/getting-started/quickstart/installation-and-setup)
  and installs it without refreshing the machine's global APT indexes. The
  package configures Warp's signed update repository and signing key. Launch
  `warp-terminal` after installation; the first launch needs an internet
  connection, while creating or signing in to a Warp account is optional.
  Online AI and collaboration features still require connectivity and the
  applicable account access. Warp requires glibc 2.31+ and graphics hardware
  with OpenGL ES 3.0+ or Vulkan support.
- **htop**, **Midnight Commander**, and **Terminator** each install only their
  selected Debian package.
- **User lingering** enables lingering for the target user without installing
  a developer package.

Codex CLI and Claude Code are independent per-user installations and do not
use a system-wide npm installation. Warp's signing key, APT source, and package
are system-wide; as with the other Debian packages and repositories, only
those system configuration tasks run with elevated privileges. User-specific
files are created as the regular target user.

The independent Codex CLI, Claude Code, and Warp Terminal items support both
x86-64 and ARM64. The VS Code component—and therefore **Install all**—remains
x86-64-only, while every other independent component is available on ARM64.
The root `install-devtools.sh` remains a legacy compatibility script and is
not extended by these TUI-only items.

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

The main-menu **Install CorvusCNC config** item, immediately after **Git
setup**, runs an embedded Ansible playbook that installs Git or OpenSSH only
when its executable is missing, creates the target user's
`~/linuxcnc/configs` directory, and clones
`git@github.com:ymiroshnychenko668/corvuscnc.git` directly to
`~/linuxcnc/configs/corvuscnc`. The repository root is the ready-to-use
LinuxCNC configuration directory; no additional nesting or copy step is
introduced.

The clone runs as the regular target user. It reuses the current SSH agent
socket when one is available and otherwise uses that user's on-disk GitHub SSH
configuration. Run **Git setup → Install Git tools** and then **Git setup →
Sign in to GitHub with gh** before cloning when GitHub access has not already
been configured.
An existing checkout is verified as the expected repository and left
unchanged, preserving local machine configuration. An unrelated file,
directory, or Git checkout at the destination causes the playbook to stop
without overwriting it.

The playbook verifies that the installed repository root contains at least one
`.ini` file. It does not launch LinuxCNC or enable autostart. The existing
LinuxCNC autostart scanner discovers the installed INI files when that menu is
opened. The root-level `clonelinuxcncconfig.sh` remains as a compatibility
script.

## LinuxCNC autostart playbooks

The **LinuxCNC autostart** menu first selects **Sway (Wayland)** or **XFCE
(X11)**. Both paths recursively list the current user's
`~/linuxcnc/configs/*.ini` files by INI basename and pass the selected absolute
path to a desktop-specific embedded Ansible playbook.

The Sway playbook validates the account, LinuxCNC tools, Sway configuration,
and selected QtVCP INI. It then installs a safely quoted user wrapper and a
managed Sway snippet under `~/.config/sway/config.d/`. At the next Sway login,
the wrapper selects workspace 1 immediately before launching LinuxCNC; it does
not launch LinuxCNC or reload Sway during setup. The Waybar **CNC** button uses
the same wrapper: when a QtVCP window is already open it focuses that window
and returns to workspace 1; otherwise it selects workspace 1 and starts the
configured LinuxCNC profile.

The XFCE X11 playbook validates the installed XFCE X11 session and selected
QtVCP INI, installs the small `wmctrl` window-control dependency, and writes:

- `~/.local/bin/linuxcnc-autostart-x11`
- `~/.config/autostart/linuxcncsetup-linuxcnc-x11.desktop`

XFCE consumes the standard XDG autostart entry at the next login. The launcher
uses a per-user runtime lock, avoids starting a second LinuxCNC process when
one is already opening, selects workspace 1, and moves, focuses, and
fullscreens the QtVCP window. Because XFCE uses the same desktop identifier for
its X11 and Wayland sessions, the launcher explicitly exits without doing
anything unless `XDG_SESSION_TYPE=x11`.

The Sway and XFCE files are separate and can coexist. Selecting another INI
for either desktop updates that desktop's managed files idempotently. Neither
playbook launches LinuxCNC or changes the current desktop session during
setup.

Automatic LinuxCNC startup can initialize connected machine hardware. The TUI
therefore requires a separate confirmation after the configuration is
selected.

## SMB mount management

**Configuration → SMB mounts** manages the existing
`//10.0.1.246/share` guest share at `/mnt/smb_share` through an embedded
Ansible playbook:

- **Mount SMB share** installs `cifs-utils` when needed, adds a marked
  LinuxCNC Setup block to `/etc/fstab`, enables systemd automounting, and mounts
  the share immediately.
- **Unmount SMB share** stops the automount unit before performing a normal
  unmount. It retains the persistent entry, so the share becomes available
  again after the next reboot.
- **Remove SMB mount** performs the same safe unmount and removes only the
  marked fstab block owned by LinuxCNC Setup. It refuses to delete unrelated or
  legacy entries.

The workflow never force-unmounts a busy share and does not create a remote
write-test file. The root-level SMB shell scripts remain compatibility tools;
the Go TUI does not invoke them.

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

- **LightDM:** installs LightDM without starting it, verifies its daemon and
  `lightdm-autologin` PAM service, and configures the selected regular user
  through `/etc/lightdm/lightdm.conf.d/50-linuxcnc-autologin.conf`. It checks
  LightDM's effective configuration, records it as Debian's default display
  manager, disables greetd for the next boot, unmasks LightDM, and enables its
  `display-manager.service` alias. The selected/default desktop session is
  preserved.
- **Sway:** installs Sway, greetd, and tuigreet, then configures one automatic
  Sway login per boot. Select it only after a manual Sway login has succeeded.

Neither mode starts, stops, or restarts a display manager during the current
session. Service selection takes effect after reboot.

The root-level `autologin.sh` remains as a compatibility launcher. Its Sway
mode requires `LINUXCNCSETUP_SWAY_VALIDATED=1` to acknowledge the same
precondition.

The **Reboot system** menu item uses `systemctl reboot` after an explicit
save-work confirmation and hands the terminal to `sudo` when elevation is
required.
