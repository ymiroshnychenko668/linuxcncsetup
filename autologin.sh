
sudo apt install lightdm -y

# Enable LightDM for graphical login

sudo systemctl enable lightdm




# Configure LightDM for autologin
sudo sed -i '/^\[Seat:\*\]/a autologin-user='"$(whoami)"'\nautologin-user-timeout=0' /etc/lightdm/lightdm.conf
