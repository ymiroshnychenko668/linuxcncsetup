#!/bin/bash

# DEPRECATED: This legacy shell setup menu is retained for compatibility.
# Use the LinuxCNC Setup TUI application for new installations and updates.

set -e

printf '%s\n\n' 'WARNING: setup.sh is deprecated. Use the LinuxCNC Setup TUI application instead.' >&2

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
cd "$SCRIPT_DIR"

REMOTE_TERMINAL_LABEL="Install Remote Terminal (Ansible)"
REMOTE_TERMINAL_PLAYBOOK="$SCRIPT_DIR/remoteterminal/ansible/install.yml"

is_valid_ipv4() {
  local address=$1
  local octet
  local -a octets

  [[ $address =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]] || return 1
  IFS=. read -r -a octets <<< "$address"
  for octet in "${octets[@]}"; do
    ((10#$octet <= 255)) || return 1
  done
  ((10#${octets[0]} >= 1 && 10#${octets[0]} <= 223)) || return 1
  ((10#${octets[0]} != 127)) || return 1
  ((10#${octets[3]} != 0 && 10#${octets[3]} != 255))
}

is_valid_machine_name() {
  local name=$1
  local pattern='^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$'

  [[ $name =~ $pattern ]]
}

install_ansible_if_needed() {
  local answer

  if command -v ansible-playbook >/dev/null 2>&1; then
    return 0
  fi

  echo "Ansible is required to install Remote Terminal."
  read -r -p "Install ansible-core now? [Y/n]: " answer
  case ${answer:-Y} in
    [Yy]*)
      sudo apt-get update || return 1
      sudo apt-get install -y ansible-core || return 1
      ;;
    *)
      echo "Remote Terminal installation cancelled."
      return 1
      ;;
  esac
}

install_remote_terminal() {
  local default_user
  local default_machine_name
  local detected_address
  local target_user
  local machine_name
  local listen_address
  local listen_port

  install_ansible_if_needed || return 1

  default_user=${SUDO_USER:-${USER:-}}
  default_machine_name=$(hostname -s 2>/dev/null || hostname 2>/dev/null || true)
  # Do not silently choose a tunnel address: version 1 is intentionally LAN
  # only and has no Tailscale integration. Manual input remains available for
  # unusual physical-interface names and is revalidated by Ansible.
  detected_address=$(ip -4 -o addr show up scope global 2>/dev/null | awk '
    $2 != "lo" && $2 !~ /^(tailscale|tun|tap|wg|zt|docker|br-|virbr|veth)/ {
      split($4, parts, "/")
      print parts[1]
      exit
    }
  ')

  read -r -p "Linux system user [${default_user}]: " target_user
  target_user=${target_user:-$default_user}
  if [[ -z $target_user || $target_user == root ]] || ! id "$target_user" >/dev/null 2>&1; then
    echo "A valid non-root local system user is required." >&2
    return 1
  fi

  read -r -p "Machine name [${default_machine_name}]: " machine_name
  machine_name=${machine_name:-$default_machine_name}
  if ! is_valid_machine_name "$machine_name"; then
    echo "The machine name must be 1-64 characters and use letters, numbers, spaces, dots, underscores, or hyphens." >&2
    return 1
  fi

  read -r -p "LAN IPv4 address [${detected_address}]: " listen_address
  listen_address=${listen_address:-$detected_address}
  if [[ -z $listen_address ]] || ! is_valid_ipv4 "$listen_address"; then
    echo "A valid non-loopback IPv4 address is required." >&2
    return 1
  fi

  read -r -p "HTTPS port [8443]: " listen_port
  listen_port=${listen_port:-8443}
  if [[ ! $listen_port =~ ^[0-9]+$ ]] || ((10#$listen_port < 1024 || 10#$listen_port > 65535)); then
    echo "The HTTPS port must be between 1024 and 65535." >&2
    return 1
  fi

  sudo -v || return 1
  ansible-playbook \
    -i localhost, \
    --connection=local \
    --become \
    --extra-vars "remoteterminal_user=$target_user" \
    --extra-vars "{\"remoteterminal_machine_name\":\"$machine_name\"}" \
    --extra-vars "remoteterminal_listen_address=$listen_address" \
    --extra-vars "remoteterminal_port=$listen_port" \
    "$REMOTE_TERMINAL_PLAYBOOK"
}

# Collect executable scripts in this directory excluding setup.sh
shopt -s nullglob
scripts=()
for file in *.sh; do
  [ "$file" = "setup.sh" ] && continue
  scripts+=("$file")
done

menu_items=("${scripts[@]}")
if [[ -f $REMOTE_TERMINAL_PLAYBOOK ]]; then
  menu_items+=("$REMOTE_TERMINAL_LABEL")
fi

if [ ${#menu_items[@]} -eq 0 ]; then
  echo "No setup actions available."
  exit 0
fi

PS3="Select a script to execute (or choose Quit to exit): "
select script in "${menu_items[@]}" "Quit"; do
  case $script in
    "Quit")
      echo "Exiting."
      exit 0
      ;;
    "$REMOTE_TERMINAL_LABEL")
      echo "Executing: $REMOTE_TERMINAL_LABEL"
      if install_remote_terminal; then
        echo "Remote Terminal installation completed."
      else
        echo "Remote Terminal installation did not complete." >&2
      fi
      echo ""
      echo "Available setup actions:"
      for i in "${!menu_items[@]}"; do
        printf "%2d) %s\n" $((i+1)) "${menu_items[$i]}"
      done
      printf "%2d) %s\n" $((${#menu_items[@]}+1)) "Quit"
      echo ""
      ;;
    "")
      echo "Invalid selection. Please try again."
      ;;
    *)
      chmod +x "$script"
      echo "Executing: $script"
      # Handle special scripts that need different execution
      case "$script" in
        "mount-smb-share.sh")
          # Execute directly to preserve $0 for sudo re-execution
          "./mount-smb-share.sh"
          ;;
        *)
          bash "$script"
          ;;
      esac
      echo ""
      echo "Script completed. Select another script or quit."
      echo ""
      # Re-display the menu options
      echo "Available scripts:"
      for i in "${!menu_items[@]}"; do
        printf "%2d) %s\n" $((i+1)) "${menu_items[$i]}"
      done
      printf "%2d) %s\n" $((${#menu_items[@]}+1)) "Quit"
      echo ""
      ;;
  esac
done
