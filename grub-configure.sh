#!/bin/bash

# Backup the existing grub configuration
sudo cp /etc/default/grub /etc/default/grub.backup.$(date +%Y%m%d%H%M%S)

echo "Updating GRUB_CMDLINE_LINUX_DEFAULT for LinuxCNC..."

# Define the new GRUB parameters
NEW_PARAMS='GRUB_CMDLINE_LINUX_DEFAULT="quiet isolcpus=2,3 nohz_full=2,3 rcu_nocbs=2,3 irqaffinity=0-1 kthread_cpus=0-1"'

# Use sed to replace the existing GRUB_CMDLINE_LINUX_DEFAULT line
sudo sed -i 's/^GRUB_CMDLINE_LINUX_DEFAULT=.*/'"$NEW_PARAMS"'/g' /etc/default/grub

# Update GRUB
sudo update-grub

# Confirmation message
echo "GRUB updated successfully. Please reboot your system."