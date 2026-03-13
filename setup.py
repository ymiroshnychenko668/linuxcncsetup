#!/usr/bin/env python3
"""LinuxCNC Setup Manager - Interactive TUI for system configuration."""

import os
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path

from textual import on, work
from textual.app import App, ComposeResult
from textual.binding import Binding
from textual.containers import Horizontal, Vertical, VerticalScroll
from textual.screen import ModalScreen
from textual.widgets import (
    Button,
    Footer,
    Header,
    Input,
    Label,
    RichLog,
    Static,
    Tree,
)

SCRIPT_DIR = Path(__file__).parent.resolve()


# ---------------------------------------------------------------------------
# Script registry – every script documented in one place
# ---------------------------------------------------------------------------

@dataclass
class ScriptInfo:
    filename: str
    name: str
    description: str
    long_description: str
    needs_sudo: bool = False
    status: str = "working"  # "working", "experimental", "helper"


CATEGORIES: dict[str, list[ScriptInfo]] = {
    "Desktop Environment": [
        ScriptInfo(
            "i3kiosk.sh",
            "i3 Kiosk Setup",
            "Install and configure i3 window manager with Polybar and LinuxCNC integration",
            "Installs i3-wm, polybar, rofi, Firefox ESR, and VS Code. "
            "Finds available LinuxCNC configurations and lets you pick one. "
            "Sets up polybar with custom menus (Browser, LinuxCNC, Logout, Reboot, Shutdown), "
            "configures i3 workspaces (workspace 2 = CNC), registers i3 as the default "
            "window manager, enables LightDM, and creates autostart entries. "
            "Also copies helper scripts for logout/reboot/shutdown to polybar.",
        ),
        ScriptInfo(
            "autologin.sh",
            "Auto-Login Setup",
            "Configure LightDM display manager for automatic graphical login",
            "Installs LightDM, enables it via systemd, and configures autologin "
            "for the current user with zero timeout. After reboot, the system "
            "boots straight to the desktop without a login prompt.",
        ),
    ],
    "CPU & Real-Time Tuning": [
        ScriptInfo(
            "grub-configure-multicore.sh",
            "GRUB RT Configuration",
            "Configure GRUB with full real-time kernel parameters for multi-core systems",
            "The most comprehensive GRUB setup. Dynamically detects CPU count and applies: "
            "isolcpus, nohz_full, rcu_nocbs, irqaffinity, kthread_cpus, "
            "processor.max_cstate=1, intel_idle.max_cstate=0, idle=poll, "
            "mitigations=off, nosoftlockup, tsc=reliable, and more. "
            "Also disables GRUB timeout for instant boot, installs Plymouth "
            "splash screen, and sets GRUB_DISABLE_RECOVERY. "
            "Creates a backup of /etc/default/grub before changes.",
            needs_sudo=True,
        ),
        ScriptInfo(
            "setup-linuxcnc-cpu-pinning.sh",
            "CPU Core Pinning",
            "Isolate CPU cores via GRUB isolcpus for LinuxCNC real-time threads",
            "Detects CPU count with lscpu and recommends which core(s) to isolate. "
            "Adds isolcpus=<cores> and idle=poll to GRUB_CMDLINE_LINUX_DEFAULT. "
            "Simpler than the full GRUB configuration — good if you only need "
            "basic core isolation. Backs up GRUB config, runs update-grub, "
            "and offers to reboot immediately.",
            needs_sudo=True,
        ),
        ScriptInfo(
            "configure-cpu-affinity.sh",
            "CPU & IRQ Affinity",
            "Set CPU governor to performance, pin IRQs to specific cores, create boot service",
            "Sets all CPUs to 'performance' governor and locks frequency to max. "
            "Applies smp_affinity to all IRQs, directing them to non-isolated cores. "
            "Offers interactive IRQ-by-IRQ configuration (single or batch). "
            "Saves config to /etc/linuxcnc-irq-affinity.conf, creates a boot script "
            "at /usr/local/bin/linuxcnc-cpu-setup.sh, and installs a systemd service "
            "to restore settings on every boot.",
            needs_sudo=True,
        ),
        ScriptInfo(
            "cpu-setup.sh",
            "CPU Setup (launcher)",
            "Wrapper that runs configure-cpu-affinity.sh with sudo",
            "Simple launcher script — calls configure-cpu-affinity.sh with sudo. "
            "Use this if you want to run the affinity setup from a non-root shell.",
            needs_sudo=True,
            status="helper",
        ),
    ],
    "Network & Storage": [
        ScriptInfo(
            "mount-smb-share.sh",
            "Mount SMB Share",
            "Set up persistent SMB/CIFS network share with systemd automount",
            "Installs cifs-utils if missing. Prompts for a mount point path, "
            "creates /etc/cifs-credentials (guest access), adds an fstab entry "
            "with x-systemd.automount, SMB 3.0, and proper uid/gid. "
            "Starts the systemd automount unit, tests the mount, and verifies "
            "write access. SMB server: //10.0.1.246/share.",
            needs_sudo=True,
        ),
        ScriptInfo(
            "smb-mount-manager.sh",
            "SMB Mount Manager",
            "Troubleshoot and manage existing SMB mounts (status, check, remount, logs)",
            "Multi-command helper: status (mount info + disk usage + access test), "
            "check (connectivity + credentials + permissions diagnostics), "
            "remount (stop + start mount unit), unmount, logs (journalctl), "
            "cleanup (daemon-reload + reset-failed), list (show all CIFS in fstab). "
            "Accepts mount point as second argument.",
            status="helper",
        ),
    ],
    "Hardware (MESA)": [
        ScriptInfo(
            "upgrade-mesa.sh",
            "Upgrade MESA Drivers",
            "Build and install mesaflash + hostmot2-firmware from source",
            "Installs build dependencies (build-essential, libpci-dev, etc.), "
            "clones or updates the mesaflash repo from GitHub, compiles and "
            "installs it system-wide. Also clones/updates hostmot2-firmware. "
            "Verifies HAL pins load correctly via halrun. "
            "Contains commented-out section for building LinuxCNC drivers from source.",
            needs_sudo=True,
        ),
        ScriptInfo(
            "totest/configure-mesa-ethernet.sh",
            "MESA Ethernet Config",
            "Configure a network interface for MESA Ethernet cards (192.168.1.x)",
            "Uses NetworkManager (nmcli) to set up a dedicated connection for MESA cards. "
            "Detects available ethernet interfaces, sets static IP 192.168.1.1/24, "
            "disables auto-negotiate (100Mbps full-duplex). "
            "Tests connectivity to standard MESA IPs (.121, .122, .123). "
            "Optionally creates udev rules for consistent interface naming. "
            "Shows HAL configuration examples for hm2_eth.",
            status="experimental",
        ),
    ],
    "Real-Time IRQ Management": [
        ScriptInfo(
            "totest/rt-irq-install.sh",
            "Install RT IRQ Tuning",
            "Install rtirq-init, configure interrupt priorities, disable hardware watchdog",
            "Installs rtirq-init package and writes a full /etc/default/rtirq config "
            "with priority levels for timer, parport, serial, USB, and network IRQs. "
            "Disables iTCO hardware watchdog (blacklists kernel modules) to prevent "
            "RT conflicts causing system reboots. Configures systemd to not use "
            "hardware watchdog. Enables and starts the rtirq service. "
            "Shows current RT thread priorities and interrupt distribution.",
            needs_sudo=True,
            status="experimental",
        ),
        ScriptInfo(
            "totest/rt-irq-check.sh",
            "Check RT IRQ Status",
            "Verify real-time configuration: kernel, threads, IRQs, latency sources",
            "Read-only diagnostic script. Checks: rtirq service status, "
            "RT kernel detection, CPU isolation from /proc/cmdline, "
            "RT thread priorities (SCHED_FIFO/RR), interrupt distribution, "
            "IRQ CPU affinity for critical devices, system load, "
            "and potential latency sources (C-states, mitigations, softlockup). "
            "Offers interactive cyclictest runs (10s / 60s / 5min).",
            status="experimental",
        ),
    ],
    "LinuxCNC Config": [
        ScriptInfo(
            "clonelinuxcncconfig.sh",
            "Clone CNC Config",
            "Clone the CorvusCNC configuration repository from GitHub",
            "Creates ~/linuxcnc/configs/ directory and clones the corvuscnc "
            "repository via SSH (git@github.com:ymiroshnychenko668/corvuscnc.git). "
            "Requires SSH key configured for GitHub access. "
            "Skips if the repo already exists.",
        ),
    ],
    "Development Tools": [
        ScriptInfo(
            "install-devtools.sh",
            "Install Dev Tools",
            "Install Git, VS Code, htop, mc, Terminator, and generate SSH keys",
            "Installs and configures: Git (user 'cnc', email cnc@cnc.cn), "
            "SSH key generation (ED25519) for GitHub, Midnight Commander, "
            "Terminator terminal, htop. Adds Microsoft APT repository and "
            "installs VS Code. Enables Syncthing via loginctl enable-linger. "
            "Prints the SSH public key for adding to GitHub.",
        ),
    ],
    "System Fixes": [
        ScriptInfo(
            "fix-initramfs-tools.sh",
            "Fix initramfs Errors",
            "Fix initramfs-tools failures caused by raspi-firmware on non-Pi systems",
            "Detects if the system is a Raspberry Pi (exits if so). "
            "Disables problematic /etc/initramfs/post-update.d/z50-raspi-firmware "
            "and /etc/kernel/postinst.d/z50-raspi-firmware scripts by moving them "
            "to .disabled and replacing with dummy scripts that exit 0. "
            "Re-configures initramfs-tools and runs update-initramfs to verify. "
            "Original scripts backed up with .disabled extension.",
            needs_sudo=True,
        ),
        ScriptInfo(
            "prevent-raspi-firmware-conflicts.sh",
            "Prevent Raspi-Firmware Issues",
            "Set up permanent prevention of raspi-firmware conflicts on PC systems",
            "Creates APT preferences (/etc/apt/preferences.d/99-no-raspi-firmware) "
            "to block raspi-firmware, raspberrypi-bootloader, and raspberrypi-kernel "
            "packages from ever being installed. Creates a check script at "
            "/usr/local/bin/check-raspi-firmware, a systemd service to run it at boot, "
            "and a dpkg hook for real-time monitoring during package operations.",
            needs_sudo=True,
        ),
    ],
    "System Power": [
        ScriptInfo(
            "logout.sh", "Logout", "Exit i3 window manager session",
            "Sends 'exit' command to i3 via i3-msg. Ends your X session.",
            status="helper",
        ),
        ScriptInfo(
            "reboot.sh", "Reboot", "Reboot the system immediately",
            "Calls systemctl reboot. System will restart immediately.",
            needs_sudo=True,
            status="helper",
        ),
        ScriptInfo(
            "shutdown.sh", "Shutdown", "Power off the system immediately",
            "Calls systemctl poweroff. System will shut down immediately.",
            needs_sudo=True,
            status="helper",
        ),
    ],
}

STATUS_STYLES = {
    "working": ("[green]STABLE[/]", "green"),
    "experimental": ("[yellow]EXPERIMENTAL[/]", "yellow"),
    "helper": ("[dim]HELPER[/]", "dim"),
}


def find_script(filename: str) -> Path | None:
    """Resolve a script path relative to the project root."""
    path = SCRIPT_DIR / filename
    return path if path.exists() else None


# ---------------------------------------------------------------------------
# Script runner screen
# ---------------------------------------------------------------------------

class RunScriptScreen(ModalScreen[None]):
    """Full-screen modal that executes a script and streams output."""

    BINDINGS = [
        Binding("escape", "close", "Close", show=True),
        Binding("q", "close", "Close"),
    ]

    CSS = """
    RunScriptScreen {
        align: center middle;
    }
    RunScriptScreen > Vertical {
        width: 95%;
        height: 90%;
        border: thick $accent;
        background: $surface;
        padding: 1 2;
    }
    RunScriptScreen #run-header {
        height: 3;
        content-align: center middle;
        text-style: bold;
        color: $text;
        background: $primary-background;
        margin-bottom: 1;
    }
    RunScriptScreen RichLog {
        height: 1fr;
        border: solid $primary;
        scrollbar-size: 1 1;
    }
    RunScriptScreen #run-status {
        height: 1;
        margin-top: 1;
        text-align: center;
    }
    RunScriptScreen #close-btn {
        dock: bottom;
        width: 100%;
        margin-top: 1;
    }
    """

    def __init__(self, script: ScriptInfo) -> None:
        super().__init__()
        self.script = script
        self._process: subprocess.Popen | None = None

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label(f"Running: {self.script.name}", id="run-header")
            yield RichLog(highlight=True, markup=True, wrap=True, id="run-log")
            yield Label("Starting...", id="run-status")
            yield Button("Close  [Esc]", id="close-btn", variant="primary")

    def on_mount(self) -> None:
        self.run_script()

    @work(exclusive=True, thread=True)
    def run_script(self) -> None:
        log = self.query_one("#run-log", RichLog)
        status = self.query_one("#run-status", Label)

        script_path = find_script(self.script.filename)
        if script_path is None:
            self.app.call_from_thread(log.write, f"[red]ERROR: Script not found: {self.script.filename}[/]")
            self.app.call_from_thread(status.update, "[red]Script not found[/]")
            return

        cmd = ["bash", str(script_path)]
        if self.script.filename == "mount-smb-share.sh":
            cmd = [str(script_path)]

        env = os.environ.copy()
        env["TERM"] = "dumb"

        self.app.call_from_thread(status.update, "[yellow]Running...[/]")

        try:
            self._process = subprocess.Popen(
                cmd,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
                bufsize=1,
                cwd=str(SCRIPT_DIR),
                env=env,
            )

            for line in self._process.stdout:
                self.app.call_from_thread(log.write, line.rstrip("\n"))

            self._process.wait()
            rc = self._process.returncode

            if rc == 0:
                self.app.call_from_thread(status.update, "[green]Completed successfully (exit code 0)[/]")
            else:
                self.app.call_from_thread(status.update, f"[red]Exited with code {rc}[/]")

        except Exception as e:
            self.app.call_from_thread(log.write, f"[red]Error: {e}[/]")
            self.app.call_from_thread(status.update, "[red]Failed to execute[/]")

    def action_close(self) -> None:
        if self._process and self._process.poll() is None:
            self._process.terminate()
        self.dismiss(None)

    @on(Button.Pressed, "#close-btn")
    def on_close_pressed(self) -> None:
        self.action_close()


# ---------------------------------------------------------------------------
# Confirm screen
# ---------------------------------------------------------------------------

class ConfirmScreen(ModalScreen[bool]):
    """Ask for confirmation before running a script."""

    CSS = """
    ConfirmScreen {
        align: center middle;
    }
    ConfirmScreen > Vertical {
        width: 70;
        height: auto;
        max-height: 20;
        border: thick $warning;
        background: $surface;
        padding: 1 2;
    }
    ConfirmScreen Label {
        width: 100%;
        margin-bottom: 1;
    }
    ConfirmScreen Horizontal {
        height: 3;
        align: center middle;
    }
    ConfirmScreen Button {
        margin: 0 2;
    }
    """

    def __init__(self, script: ScriptInfo) -> None:
        super().__init__()
        self.script = script

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label(f"[bold]Run {self.script.name}?[/]")
            if self.script.needs_sudo:
                yield Label("[yellow]This script requires sudo privileges.[/]")
            if self.script.status == "experimental":
                yield Label("[yellow]This script is marked EXPERIMENTAL and may not work correctly.[/]")
            yield Label(f"\n{self.script.description}")
            with Horizontal():
                yield Button("Run", variant="success", id="confirm-yes")
                yield Button("Cancel", variant="error", id="confirm-no")

    @on(Button.Pressed, "#confirm-yes")
    def on_yes(self) -> None:
        self.dismiss(True)

    @on(Button.Pressed, "#confirm-no")
    def on_no(self) -> None:
        self.dismiss(False)


# ---------------------------------------------------------------------------
# SMB Mount Manager screen
# ---------------------------------------------------------------------------

class SMBManagerScreen(ModalScreen[None]):
    """Interactive SMB mount manager with commands for troubleshooting and managing mounts."""

    BINDINGS = [
        Binding("escape", "close", "Close", show=True),
        Binding("q", "close", "Close"),
    ]

    CSS = """
    SMBManagerScreen {
        align: center middle;
    }
    SMBManagerScreen > Vertical {
        width: 95%;
        height: 90%;
        border: thick $accent;
        background: $surface;
        padding: 1 2;
    }
    SMBManagerScreen #smb-header {
        height: 3;
        content-align: center middle;
        text-style: bold;
        color: $text;
        background: $primary-background;
        margin-bottom: 1;
    }
    SMBManagerScreen #smb-mount-input {
        margin-bottom: 1;
    }
    SMBManagerScreen #smb-buttons {
        height: 3;
        margin-bottom: 1;
    }
    SMBManagerScreen #smb-buttons Button {
        margin: 0 1;
    }
    SMBManagerScreen RichLog {
        height: 1fr;
        border: solid $primary;
        scrollbar-size: 1 1;
    }
    SMBManagerScreen #smb-status {
        height: 1;
        margin-top: 1;
        text-align: center;
    }
    SMBManagerScreen #smb-close-btn {
        dock: bottom;
        width: 100%;
        margin-top: 1;
    }
    """

    def __init__(self) -> None:
        super().__init__()
        self._process: subprocess.Popen | None = None

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label("SMB Mount Manager", id="smb-header")
            yield Input(
                placeholder="/mnt/smb_share",
                id="smb-mount-input",
            )
            with Horizontal(id="smb-buttons"):
                yield Button("List", id="smb-list", variant="default")
                yield Button("Status", id="smb-status-btn", variant="primary")
                yield Button("Check", id="smb-check", variant="primary")
                yield Button("Remount", id="smb-remount", variant="warning")
                yield Button("Unmount", id="smb-unmount", variant="warning")
                yield Button("Logs", id="smb-logs", variant="default")
                yield Button("Cleanup", id="smb-cleanup", variant="error")
            yield RichLog(highlight=True, markup=True, wrap=True, id="smb-log")
            yield Label("Select a command above. Set mount point for mount-specific commands.", id="smb-status")
            yield Button("Close  [Esc]", id="smb-close-btn", variant="primary")

    def on_mount(self) -> None:
        self._run_smb_command("list")

    def _get_mount_point(self) -> str:
        return self.query_one("#smb-mount-input", Input).value.strip()

    def _run_smb_command(self, command: str, mount_point: str = "") -> None:
        """Run an smb-mount-manager.sh command and stream output to the log."""
        log = self.query_one("#smb-log", RichLog)
        status = self.query_one("#smb-status", Label)

        log.clear()

        script_path = SCRIPT_DIR / "smb-mount-manager.sh"
        if not script_path.exists():
            log.write("[red]ERROR: smb-mount-manager.sh not found[/]")
            status.update("[red]Script not found[/]")
            return

        needs_mount_point = command in ("status", "check", "remount", "unmount", "logs")
        if needs_mount_point and not mount_point:
            log.write(f"[yellow]Please enter a mount point path above before running '{command}'.[/]")
            log.write("[dim]Hint: Use 'List' first to see available mounts.[/]")
            status.update("[yellow]Mount point required[/]")
            return

        self._run_command_worker(command, mount_point, script_path)

    @work(exclusive=True, thread=True)
    def _run_command_worker(self, command: str, mount_point: str, script_path: Path) -> None:
        log = self.query_one("#smb-log", RichLog)
        status = self.query_one("#smb-status", Label)

        cmd = ["bash", str(script_path), command]
        if mount_point:
            cmd.append(mount_point)

        env = os.environ.copy()
        env["TERM"] = "dumb"

        self.app.call_from_thread(
            status.update, f"[yellow]Running: {command} {mount_point}...[/]"
        )

        try:
            self._process = subprocess.Popen(
                cmd,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
                bufsize=1,
                cwd=str(SCRIPT_DIR),
                env=env,
            )

            for line in self._process.stdout:
                self.app.call_from_thread(log.write, line.rstrip("\n"))

            self._process.wait()
            rc = self._process.returncode

            if rc == 0:
                self.app.call_from_thread(
                    status.update, f"[green]'{command}' completed successfully[/]"
                )
            else:
                self.app.call_from_thread(
                    status.update, f"[red]'{command}' exited with code {rc}[/]"
                )

        except Exception as e:
            self.app.call_from_thread(log.write, f"[red]Error: {e}[/]")
            self.app.call_from_thread(status.update, "[red]Failed to execute[/]")

    @on(Button.Pressed, "#smb-list")
    def on_list(self) -> None:
        self._run_smb_command("list")

    @on(Button.Pressed, "#smb-status-btn")
    def on_status(self) -> None:
        self._run_smb_command("status", self._get_mount_point())

    @on(Button.Pressed, "#smb-check")
    def on_check(self) -> None:
        self._run_smb_command("check", self._get_mount_point())

    @on(Button.Pressed, "#smb-remount")
    def on_remount(self) -> None:
        self._run_smb_command("remount", self._get_mount_point())

    @on(Button.Pressed, "#smb-unmount")
    def on_unmount(self) -> None:
        self._run_smb_command("unmount", self._get_mount_point())

    @on(Button.Pressed, "#smb-logs")
    def on_logs(self) -> None:
        self._run_smb_command("logs", self._get_mount_point())

    @on(Button.Pressed, "#smb-cleanup")
    def on_cleanup(self) -> None:
        self._run_smb_command("cleanup")

    def action_close(self) -> None:
        if self._process and self._process.poll() is None:
            self._process.terminate()
        self.dismiss(None)

    @on(Button.Pressed, "#smb-close-btn")
    def on_close_pressed(self) -> None:
        self.action_close()


# ---------------------------------------------------------------------------
# Detail panel widget
# ---------------------------------------------------------------------------

class DetailPanel(Static):
    """Right-side panel showing script details."""

    CSS = """
    DetailPanel {
        width: 1fr;
        height: 100%;
        padding: 1 2;
        border-left: solid $primary;
        overflow-y: auto;
    }
    """

    def update_script(self, script: ScriptInfo | None) -> None:
        if script is None:
            self.update("[dim]Select a script from the tree to see details.[/]")
            return

        status_label, _ = STATUS_STYLES.get(script.status, ("[white]UNKNOWN[/]", "white"))
        sudo_badge = "  [red bold]SUDO[/]" if script.needs_sudo else ""
        path = find_script(script.filename)
        exists = "[green]found[/]" if path else "[red]NOT FOUND[/]"

        text = (
            f"[bold underline]{script.name}[/]\n\n"
            f"[bold]Status:[/] {status_label}{sudo_badge}\n"
            f"[bold]File:[/]   {script.filename}  ({exists})\n\n"
            f"[bold]Summary[/]\n{script.description}\n\n"
            f"[bold]Details[/]\n{script.long_description}\n"
        )
        self.update(text)


# ---------------------------------------------------------------------------
# Main application
# ---------------------------------------------------------------------------

class LinuxCNCSetup(App):
    """LinuxCNC Setup Manager."""

    TITLE = "LinuxCNC Setup Manager"
    SUB_TITLE = "Interactive system configuration"

    CSS = """
    Screen {
        layout: vertical;
    }
    #main-area {
        height: 1fr;
    }
    #tree-pane {
        width: 48;
        height: 100%;
        border-right: solid $primary;
        padding: 0 1;
    }
    #tree-pane Tree {
        height: 1fr;
        scrollbar-size: 1 1;
    }
    #tree-header {
        height: 3;
        content-align: center middle;
        text-style: bold;
        background: $primary-background;
        margin-bottom: 1;
    }
    #detail-pane {
        width: 1fr;
        height: 100%;
    }
    #bottom-bar {
        height: 3;
        dock: bottom;
        background: $primary-background;
        padding: 0 2;
        content-align: center middle;
    }
    """

    BINDINGS = [
        Binding("q", "quit", "Quit"),
        Binding("enter", "run_selected", "Run Script", show=True),
        Binding("r", "run_selected", "Run"),
        Binding("d", "toggle_dark", "Dark/Light"),
    ]

    def __init__(self) -> None:
        super().__init__()
        self._selected_script: ScriptInfo | None = None
        self._script_map: dict[str, ScriptInfo] = {}

    def compose(self) -> ComposeResult:
        yield Header()
        with Horizontal(id="main-area"):
            with Vertical(id="tree-pane"):
                yield Label("Scripts", id="tree-header")
                yield self._build_tree()
            with VerticalScroll(id="detail-pane"):
                yield DetailPanel(id="detail")
        yield Footer()

    def _build_tree(self) -> Tree:
        tree: Tree[str] = Tree("LinuxCNC Setup", id="script-tree")
        tree.root.expand()

        for category, scripts in CATEGORIES.items():
            branch = tree.root.add(f"[bold]{category}[/]", expand=True)
            for script in scripts:
                status_label, color = STATUS_STYLES.get(script.status, ("[white]?[/]", "white"))
                sudo = " [red]sudo[/]" if script.needs_sudo else ""
                label = f"[{color}]{script.name}[/]{sudo}"
                node = branch.add_leaf(label)
                key = f"{category}::{script.filename}"
                node.data = key
                self._script_map[key] = script

        return tree

    @on(Tree.NodeHighlighted)
    def on_tree_node_highlighted(self, event: Tree.NodeHighlighted) -> None:
        key = event.node.data
        if key and key in self._script_map:
            self._selected_script = self._script_map[key]
            self.query_one("#detail", DetailPanel).update_script(self._selected_script)
        else:
            self._selected_script = None
            self.query_one("#detail", DetailPanel).update_script(None)

    @on(Tree.NodeSelected)
    def on_tree_node_selected(self, event: Tree.NodeSelected) -> None:
        key = event.node.data
        if key and key in self._script_map:
            self._selected_script = self._script_map[key]
            self._confirm_and_run()

    def action_run_selected(self) -> None:
        if self._selected_script:
            self._confirm_and_run()

    def _confirm_and_run(self) -> None:
        if self._selected_script is None:
            return

        script = self._selected_script

        # Open dedicated SMB Manager screen instead of running the raw script
        if script.filename == "smb-mount-manager.sh":
            self.push_screen(SMBManagerScreen())
            return

        path = find_script(script.filename)
        if path is None:
            self.notify(f"Script not found: {script.filename}", severity="error")
            return

        def on_confirm(result: bool | None) -> None:
            if result:
                self.push_screen(RunScriptScreen(script))

        self.push_screen(ConfirmScreen(script), on_confirm)


def main() -> None:
    app = LinuxCNCSetup()
    app.run()


if __name__ == "__main__":
    main()
