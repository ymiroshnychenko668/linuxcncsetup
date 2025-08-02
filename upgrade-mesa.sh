#!/bin/bash

set -e

# Install dependencies
sudo apt update
sudo apt install -y build-essential git python3 python3-distutils python3-setuptools pkg-config libpci-dev libmd-dev
# Clone and build mesaflash
cd "$HOME"
if [ ! -d "$HOME/mesaflash" ]; then
    git clone https://github.com/LinuxCNC/mesaflash.git "$HOME/mesaflash"
fi
cd "$HOME/mesaflash"
make clean
make
sudo make install

# Clone latest HostMot2 firmware
cd "$HOME"
if [ ! -d "$HOME/hostmot2-firmware" ]; then
    git clone https://github.com/LinuxCNC/hostmot2-firmware.git "$HOME/hostmot2-firmware"
else
    cd "$HOME/hostmot2-firmware" && git pull
fi

# # Clone and build latest LinuxCNC drivers (hm2_eth) into linuxcnc-sources
# cd "$HOME"
# if [ ! -d "$HOME/linuxcnc-sources" ]; then
#     git clone https://github.com/LinuxCNC/linuxcnc.git "$HOME/linuxcnc-sources"
# else
#     cd "$HOME/linuxcnc-sources" && git pull
# fi

# cd "$HOME/linuxcnc-sources/src"
# ./autogen.sh
# ./configure --with-realtime=uspace
# make
# sudo make setuid

# Use non-root username
NON_ROOT_USER=$(logname)

# Verify HAL pins as non-root user
echo "Verifying HAL pins as $NON_ROOT_USER..."
sudo -u "$NON_ROOT_USER" halrun <<EOF
loadrt hostmot2
exit
EOF

echo "Mesa driver upgrade complete! Restart LinuxCNC to apply changes."
