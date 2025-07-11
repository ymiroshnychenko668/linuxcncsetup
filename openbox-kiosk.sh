#!/bin/bash

# Update and install required packages
sudo apt update
sudo apt install -y openbox obconf firefox code xterm

# Create Openbox autostart file
autostart_file="$HOME/.config/openbox/autostart"
mkdir -p $(dirname "$autostart_file")

# Write autostart commands for kiosk mode
cat > "$autostart_file" <<EOL
# Disable screen blanking
xset s off
xset s noblank
xset -dpms

# Launch LinuxCNC with specific QT Dragon configuration
linuxcnc /home/user/linuxcnc/configs/sim.qtdragon_hd.qtdragon_hd_xyz/qtdragon_hd_xyzab.ini

# Launch Visual Studio Code
code &

# Launch Firefox in kiosk mode
firefox-esr --kiosk --new-window "https://www.linuxcnc.org" &

# Launch terminal emulator
xterm &
EOL

# Set Openbox as the default window manager
sudo update-alternatives --set x-window-manager /usr/bin/openbox-session

# Create .xinitrc to start Openbox automatically
echo "exec openbox-session" > ~/.xinitrc

# Confirmation message
echo "Openbox kiosk mode setup complete. Please log out and log back in or reboot your system to apply changes."