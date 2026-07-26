# Wayland + Sway migration notes

Migration of this machine's graphical stack from **Xorg + XFCE (LightDM)** to
**Wayland + Sway**.

- **Host:** `debian` — Debian GNU/Linux 13.2 (trixie), amd64
- **Started:** 2026-07-26
- **Status:** installation and configuration complete; **X11/XFCE not yet removed**

---

## 1. Status summary

| Phase | Description | State |
| ----- | ----------- | ----- |
| 0 | Rollback snapshots + recovery paths | Done |
| 1 | Install Wayland/Sway stack (additive) | Done |
| 2 | Write Sway / Waybar / mako configs | Done |
| 3 | First real Sway login + validation | **Pending — needs manual login** |
| 4 | Switch greeter to greetd + tuigreet | Not started |
| 5 | Purge Xorg + XFCE | Not started |
| 6 | Final verification after reboot | Not started |

Nothing destructive has happened yet. XFCE + Xorg + LightDM are all still
installed and still the active session, so the machine boots exactly as before.

---

## 2. Why Wayland works well on this hardware

Checks performed before installing anything:

- **GPU:** AMD Granite Ridge integrated Radeon (`1002:13c0`), kernel driver
  `amdgpu` with KMS active, `/dev/dri/card0` + `renderD128` present.
  wlroots/Sway run natively here — no `--unsupported-gpu` workaround is needed
  (that is only required for the proprietary NVIDIA driver).
- The `firmware-nvidia-graphics` / `firmware-nvidia-tesla-535-gsp` packages
  present on the system are unused firmware blobs, not the NVIDIA driver.
- **Display:** single panel on connector `DP-1` at 1920x1080. `HDMI-A-1` is
  free/disconnected. Note Sway uses DRM connector names (`DP-1`), which differ
  from the older Xorg naming (`DisplayPort-0`).
- **Warp terminal compatibility:** `/opt/warpdotdev/warp-terminal/warp`
  `dlopen`s both `libwayland-client.so.0` and `libX11.so.6`, so it runs
  natively on Wayland (and could fall back to XWayland).

---

## 3. Phase 0 — Safety net

Rollback snapshots written to the home directory:

- `~/pkg-selections-pre-sway.txt` — `dpkg --get-selections` (1784 entries)
- `~/apt-manual-pre-sway.txt` — `apt-mark showmanual` (244 entries)
- `~/dpkg-l-pre-sway.txt` — full `dpkg -l` listing

Recovery channels confirmed available before any change:

- `sshd` active on port 22, machine reachable at `192.168.88.201`
- `getty@tty1` running; other VTs reachable with Ctrl+Alt+F2..F6
- `dpkg -C` clean (no half-configured packages)

---

## 4. Phase 1 — Packages installed

Installed with `apt-get install` — **69 new packages, 0 removed** (purely
additive, verified first with `apt-get -s install`).

Explicitly requested:

| Package | Version | Purpose |
| ------- | ------- | ------- |
| `sway` | 1.10.1-2 | Wayland compositor (wlroots) |
| `swaybg` | 1.2.1-1 | Wallpaper / background |
| `swayidle` | 1.8.0-1 | Idle management |
| `swaylock` | 1.8.2-1 | Screen locker |
| `waybar` | 0.12.0-1 | Status bar |
| `foot` | 1.21.0-2 | Fallback terminal |
| `wofi` | 1.4.1-1+b2 | Application launcher |
| `xwayland` | 2:24.1.6-1 | X11 app compatibility (kept permanently) |
| `mako-notifier` | 1.10.0-1 | Notification daemon |
| `grim` | 1.4.0+ds-2+b1 | Screenshots |
| `slurp` | 1.5.0-1 | Region selection |
| `wl-clipboard` | 2.2.1-2 | Clipboard (`wl-copy` / `wl-paste`) |
| `brightnessctl` | 0.5.1-3.1 | Backlight control |
| `lxpolkit` | 0.5.6-2 | Polkit authentication agent |
| `qt6-wayland` | 6.8.2-4 | Qt6 Wayland backend |
| `xdg-desktop-portal-wlr` | 0.7.1-2 | Screen sharing / screencast portal |
| `xdg-desktop-portal-gtk` | 1.15.3-1 | File chooser portal (already present) |
| `fonts-font-awesome` | 4.7.0 | Waybar glyphs (already present) |
| `libnotify-bin` | 0.8.6-1 | `notify-send` (already present) |

Notable dependencies pulled in automatically: `pipewire` 1.4.2-1,
`pipewire-pulse`, `wireplumber` 0.5.8-2, `libwlroots-0.18`, `libseat1`,
and the Qt6 runtime libraries.

**`greetd` / `tuigreet` were deliberately NOT installed yet.** Installing them
now risks debconf reassigning the default display manager while LightDM is
still needed to test-login to Sway in Phase 3. They come in Phase 4.

---

## 5. Phase 2 — Configuration files created

| File | Purpose |
| ---- | ------- |
| `~/.config/sway/config` | Main compositor config |
| `~/.config/waybar/config` | Status bar modules (JSON) |
| `~/.config/waybar/style.css` | Bar styling; colors as `@define-color` globals |
| `~/.config/mako/config` | Notification appearance |
| `~/.config/systemd/user/sway-session.target` | Starts Waybar/mako with Sway |
| `~/.config/environment.d/90-wayland.conf` | Wayland env vars for user services |
| `~/.profile` | Same env vars, guarded block (see below) |

### 5.1 Sway config highlights

- `$mod` = `Mod4` (Super key).
- `$term` = `warp-terminal` (**Super+Return**), with `foot` on
  **Super+Shift+Return** as a deliberate escape hatch if Warp ever fails.
- `$menu` = `wofi --show drun -i` (**Super+d**).
- Output: `output DP-1 resolution 1920x1080 position 0,0`.
- Background: `output * bg #1e1e2e solid_color` — a solid color rather than an
  image, because the `sway-backgrounds` package is not installed and pointing at
  a nonexistent file would error.
- Idle: `swayidle` powers outputs off after 600s and wakes them without a
  password. It still locks before sleep; manual locking remains available.
- Autostarted: `lxpolkit` (polkit prompts) and `nm-applet --indicator`
  (network tray).
- Screenshots: `Print` = whole screen to clipboard,
  `Shift+Print` = selected region to clipboard.

### 5.2 Why there is no `bar {}` block

The `waybar` and `mako-notifier` packages install systemd **user** units that
are `WantedBy=graphical-session.target`, and `waybar.service` additionally
declares `Requisite=graphical-session.target`. But systemd ships
`graphical-session.target` with `RefuseManualStart=yes`, so it cannot be
started directly and nothing else was activating it under Sway.

Solution: `~/.config/systemd/user/sway-session.target` declares
`BindsTo=graphical-session.target`, which pulls that target in legitimately as
a dependency. The Sway config runs:

```
exec_always systemctl --user import-environment WAYLAND_DISPLAY XDG_CURRENT_DESKTOP XDG_SESSION_TYPE SWAYSOCK
exec_always dbus-update-activation-environment --systemd WAYLAND_DISPLAY XDG_CURRENT_DESKTOP XDG_SESSION_TYPE SWAYSOCK
exec systemctl --user start sway-session.target
```

Because Waybar therefore runs as a systemd service, the Sway config
intentionally contains **no `bar {}` block** — adding one would spawn a second,
overlapping swaybar.

### 5.3 Environment variables

Set in both `~/.config/environment.d/90-wayland.conf` (for systemd user
services) and `~/.profile` (for login-shell launched sessions, which is how
greetd will start Sway in Phase 4):

```
XDG_CURRENT_DESKTOP=sway
QT_QPA_PLATFORM=wayland;xcb
QT_WAYLAND_DISABLE_WINDOWDECORATION=1
MOZ_ENABLE_WAYLAND=1
_JAVA_AWT_WM_NONREPARENTING=1
SDL_VIDEODRIVER=wayland
CLUTTER_BACKEND=wayland
```

The `~/.profile` block is wrapped in `if [ "$XDG_SESSION_TYPE" != "x11" ]` so
it stays inert while the XFCE session is still in use.

### 5.4 Audio: PulseAudio to PipeWire handover

Installing the Sway stack pulled in `pipewire-pulse` as a dependency, but
`pulseaudio` 17.0 was already installed and owned the Pulse socket. Both were
enabled, and `pipewire-pulse.service` declares
`Conflicts=pulseaudio.service` — meaning the next login would have been a
race between the two.

Resolved by masking PulseAudio's user units:

```bash
systemctl --user mask pulseaudio.service pulseaudio.socket
```

The running instance was left alone, so audio in the current XFCE session is
unaffected; PipeWire takes over cleanly at next login. `pulseaudio` is on the
Phase 5 purge list.

### 5.5 Validation performed

- `sway --validate` → exit code 0, no parse errors.
  (It also prints `amdgpu: amdgpu_cs_ctx_create2 failed. (-13)` — benign, since
  Xorg currently holds DRM master so the renderer probe cannot get a context
  from inside the X11 session.)
- Waybar config confirmed to be valid JSON.
- `sway-session.target` recognised by systemd after `daemon-reload`.

---

## 6. Keybindings

| Keys | Action |
| ---- | ------ |
| `Super+Return` | Warp terminal |
| `Super+Shift+Return` | `foot` terminal (fallback) |
| `Super+d` | `wofi` launcher |
| `Super+Shift+q` | Close window |
| `Super+h/j/k/l` or arrows | Move focus |
| `Super+Shift+` + those | Move window |
| `Super+1..0` | Switch workspace |
| `Super+Shift+1..0` | Move window to workspace |
| `Super+b` / `Super+v` | Split horizontal / vertical |
| `Super+s` / `Super+w` / `Super+e` | Stacking / tabbed / toggle split |
| `Super+f` | Fullscreen toggle |
| `Super+Shift+space` | Float toggle |
| `Super+r` | Resize mode |
| `Super+Shift+x` | Lock screen |
| `Super+Shift+c` | Reload config |
| `Super+Shift+e` | Exit Sway (with confirmation) |
| `Print` / `Shift+Print` | Screenshot full / region to clipboard |

---

## 7. Remaining work

### Phase 3 — First Sway login (manual, must happen next)

Optional zero-risk smoke test first (XFCE stays alive because logind hands DRM
master over on VT switch):

```bash
# Ctrl+Alt+F3, log in, then:
sway > /tmp/sway-smoke.log 2>&1
# Super+Shift+e to exit, Ctrl+Alt+F7 to return to XFCE
```

Then the real switch: log out of XFCE and choose the **Sway** session in the
LightDM greeter, relaunch Warp inside Sway, and verify
`loginctl show-session <id> -p Type` reports `wayland`, plus audio, network,
notifications, clipboard and screenshots. Still fully reversible at this point —
log back into XFCE if anything is wrong.

### Phase 4 — Greeter

```bash
sudo apt-get install greetd tuigreet
# /etc/greetd/config.toml -> command = "tuigreet --time --remember --cmd sway"
sudo systemctl disable lightdm && sudo systemctl enable greetd
```

Using `bash -lc 'exec sway'` as the greetd command makes it a login shell so
`~/.profile` (and therefore the Wayland env vars) get sourced.

### Phase 5 — Purge X11 and XFCE

**Only run this from inside a working Sway session (or over SSH/TTY) — never
from the X11 session, since tearing down Xorg mid-`dpkg` can leave packages
half-configured.**

Protect keepbacks first so `--autoremove` cannot take them:

```bash
sudo apt-mark manual network-manager-applet gnome-keyring gnome-keyring-pkcs11 \
    libpam-gnome-keyring tango-icon-theme thunar fonts-font-awesome \
    xwayland xserver-common
```

(See section 10 — the package providing `nm-applet` is `network-manager-applet`,
not `network-manager-gnome`.)

Then dry-run and inspect before committing:

```bash
apt-get -s purge --autoremove xfce4 task-xfce-desktop task-desktop \
    xorg xserver-xorg xserver-xorg-core lightdm pulseaudio
```

Confirm `xwayland`, `xserver-common`, `network-manager`, `pipewire` and `sway`
are **absent** from the removal list, then re-run without `-s`.

`xwayland` depends on `xserver-common` but **not** on `xserver-xorg-core`, so
the Xorg server can be removed while keeping X11 app compatibility.

Expected scale: ~164 packages with `--autoremove`. Verified safe — `systemd`,
`network-manager`, `openssh-server`, the kernel and GRUB are all untouched.

### Phase 6 — Verification

Reboot, confirm Sway starts via greetd, re-run the functional checks, confirm
no Xorg server binary remains (only `Xwayland`), and `dpkg -C` is clean.

---

## 8. Rollback

Nothing needs undoing at the moment — the Wayland stack is purely additive and
the X11 desktop is untouched. If Sway misbehaves, simply pick the XFCE session
at the LightDM greeter.

If Phase 5 has already run and you need X11 back:

```bash
sudo apt-get install task-xfce-desktop xfce4 lightdm xorg
sudo systemctl enable lightdm && sudo systemctl disable greetd
sudo reboot
```

To restore audio to PulseAudio:

```bash
systemctl --user unmask pulseaudio.service pulseaudio.socket
```

Compare against `~/pkg-selections-pre-sway.txt` and
`~/apt-manual-pre-sway.txt` to see exactly what changed.

---

## 9. Handy commands

```bash
sway --validate                       # check config syntax
swaymsg -t get_outputs                # monitors and modes
swaymsg -t get_inputs                 # keyboards, mice
swaymsg -t get_tree                   # window tree (app_id / class for rules)
swaymsg reload                        # apply config changes
systemctl --user status waybar mako   # bar / notification daemon
loginctl show-session "$(loginctl --no-legend list-sessions | awk 'NR==1{print $1}')" -p Type
```

---

## 10. Audit findings (2026-07-26)

A review pass looking for missed details. Verified-clean items first, then the
real gaps and what was done about them.

### Verified as non-issues

- **`/etc/pam.d/swaylock` exists.** This is the classic Sway lockout trap — without
  that PAM file `swaylock` can authenticate nobody. Debian's package ships it,
  so the manual and before-sleep locks are safe.
- **Keyboard layout matches.** The X11 session used `us` / `pc105`, and the Sway
  config hardcodes `xkb_layout us`. No regression.
- **`dbus-user-session` 1.16.2-2 installed** — required for a working user D-Bus.
- **Waybar's tray survives the purge.** `waybar` *depends* on
  `libdbusmenu-gtk3-4`, so apt cannot autoremove it; the StatusNotifier tray
  that `nm-applet` uses keeps working.
- **Qt5 apps are covered.** `qtwayland5` 5.15.15-3 was already installed
  alongside the Qt6 backend, so Qt5 apps get native Wayland rather than XWayland.
- **`adwaita-icon-theme` 48.1 and `gnome-themes-extra` are not in the removal
  set**, so a working GTK theme/icon fallback exists after XFCE goes.

### Gap 1 — Portal file chooser would silently break (fixed)

`/usr/share/xdg-desktop-portal/portals/gtk.portal` declares `UseIn=gnome` and
`gnome-keyring.portal` likewise. Under `XDG_CURRENT_DESKTOP=sway` neither is
selected, and `wlr.portal` implements only Screenshot and ScreenCast. That would
leave FileChooser, Settings, Print and Secret with no backend — file dialogs in
Firefox and any Flatpak app would fail with no obvious error.

Fixed by creating `~/.config/xdg-desktop-portal/sway-portals.conf` routing
`default=gtk` with Screenshot/ScreenCast to `wlr` and Secret to `gnome-keyring`.

### Gap 2 — Wrong package name in the keepback list (fixed)

The Phase 5 keepback list named `network-manager-gnome`, which **is not
installed** on this system. `dpkg -S /usr/bin/nm-applet` shows the binary belongs
to **`network-manager-applet`**, and that package *is* in the removal set. Marking
the wrong name would have been a no-op and the purge would have deleted
`nm-applet` — which the Sway config autostarts for the network tray.

The keepback command in section 7 has been corrected. Also added to it:
`gnome-keyring-pkcs11` (in the removal set) and `tango-icon-theme` (see below).

### Gap 3 — Theme and cursor regression after the purge (partly fixed)

Current GSettings values are XFCE-provided and will not survive:

- `gtk-theme` = `Xfce` → disappears with XFCE (GTK falls back to built-in Adwaita)
- `icon-theme` = `Tango` → `tango-icon-theme` **is in the removal set**, so GTK
  apps and the tray would lose their icons
- `cursor-theme` = empty → XWayland clients get a mismatched/undersized cursor

Also note `xfsettingsd` is not currently running, and nothing under Sway provides
XSettings, so GTK apps read theming from GSettings/dconf directly.

Fixed now: `seat seat0 xcursor_theme Adwaita 24` added to the Sway config.
Deferred to Phase 5 (doing it now would restyle the running XFCE desktop):
repoint `gtk-theme`/`icon-theme` to `Adwaita` and write
`~/.config/gtk-3.0/settings.ini`. `tango-icon-theme` is kept as a keepback so
nothing breaks even before that.

### Gap 4 — Power management has no replacement (noted, no action)

`xfce4-power-manager` 4.20.0 is installed and will be purged. Nothing in the
Sway stack replaces it. `upower` itself is *not* in the removal set. On this
machine (a desktop, no battery or lid) the practical loss is only automatic
suspend; screen blanking and locking are already handled by the configured
`swayidle`. Add a `swayidle` timeout calling `systemctl suspend` if wanted.

### Corrected scale figure

The earlier "~164 packages" figure was for the narrower package list. With
`task-desktop`, `xserver-xorg-core` and `pulseaudio` included, the simulated
removal set is **205 packages**. Still verified free of `systemd`,
`network-manager`, `openssh-server`, kernel and GRUB.
