#!/bin/bash

# Exit immediately if a command exits with a non-zero status
set -e

# Update and install required packages
sudo apt update
sudo apt install -y i3-wm polybar rofi firefox-esr code i3status dex xss-lock network-manager-gnome pulseaudio-utils

# Register i3 as the default window and session manager
sudo update-alternatives --install /usr/bin/x-window-manager x-window-manager /usr/bin/i3 50
sudo update-alternatives --set x-window-manager /usr/bin/i3
sudo update-alternatives --install /usr/bin/x-session-manager x-session-manager /usr/bin/i3 50
sudo update-alternatives --set x-session-manager /usr/bin/i3

# Enable LightDM for graphical login
sudo systemctl enable lightdm

# Configure .xsession to start i3 automatically
echo "exec i3" > ~/.xsession
chmod +x ~/.xsession

# Setup Polybar minimal default configuration
mkdir -p ~/.config/polybar

# Copy Polybar configuration if available
if [ -f "./config.ini" ]; then
    cp ./config.ini ~/.config/polybar/config.ini
fi

# Copy Polybar helper scripts
mkdir -p ~/.config/polybar/scripts
for script in logout.sh reboot.sh shutdown.sh; do
    if [ -f "./$script" ]; then
        install -m 755 "./$script" ~/.config/polybar/scripts/
    fi
done

# Copy default i3 configuration WITHOUT modifications
mkdir -p ~/.config/i3
if [ -f "./config" ]; then
    cp ./config ~/.config/i3/config
else
    cp /etc/i3/config ~/.config/i3/config
fi

# Confirmation
printf "\ni3 setup completed with default config (no modifications). Reboot the system to apply changes.\n"


