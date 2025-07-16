#!/bin/bash

# Update and install required packages
sudo apt update
sudo apt install -y openbox obconf firefox-esr code xterm lightdm tint2

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

# Setup tint2 configuration directory
mkdir -p ~/.config/tint2

# Copy provided tint2 configuration files to tint2 configuration directory
cp "$(dirname "$0")"/horizontal-icon-only.tint2rc ~/.config/tint2/
cp "$(dirname "$0")"/tint2rc ~/.config/tint2/

# Create Openbox autostart file
autostart_file="$HOME/.config/openbox/autostart"
mkdir -p $(dirname "$autostart_file")

# Write autostart commands for kiosk mode
cat > "$autostart_file" <<EOL
# Disable screen blanking
xset s off
xset s noblank
xset -dpms

# Launch tint2 with specific configuration
tint2 -c ~/.config/tint2/horizontal-icon-only.tint2rc &

# Launch LinuxCNC with specific QT Dragon configuration
linuxcnc ~/linuxcnc/configs/qtdragon/qtdragon.ini &

# Launch Visual Studio Code
code &

# Launch Firefox in kiosk mode
firefox-esr --new-window "https://www.linuxcnc.org" &

# Launch terminal emulator
xterm &
EOL

# Configure Openbox to remove window decorations, force fullscreen (excluding tint2), and use a single desktop
openbox_config="$HOME/.config/openbox/rc.xml"
mkdir -p $(dirname "$openbox_config")
cat > "$openbox_config" <<EOL
<?xml version="1.0" encoding="UTF-8"?>
<openbox_config>
  <applications>
    <application class="Tint2">
      <decor>no</decor>
      <fullscreen>no</fullscreen>
      <layer>above</layer>
    </application>
    <application class="*">
      <decor>no</decor>
      <fullscreen>yes</fullscreen>
    </application>
  </applications>
  <desktops>
    <number>1</number>
  </desktops>
</openbox_config>
EOL

# Confirmation message
echo "Openbox kiosk mode with autologin, tint2 panel (visible), fullscreen apps, and single desktop setup complete. Please reboot your system to apply changes."
