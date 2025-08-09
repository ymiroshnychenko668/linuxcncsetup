
#!/bin/bash

# Exit immediately if a command exits with a non-zero status
set -e

# Install LightDM display manager
sudo apt update
sudo apt install -y lightdm

# Enable LightDM for graphical login
sudo systemctl enable lightdm

# Configure LightDM for autologin
sudo sed -i '/^\[Seat:\*\]/a autologin-user='"$(whoami)"'\nautologin-user-timeout=0' /etc/lightdm/lightdm.conf

# Confirmation message
echo "LightDM autologin configured successfully for user $(whoami). Please reboot your system to apply changes."
