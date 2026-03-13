#!/bin/bash
#
# LinuxCNC Setup Manager
# Launches the interactive TUI for system configuration.
# Falls back to a basic menu if Python/Textual are not available.
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# ---------------------------------------------------------------------------
# Try launching the Textual TUI
# ---------------------------------------------------------------------------
if python3 -c "import textual" 2>/dev/null; then
    exec python3 "$SCRIPT_DIR/setup.py"
fi

# ---------------------------------------------------------------------------
# Textual not installed — offer to install it
# ---------------------------------------------------------------------------
echo "============================================"
echo "  LinuxCNC Setup Manager"
echo "============================================"
echo ""
echo "The interactive TUI requires the 'textual' Python package."
echo ""
read -p "Install it now? (Y/n): " install_choice

if [[ ! "$install_choice" =~ ^[Nn]$ ]]; then
    echo "Installing textual..."
    pip3 install --break-system-packages textual 2>/dev/null \
        || pip3 install textual 2>/dev/null \
        || { echo "Failed to install textual. Please install manually: pip3 install textual"; exit 1; }
    echo ""
    echo "Textual installed. Launching setup manager..."
    sleep 1
    exec python3 "$SCRIPT_DIR/setup.py"
fi

# ---------------------------------------------------------------------------
# Fallback: basic bash menu (original behaviour)
# ---------------------------------------------------------------------------
echo ""
echo "Running in fallback mode (basic menu)..."
echo ""

shopt -s nullglob
scripts=()
for file in *.sh; do
    [[ "$file" == "setup.sh" ]] && continue
    scripts+=("$file")
done

if [ ${#scripts[@]} -eq 0 ]; then
    echo "No scripts available."
    exit 0
fi

PS3="Select a script to execute (or choose Quit to exit): "
select script in "${scripts[@]}" "Quit"; do
    case $script in
        "Quit")
            echo "Exiting."
            exit 0
            ;;
        "")
            echo "Invalid selection. Please try again."
            ;;
        *)
            chmod +x "$script"
            echo "Executing: $script"
            case "$script" in
                "mount-smb-share.sh")
                    "./$script"
                    ;;
                *)
                    bash "$script"
                    ;;
            esac
            echo ""
            echo "Script completed. Select another script or quit."
            echo ""
            echo "Available scripts:"
            for i in "${!scripts[@]}"; do
                printf "%2d) %s\n" $((i+1)) "${scripts[$i]}"
            done
            printf "%2d) %s\n" $((${#scripts[@]}+1)) "Quit"
            echo ""
            ;;
    esac
done
