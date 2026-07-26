# LinuxCNC Setup TUI

Greenfield terminal interface for LinuxCNC workstation setup and diagnostics.

## Automatic installation

The installer uses an existing Go 1.25+ toolchain when available. Otherwise,
it downloads a checksum-verified Go toolchain into the current user's local
data directory. It never requires `sudo`.

```bash
./install.sh
~/.local/bin/linuxcncsetup
```

Install and launch immediately:

```bash
./install.sh --run
```

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
