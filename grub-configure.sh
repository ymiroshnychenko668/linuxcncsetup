#!/bin/bash

# Backup the existing grub configuration
sudo cp /etc/default/grub "/etc/default/grub.backup.$(date +%Y%m%d%H%M%S)"

echo "Updating GRUB_CMDLINE_LINUX_DEFAULT for LinuxCNC..."

# Define the new GRUB parameters
NEW_PARAMS='GRUB_CMDLINE_LINUX_DEFAULT="quiet isolcpus=2,3 nohz_full=2,3 rcu_nocbs=2,3 irqaffinity=0-1 kthread_cpus=0-1 processor.max_cstate=1 intel_idle.max_cstate=0 idle=poll mitigations=off nosoftlockup tsc=reliable clocksource=tsc nmi_watchdog=0 splash"'

# Use sed to replace the existing GRUB_CMDLINE_LINUX_DEFAULT line
sudo sed -i 's/^GRUB_CMDLINE_LINUX_DEFAULT=.*/'"$NEW_PARAMS"'/g' /etc/default/grub

# Disable GRUB menu (set timeout to 0)
sudo sed -i 's/^GRUB_TIMEOUT=.*/GRUB_TIMEOUT=0/' /etc/default/grub
sudo sed -i 's/^GRUB_TIMEOUT_STYLE=.*/GRUB_TIMEOUT_STYLE=hidden/' /etc/default/grub

# Install Plymouth (splash screen manager)
sudo apt update
sudo apt install -y plymouth plymouth-themes

# Select Plymouth theme
sudo plymouth-set-default-theme -R spinner

# Update initramfs to apply the changes
sudo update-initramfs -u

# Update GRUB
sudo update-grub

# Configure LightDM for autologin
sudo sed -i '/^\[Seat:\*\]/a autologin-user='"$(whoami)"'\\nautologin-user-timeout=0' /etc/lightdm/lightdm.conf

# Confirmation message
echo "GRUB and splash screen updated successfully. Please reboot your system."
