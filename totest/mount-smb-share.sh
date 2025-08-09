#!/bin/bash

# Ensure cifs-utils is installed
if ! dpkg -s cifs-utils >/dev/null 2>&1; then
    echo "Installing cifs-utils..."
    sudo apt update
    sudo apt install -y cifs-utils
fi

# Проверка аргумента
if [ -z "$1" ]; then
    echo "Usage: $0 /mount/point/path"
    exit 1
fi

MOUNT_POINT="$1"
SMB_PATH="//10.0.1.26/share"
FSTAB_LINE="$SMB_PATH  $MOUNT_POINT  cifs  guest,vers=3.0,uid=1000,gid=1000,x-systemd.automount,x-systemd.requires=network-online.target,_netdev  0  0"

# 1. Создаём точку монтирования
if [ ! -d "$MOUNT_POINT" ]; then
    echo "Creating mount point: $MOUNT_POINT"
    sudo mkdir -p "$MOUNT_POINT"
fi

# 2. Проверяем наличие строки в /etc/fstab
if grep -Fq "$SMB_PATH  $MOUNT_POINT" /etc/fstab; then
    echo "Entry already exists in /etc/fstab"
else
    echo "Adding entry to /etc/fstab"
    echo "$FSTAB_LINE" | sudo tee -a /etc/fstab
fi

# 3. Монтируем
echo "Mounting all filesystems..."
sudo mount -a

# 4. Проверка
if mountpoint -q "$MOUNT_POINT"; then
    echo "SMB share mounted successfully at $MOUNT_POINT"
else
    echo "Failed to mount SMB share at $MOUNT_POINT"
fi
