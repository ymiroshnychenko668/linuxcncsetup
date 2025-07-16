#!/bin/bash

# Update and install required packages
sudo apt update
sudo apt install -y i3 polybar rofi firefox-esr code lightdm

# Register i3 as the default window manager
sudo update-alternatives --install /usr/bin/x-window-manager x-window-manager /usr/bin/i3 50
sudo update-alternatives --set x-window-manager /usr/bin/i3

# Set i3 as the default session manager
sudo update-alternatives --install /usr/bin/x-session-manager x-session-manager /usr/bin/i3 50
sudo update-alternatives --set x-session-manager /usr/bin/i3

# Enable LightDM for graphical login
sudo systemctl enable lightdm

# Configure LightDM for autologin
sudo sed -i '/^\[Seat:\*\]/a autologin-user='"$(whoami)"'\nautologin-user-timeout=0' /etc/lightdm/lightdm.conf

# Configure .xsession to start i3 automatically
echo "exec i3" > ~/.xsession
chmod +x ~/.xsession

# Setup polybar configuration
mkdir -p ~/.config/polybar

# Minimal polybar configuration with rofi launcher
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

# Configure i3
mkdir -p ~/.config/i3
cat > ~/.config/i3/config <<EOL
# Set mod key to Super (Win key)
set \$mod Mod4

# Launch polybar
exec_always --no-startup-id polybar kiosk

# Launch startup applications
exec --no-startup-id firefox-esr
exec --no-startup-id linuxcnc ~/linuxcnc/configs/qtdragon/qtdragon.ini
exec --no-startup-id code

# Disable screen blanking
exec --no-startup-id xset s off
exec --no-startup-id xset s noblank
exec --no-startup-id xset -dpms

# Set default layout to tabbed
workspace_layout tabbed

# Basic i3 settings
workspace_auto_back_and_forth yes
focus_follows_mouse no
mouse_warping none
floating_modifier \$mod

# Rofi launcher
bindsym \$mod+d exec rofi -show drun
bindsym \$mod+space exec rofi -show run

# Switch windows
bindsym \$mod+Tab focus right

# Simple window management
for_window [class=".*"] border none, fullscreen enable

# Ensure i3 starts on workspace 1
exec --no-startup-id i3-msg workspace 1
EOL

# Confirmation
printf "\ni3 kiosk setup complete. Reboot the system to apply changes.\n"
