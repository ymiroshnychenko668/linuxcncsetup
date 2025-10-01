#!/bin/bash
# Launcher script for LinuxCNC CPU affinity setup

if [ ! -f /usr/local/bin/linuxcnc-cpu-setup.sh ]; then
    echo "Error: /usr/local/bin/linuxcnc-cpu-setup.sh not found"
    echo "Please run grub-configure-multicore.sh first to create the CPU setup script"
    exit 1
fi

sudo /usr/local/bin/linuxcnc-cpu-setup.sh
