#!/bin/bash

# Update and install required packages
sudo apt update
sudo apt install -y i3 polybar rofi firefox-esr code lightdm i3status dex xss-lock nm-applet pulseaudio-utils

# Register i3 as the default window and session manager
sudo update-alternatives --install /usr/bin/x-window-manager x-window-manager /usr/bin/i3 50
sudo update-alternatives --set x-window-manager /usr/bin/i3
sudo update-alternatives --install /usr/bin/x-session-manager x-session-manager /usr/bin/i3 50
sudo update-alternatives --set x-session-manager /usr/bin/i3

# Enable LightDM for graphical login
sudo systemctl enable lightdm

# Configure LightDM for autologin
sudo sed -i '/^\[Seat:\*\]/a autologin-user='"$(whoami)"'\nautologin-user-timeout=0' /etc/lightdm/lightdm.conf

# Configure .xsession to start i3 automatically
echo "exec i3" > ~/.xsession
chmod +x ~/.xsession

# Setup Polybar default minimal configuration
mkdir -p ~/.config/polybar
cat > ~/.config/polybar/config.ini <<EOL
[bar/kiosk]
width = 100%
height = 30
background = #222
foreground = #fff
font-0 = "Noto Sans:size=10"
modules-center = launcher

[module/launcher]
type = custom/text
content = "🚀 Launch Apps"
click-left = rofi -show drun
EOL

# Copy default i3 configuration WITHOUT modifications
mkdir -p ~/.config/i3
cp config ~/.config/i3/config

mkdir -p ~/.config/polybar
cp config.ini ~/.config/polybar/config.ini
# Confirmation
printf "\ni3 setup completed with default config (no modifications). Reboot the system to apply changes.\n"
