#!/bin/bash
#
# Configure GRUB with real-time kernel parameters for LinuxCNC.
# Dynamically detects CPU count and isolates cores for RT tasks.
#
# Usage: sudo bash grub-configure-multicore.sh
#

set -e

# Must run as root
if [ "$(id -u)" -ne 0 ]; then
    echo "This script must be run as root."
    echo "Usage: sudo bash $0"
    exit 1
fi

GRUB_FILE="/etc/default/grub"

if [ ! -f "$GRUB_FILE" ]; then
    echo "ERROR: $GRUB_FILE not found"
    exit 1
fi

# Backup
BACKUP="${GRUB_FILE}.backup.$(date +%Y%m%d%H%M%S)"
cp "$GRUB_FILE" "$BACKUP"
echo "Backup saved to $BACKUP"

# Detect CPU cores
CPU_CORES=$(nproc)
echo "Detected $CPU_CORES CPU cores"

# Calculate isolation strategy
if [ "$CPU_CORES" -ge 6 ]; then
    ISOLCPUS="2-$((CPU_CORES-1))"
    IRQ_AFFINITY="0-1"
    KTHREAD_CPUS="0-1"
elif [ "$CPU_CORES" -ge 4 ]; then
    ISOLCPUS="2,3"
    IRQ_AFFINITY="0-1"
    KTHREAD_CPUS="0-1"
else
    ISOLCPUS="1"
    IRQ_AFFINITY="0"
    KTHREAD_CPUS="0"
fi

echo "Isolating CPUs: $ISOLCPUS"

# Build kernel command line
CMDLINE="quiet isolcpus=$ISOLCPUS nohz_full=$ISOLCPUS rcu_nocbs=$ISOLCPUS"
CMDLINE="$CMDLINE irqaffinity=$IRQ_AFFINITY kthread_cpus=$KTHREAD_CPUS"
CMDLINE="$CMDLINE processor.max_cstate=1 intel_idle.max_cstate=0 idle=poll"
CMDLINE="$CMDLINE mitigations=off nosoftlockup tsc=reliable clocksource=tsc"
CMDLINE="$CMDLINE nmi_watchdog=0 rcu_nocb_poll"

# Apply GRUB parameters
sed -i "s/^GRUB_CMDLINE_LINUX_DEFAULT=.*/GRUB_CMDLINE_LINUX_DEFAULT=\"$CMDLINE\"/" "$GRUB_FILE"
sed -i 's/^GRUB_TIMEOUT=.*/GRUB_TIMEOUT=0/' "$GRUB_FILE"
sed -i 's/^GRUB_TIMEOUT_STYLE=.*/GRUB_TIMEOUT_STYLE=hidden/' "$GRUB_FILE"

# Add recordfail timeout if not already present
if ! grep -q "^GRUB_RECORDFAIL_TIMEOUT=" "$GRUB_FILE"; then
    echo 'GRUB_RECORDFAIL_TIMEOUT=0' >> "$GRUB_FILE"
fi

# Enable recovery disable if commented out
sed -i 's/^#GRUB_DISABLE_RECOVERY=.*/GRUB_DISABLE_RECOVERY="true"/' "$GRUB_FILE"

# Update GRUB
echo "Updating GRUB..."
update-grub

echo ""
echo "GRUB configuration complete."
echo "  isolcpus=$ISOLCPUS"
echo "  irqaffinity=$IRQ_AFFINITY"
echo "  kthread_cpus=$KTHREAD_CPUS"
echo ""
echo "Reboot to apply changes."
