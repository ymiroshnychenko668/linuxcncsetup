#!/bin/bash

# Backup the existing grub configuration
sudo cp /etc/default/grub "/etc/default/grub.backup.$(date +%Y%m%d%H%M%S)"

echo "Updating GRUB configuration for LinuxCNC..."

# Detect number of CPU cores
CPU_CORES=$(nproc)
echo "Detected $CPU_CORES CPU cores"

# Calculate CPU isolation strategy for dual-processor systems
if [ "$CPU_CORES" -ge 8 ]; then
    ISOLCPUS="2-$((CPU_CORES-1))"
    NOHZ_FULL="2-$((CPU_CORES-1))"
    RCU_NOCBS="2-$((CPU_CORES-1))"
    IRQ_AFFINITY="0-1"
    KTHREAD_CPUS="0-1"
elif [ "$CPU_CORES" -ge 6 ]; then
    ISOLCPUS="2-$((CPU_CORES-1))"
    NOHZ_FULL="2-$((CPU_CORES-1))"
    RCU_NOCBS="2-$((CPU_CORES-1))"
    IRQ_AFFINITY="0-1"
    KTHREAD_CPUS="0-1"
elif [ "$CPU_CORES" -ge 4 ]; then
    ISOLCPUS="2,3"
    NOHZ_FULL="2,3"
    RCU_NOCBS="2,3"
    IRQ_AFFINITY="0-1"
    KTHREAD_CPUS="0-1"
else
    ISOLCPUS="1-$((CPU_CORES-1))"
    NOHZ_FULL="1-$((CPU_CORES-1))"
    RCU_NOCBS="1-$((CPU_CORES-1))"
    IRQ_AFFINITY="0"
    KTHREAD_CPUS="0"
fi

# Define the new GRUB parameters with dynamic CPU isolation
NEW_PARAMS="GRUB_CMDLINE_LINUX_DEFAULT=\"quiet isolcpus=$ISOLCPUS nohz_full=$NOHZ_FULL rcu_nocbs=$RCU_NOCBS irqaffinity=$IRQ_AFFINITY kthread_cpus=$KTHREAD_CPUS processor.max_cstate=1 intel_idle.max_cstate=0 idle=poll mitigations=off nosoftlockup tsc=reliable clocksource=tsc nmi_watchdog=0 rcu_nocb_poll pci=nommconf isolcpus_reboot=1 splash\""

# Use sed to replace the existing GRUB_CMDLINE_LINUX_DEFAULT line
sudo sed -i 's/^GRUB_CMDLINE_LINUX_DEFAULT=.*/'"$NEW_PARAMS"'/g' /etc/default/grub

# Disable GRUB menu (set timeout to 0)
sudo sed -i 's/^GRUB_TIMEOUT=.*/GRUB_TIMEOUT=0/' /etc/default/grub
sudo sed -i 's/^GRUB_TIMEOUT_STYLE=.*/GRUB_TIMEOUT_STYLE=hidden/' /etc/default/grub

# Set GRUB recordfail timeout to 0 to prevent boot delays
echo "GRUB_RECORDFAIL_TIMEOUT=0" | sudo tee -a /etc/default/grub > /dev/null 2>&1

# Enable GRUB_DISABLE_RECOVERY to remove recovery mode entries
sudo sed -i 's/^#GRUB_DISABLE_RECOVERY=.*/GRUB_DISABLE_RECOVERY="true"/' /etc/default/grub

# Install Plymouth (splash screen manager)
echo "Installing Plymouth splash screen..."
sudo apt update
sudo apt install -y plymouth plymouth-themes

# Select Plymouth theme
sudo plymouth-set-default-theme -R spinner

# Update initramfs to apply the changes
echo "Updating initramfs..."
sudo update-initramfs -u

# Update GRUB
echo "Updating GRUB..."
sudo update-grub

# Install and configure LightDM
echo "Installing LightDM..."
sudo apt install lightdm -y

# Enable LightDM for graphical login
sudo systemctl enable lightdm

# Configure LightDM for autologin
echo "Configuring LightDM autologin..."
sudo sed -i '/^\[Seat:\*\]/a autologin-user='"$(whoami)"'\nautologin-user-timeout=0' /etc/lightdm/lightdm.conf

echo ""
echo "============================================"
echo "Configuration Summary"
echo "============================================"
echo "CPU cores: $CPU_CORES"
echo "Isolated CPUs: $ISOLCPUS"
echo "System CPUs: $IRQ_AFFINITY"
echo ""
echo "GRUB configuration completed successfully."
echo ""
echo "Next steps:"
echo "1. Run: sudo ./configure-cpu-affinity.sh"
echo "2. Configure IRQ affinities interactively"
echo "3. Reboot to apply all changes"
echo ""
