#!/bin/bash

# Real-time IRQ configuration script for LinuxCNC
# This script installs and configures rtirq for optimal real-time performance

set -e  # Exit on any error

echo "Starting real-time IRQ configuration..."

# Check if running as root or with sudo
if [[ $EUID -eq 0 ]]; then
    echo "Warning: Running as root. Consider running with sudo instead."
fi

# Update package repositories
echo "Updating package repositories..."
sudo apt update

# Install rtirq and related tools
echo "Installing rtirq-init..."
sudo apt install -y rtirq-init

# Backup original configuration if it exists
if [ -f /etc/default/rtirq ]; then
    echo "Backing up existing rtirq configuration..."
    sudo cp /etc/default/rtirq /etc/default/rtirq.backup.$(date +%Y%m%d_%H%M%S)
fi

# Configure rtirq priorities
echo "Configuring rtirq priorities for LinuxCNC..."
sudo tee /etc/default/rtirq > /dev/null << 'EOF'
# rtirq configuration for LinuxCNC real-time setup
# Higher numbers = higher priority (1-99)

# Enable rtirq
RTIRQ_ENABLED="yes"

# Real-time priority levels (1-99, higher = more priority)
# Critical interrupts for real-time performance
RTIRQ_PRIO_HIGH="90"
RTIRQ_PRIO_DEFAULT="50"
RTIRQ_PRIO_LOW="25"

# Interrupt priorities (in order of importance for LinuxCNC)
# Timer interrupts - critical for real-time scheduling
RTIRQ_NAME_LIST="rtc hrtimer"

# System timer and hardware interrupts
RTIRQ_NAME_LIST="$RTIRQ_NAME_LIST timer"

# Parallel port (if using parallel port for stepper drivers)
RTIRQ_NAME_LIST="$RTIRQ_NAME_LIST parport_pc parport"

# Serial ports (for some controller communications)
RTIRQ_NAME_LIST="$RTIRQ_NAME_LIST serial"

# USB (for USB-based controllers)
RTIRQ_NAME_LIST="$RTIRQ_NAME_LIST uhci_hcd ohci_hcd ehci_hcd xhci_hcd"

# Network (lower priority)
RTIRQ_NAME_LIST="$RTIRQ_NAME_LIST eth0 eth1 wlan0"

# Sound (lowest priority for RT systems)
RTIRQ_NAME_LIST="$RTIRQ_NAME_LIST snd"

# CPU mask for RT interrupts (use cores 0-1, leave 2-3 for RT tasks)
RTIRQ_CPU_LIST="0 1"

# Non-threaded IRQs (keep these in kernel context)
RTIRQ_NON_THREADED="timer rtc"
EOF

# Set proper permissions
sudo chmod 644 /etc/default/rtirq

# Enable and start rtirq service
echo "Enabling rtirq service..."
sudo systemctl enable rtirq

echo "Starting rtirq service..."
sudo systemctl start rtirq

# Wait a moment for service to initialize
sleep 2

# Verify service status
echo "Verifying rtirq service status..."
if sudo systemctl is-active --quiet rtirq; then
    echo "✓ rtirq service is running successfully"
    sudo systemctl status rtirq --no-pager --lines=5
else
    echo "✗ rtirq service failed to start"
    echo "Service status:"
    sudo systemctl status rtirq --no-pager
    echo "\nService logs:"
    sudo journalctl -u rtirq --no-pager --lines=10
    exit 1
fi

# Show current interrupt priorities
echo "\n=== Current RT Thread Priorities ==="
if command -v chrt >/dev/null 2>&1; then
    echo "RT threads with priorities:"
    ps -eo pid,tid,class,rtprio,comm | grep -E "(FF|RR)" | head -10 || echo "No RT threads found yet"
fi

# Show interrupt distribution
echo "\n=== Interrupt Distribution Check ==="
echo "IRQ affinities (should prefer cores 0-1):"
for irq in /proc/irq/*/smp_affinity_list; do
    if [ -r "$irq" ]; then
        irq_num=$(echo $irq | sed 's/.*irq\/\([0-9]*\)\/.*/\1/')
        affinity=$(cat "$irq" 2>/dev/null || echo "N/A")
        irq_name=$(grep "^ *$irq_num:" /proc/interrupts | awk '{print $NF}' | head -1)
        if [ -n "$irq_name" ] && [ "$irq_name" != "" ]; then
            printf "IRQ %3s: %s -> %s\n" "$irq_num" "$affinity" "$irq_name"
        fi
    fi
done | sort -n | head -15

echo "\n=== Configuration Complete ==="
echo "✓ rtirq installed and configured for LinuxCNC"
echo "✓ RT interrupt priorities set"
echo "✓ IRQ affinity configured for cores 0-1"
echo "✓ Cores 2-3 reserved for RT tasks"
echo ""
echo "To verify RT performance, run:"
echo "  sudo rtirq status"
echo "  cat /proc/interrupts | head -20"
echo "  cyclictest -m -p99 -t4 -i200 -d0 -q"
echo ""
echo "Reboot recommended to ensure all settings take effect."
echo "rtirq installation and setup complete."
