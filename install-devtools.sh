#!/bin/bash

# Update package repositories
sudo apt update

# Install Git
sudo apt install -y git

# Install Syncthing
sudo apt install -y syncthing

# Enable and start Syncthing service for the current user
systemctl --user enable syncthing
systemctl --user start syncthing

# Add Visual Studio Code repository and install
sudo apt install -y wget gpg
wget -qO- https://packages.microsoft.com/keys/microsoft.asc | gpg --dearmor > packages.microsoft.gpg
sudo install -o root -g root -m 644 packages.microsoft.gpg /usr/share/keyrings/
sudo sh -c 'echo "deb [arch=amd64 signed-by=/usr/share/keyrings/packages.microsoft.gpg] https://packages.microsoft.com/repos/vscode stable main" > /etc/apt/sources.list.d/vscode.list'
rm -f packages.microsoft.gpg

# Update repositories again and install VS Code
sudo apt update
sudo apt install -y code

# Final message
echo "Syncthing, Git, and Visual Studio Code installation complete."