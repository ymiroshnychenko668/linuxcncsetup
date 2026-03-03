#!/bin/bash

# Script to configure CPU core pinning for LinuxCNC
# This improves real-time performance by isolating a CPU core for LinuxCNC's real-time threads
# Usage: ./setup-linuxcnc-cpu-pinning.sh [ini_file_path]

set -e

echo "======================================"
echo "LinuxCNC CPU Core Pinning Setup"
echo "======================================"
echo ""

# Detect number of CPU cores
NUM_CPUS=$(nproc)
echo "Detected $NUM_CPUS CPU cores (numbered 0-$((NUM_CPUS-1)))"
echo ""

# Determine which core(s) to isolate
# Best practice: isolate the last (highest numbered) core
LAST_CORE=$((NUM_CPUS-1))

if [ $NUM_CPUS -eq 1 ]; then
    echo "ERROR: You have only 1 CPU core. Core isolation is not possible."
    exit 1
elif [ $NUM_CPUS -eq 2 ]; then
    ISOLATE_CORES="1"
    echo "Dual-core system detected."
    echo "Recommended: isolate core 1 for LinuxCNC real-time tasks"
elif [ $NUM_CPUS -eq 4 ]; then
    # For quad-core, opinions vary:
    # - Some prefer isolating only the last core (3)
    # - Others prefer isolating 2,3 or even 1,2,3
    # We'll default to last core only
    ISOLATE_CORES="3"
    echo "Quad-core system detected."
    echo "Recommended: isolate core 3 for LinuxCNC real-time tasks"
    echo "Alternative: you could isolate cores 2,3 or even 1,2,3"
else
    ISOLATE_CORES="$LAST_CORE"
    echo "Multi-core system detected."
    echo "Recommended: isolate core $LAST_CORE for LinuxCNC real-time tasks"
fi

echo ""
echo "Default recommendation: isolcpus=$ISOLATE_CORES"
echo ""
read -p "Press ENTER to use default ($ISOLATE_CORES), or type custom cores (e.g., '2,3' or '1,2,3'): " CUSTOM_CORES

if [ ! -z "$CUSTOM_CORES" ]; then
    ISOLATE_CORES="$CUSTOM_CORES"
fi

echo ""
echo "Will configure: isolcpus=$ISOLATE_CORES"
echo ""

# Check if script is run as root, if not re-run with sudo
if [ "$EUID" -ne 0 ]; then
    echo "This script requires root privileges. Re-running with sudo..."
    exec sudo "$0" "$@"
fi

# Backup GRUB configuration
GRUB_FILE="/etc/default/grub"
BACKUP_FILE="${GRUB_FILE}.backup.$(date +%Y%m%d_%H%M%S)"

echo "Backing up GRUB configuration to $BACKUP_FILE"
cp "$GRUB_FILE" "$BACKUP_FILE"

# Check current GRUB_CMDLINE_LINUX_DEFAULT
echo ""
echo "Current GRUB_CMDLINE_LINUX_DEFAULT:"
grep "^GRUB_CMDLINE_LINUX_DEFAULT=" "$GRUB_FILE" || echo "(not found)"
echo ""

# Build the kernel command line parameters
# isolcpus - isolates specified CPUs from the kernel scheduler
# idle=poll - prevents CPU from entering power-saving states (reduces latency)
KERNEL_PARAMS="isolcpus=$ISOLATE_CORES idle=poll"

echo "Adding kernel parameters: $KERNEL_PARAMS"
echo ""

# Check if GRUB_CMDLINE_LINUX_DEFAULT exists
if grep -q "^GRUB_CMDLINE_LINUX_DEFAULT=" "$GRUB_FILE"; then
    # Remove existing isolcpus and idle parameters if present
    sed -i 's/isolcpus=[^ "]* //g' "$GRUB_FILE"
    sed -i 's/idle=[^ "]* //g' "$GRUB_FILE"
    
    # Add new parameters
    sed -i "s/^\(GRUB_CMDLINE_LINUX_DEFAULT=\"[^\"]*\)/\1 $KERNEL_PARAMS/" "$GRUB_FILE"
else
    # Add new line if it doesn't exist
    echo "GRUB_CMDLINE_LINUX_DEFAULT=\"$KERNEL_PARAMS\"" >> "$GRUB_FILE"
fi

# Clean up any double spaces
sed -i 's/  */ /g' "$GRUB_FILE"

echo "Updated GRUB_CMDLINE_LINUX_DEFAULT:"
grep "^GRUB_CMDLINE_LINUX_DEFAULT=" "$GRUB_FILE"
echo ""

# Update GRUB
echo "Updating GRUB..."
if command -v update-grub &> /dev/null; then
    update-grub
elif command -v grub-mkconfig &> /dev/null; then
    grub-mkconfig -o /boot/grub/grub.cfg
else
    echo "ERROR: Could not find update-grub or grub-mkconfig"
    exit 1
fi

echo ""
echo "======================================"
echo "Configuration Summary"
echo "======================================"
echo ""
echo "✓ CPU cores to isolate: $ISOLATE_CORES"
echo "✓ Kernel parameters added: $KERNEL_PARAMS"
echo "✓ GRUB configuration updated"
echo "✓ Backup saved to: $BACKUP_FILE"
echo ""
echo "What this does:"
echo "  - isolcpus=$ISOLATE_CORES"
echo "    Removes core(s) $ISOLATE_CORES from the kernel scheduler"
echo "    LinuxCNC real-time threads will automatically use the isolated core(s)"
echo "    Other system processes will run on the remaining cores"
echo ""
echo "  - idle=poll"
echo "    Keeps CPU from entering power-saving states"
echo "    Reduces latency by preventing CPU wake-up delays"
echo ""
echo "⚠ IMPORTANT: You MUST REBOOT for changes to take effect!"
echo ""
echo "After reboot, verify with:"
echo "  cat /proc/cmdline"
echo ""
echo "To test latency after reboot, run LinuxCNC's latency test:"
echo "  latency-histogram"
echo "  or"
echo "  latency-test"
echo ""
echo "Additional optimization tips:"
echo "  1. Disable power management in BIOS"
echo "  2. Disable CPU frequency scaling (C-states, SpeedStep, etc.)"
echo "  3. Consider disabling hyperthreading in BIOS"
echo "  4. For Ethernet-based Mesa cards, pin network IRQs to non-isolated cores"
echo ""
read -p "Would you like to reboot now? (y/N): " REBOOT_NOW

if [[ "$REBOOT_NOW" =~ ^[Yy]$ ]]; then
    echo "Rebooting in 5 seconds... (Press Ctrl+C to cancel)"
    sleep 5
    reboot
else
    echo ""
    echo "Remember to reboot manually for changes to take effect!"
fi
