#!/usr/bin/env bash

# Compatibility launcher for the Ansible implementation used by the Go TUI.
# Usage: ./autologin.sh [lightdm|sway]

set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PLAYBOOK="${SCRIPT_DIR}/tui/internal/playbooks/autologin.yml"
MODE="${1:-lightdm}"

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

case "${MODE}" in
lightdm | sway) ;;
-h | --help)
	printf 'Usage: %s [lightdm|sway]\n' "$0"
	exit 0
	;;
*)
	die "mode must be 'lightdm' or 'sway'"
	;;
esac

ANSIBLE_PLAYBOOK="$(command -v ansible-playbook 2>/dev/null)" \
	|| die "Ansible is not installed; use 'Install Ansible' in the Go TUI first"
[[ -f "${PLAYBOOK}" ]] || die "playbook not found: ${PLAYBOOK}"

if [[ -n "${LINUXCNCSETUP_TARGET_USER:-}" ]]; then
	TARGET_USER="${LINUXCNCSETUP_TARGET_USER}"
elif [[ -n "${SUDO_USER:-}" && "${SUDO_USER}" != "root" ]]; then
	TARGET_USER="${SUDO_USER}"
elif ((EUID != 0)); then
	TARGET_USER="$(id -un)"
else
	die "target user is ambiguous; set LINUXCNCSETUP_TARGET_USER"
fi

[[ "${TARGET_USER}" =~ ^[a-zA-Z_][a-zA-Z0-9_.-]*[$]?$ ]] \
	|| die "invalid target user: ${TARGET_USER}"
getent passwd "${TARGET_USER}" >/dev/null \
	|| die "target user does not exist: ${TARGET_USER}"

if [[ "${MODE}" == "sway" && "${LINUXCNCSETUP_SWAY_VALIDATED:-}" != "1" ]]; then
	die "log into Sway manually first, then rerun with LINUXCNCSETUP_SWAY_VALIDATED=1"
fi

arguments=(
	--inventory "localhost,"
	--connection local
	--diff
	--extra-vars "autologin_mode=${MODE}"
	--extra-vars "target_user=${TARGET_USER}"
)
if [[ "${MODE}" == "sway" ]]; then
	arguments+=(--extra-vars '{"sway_validated": true}')
fi
arguments+=("${PLAYBOOK}")

printf 'Configuring %s auto-login for %s with Ansible.\n' "${MODE}" "${TARGET_USER}"
if ((EUID == 0)); then
	exec "${ANSIBLE_PLAYBOOK}" "${arguments[@]}"
fi
exec sudo -- "${ANSIBLE_PLAYBOOK}" "${arguments[@]}"
