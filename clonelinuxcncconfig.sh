#!/bin/bash

set -e

# Create configuration directories
mkdir -p "$HOME/linuxcnc/configs"
cd "$HOME/linuxcnc/configs"

# Clone the CorvusCNC repository if it does not already exist
if [ ! -d "corvuscnc" ]; then
  git clone git@github.com:ymiroshnychenko668/corvuscnc.git
else
  echo "corvuscnc repository already exists."
fi
