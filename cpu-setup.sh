#!/bin/bash
# Launcher script for LinuxCNC CPU affinity configuration

if [ ! -f ./configure-cpu-affinity.sh ]; then
    echo "Error: configure-cpu-affinity.sh not found in current directory"
    exit 1
fi

sudo ./configure-cpu-affinity.sh
