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

# Setup Polybar configuration
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

# Setup i3 default configuration
mkdir -p ~/.config/i3
cp /etc/i3/config ~/.config/i3/config

# Fix duplicate binding ($mod+space)
sed -i 's/^bindsym $mod+space focus mode_toggle/# bindsym $mod+space focus mode_toggle/' ~/.config/i3/config

# Increment customizations into i3 config
cat >> ~/.config/i3/config <<'EOL'

# ---- Custom incremental changes ----

# Keybindings for Rofi
bindsym $mod+Tab exec --no-startup-id rofi -show window
bindsym $mod+space exec --no-startup-id rofi -show run

# Autostart Polybar
exec_always --no-startup-id polybar kiosk &

# Autostart applications
exec --no-startup-id firefox-esr
exec --no-startup-id linuxcnc ~/linuxcnc/configs/qtdragon/qtdragon.ini
exec --no-startup-id code

# Disable screen blanking/power saving
exec --no-startup-id xset s off
exec --no-startup-id xset s noblank
exec --no-startup-id xset -dpms

EOL

# Confirmation
printf "\ni3 kiosk setup complete. Reboot the system to apply changes.\n"
