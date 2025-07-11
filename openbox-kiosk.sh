#!/bin/bash

# Update and install required packages
sudo apt update
sudo apt install -y openbox obconf firefox-esr code xterm lightdm

# Register Openbox as an alternative window manager
sudo update-alternatives --install /usr/bin/x-window-manager x-window-manager /usr/bin/openbox-session 50
sudo update-alternatives --set x-window-manager /usr/bin/openbox-session

# Set Openbox as the default session manager
sudo update-alternatives --install /usr/bin/x-session-manager x-session-manager /usr/bin/openbox-session 50
sudo update-alternatives --set x-session-manager /usr/bin/openbox-session

# Enable LightDM service
sudo systemctl enable lightdm

# Configure LightDM for autologin
sudo sed -i '/^\[Seat:\*\]/a autologin-user='"$(whoami)"'\nautologin-user-timeout=0' /etc/lightdm/lightdm.conf

# Create .xsession file to start Openbox automatically
echo "exec openbox-session" > ~/.xsession
chmod +x ~/.xsession

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
linuxcnc ~/linuxcnc/configs/qtdragon/qtdragon.ini &

# Launch Visual Studio Code
code &

# Launch Firefox in kiosk mode
firefox-esr --kiosk --new-window "https://www.linuxcnc.org" &

# Launch terminal emulator
xterm &
EOL

# Confirmation message
echo "Openbox kiosk mode with autologin setup complete. Please reboot your system to apply changes."
