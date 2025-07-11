#!/bin/bash

# Exit immediately if a command exits with a non-zero status
set -e

# Install dependencies
sudo apt update
sudo apt install -y build-essential git python3 python3-distutils python3-setuptools pkg-config libpci-dev libmd-dev automake autoconf libtool libmodbus-dev libusb-1.0-0-dev yapps2 intltool libboost-all-dev tk-dev libeditreadline-dev libxmu-dev asciidoc asciidoctor dblatex

# Clone and build mesaflash
if [ ! -d "$HOME/mesaflash" ]; then
    git clone https://github.com/LinuxCNC/mesaflash.git "$HOME/mesaflash"
fi
cd "$HOME/mesaflash"
make clean
make
sudo make install

# Clone latest HostMot2 firmware
if [ ! -d "$HOME/hostmot2-firmware" ]; then
    git clone https://github.com/LinuxCNC/hostmot2-firmware.git "$HOME/hostmot2-firmware"
else
    cd "$HOME/hostmot2-firmware" && git pull
fi

# Clone and build latest LinuxCNC drivers (hm2_eth)
if [ ! -d "$HOME/linuxcnc" ]; then
    git clone https://github.com/LinuxCNC/linuxcnc.git "$HOME/linuxcnc"
else
    cd "$HOME/linuxcnc" && git pull
fi

cd "$HOME/linuxcnc/src"
./autogen.sh
./configure --with-realtime=uspace
make
sudo make setuid

#
# Replace "your-username" with your actual non-root username
NON_ROOT_USER=$(logname)

echo "Verifying HAL pins as $NON_ROOT_USER..."
sudo -u "$NON_ROOT_USER" halrun <<EOF
loadrt hostmot2
exit
EOF

echo "Mesa driver upgrade complete! Restart LinuxCNC to apply changes."
