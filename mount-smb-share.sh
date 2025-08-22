#!/bin/bash

# Script to mount SMB share with proper permissions and persistent mounting
# Usage: ./mount-smb-share.sh /mount/point/path

# Exit on any error
set -e

# Ensure cifs-utils is installed
if ! dpkg -s cifs-utils >/dev/null 2>&1; then
    echo "Installing cifs-utils..."
    sudo apt update
    sudo apt install -y cifs-utils
fi

# Get mount point from argument or prompt user
if [ -z "$1" ]; then
    echo "No mount point specified. Please enter the mount point path:"
    echo "Example: /mnt/smb_share"
    read -p "Mount point: " MOUNT_POINT
    
    # Validate user input
    if [ -z "$MOUNT_POINT" ]; then
        echo "Error: Mount point cannot be empty"
        exit 1
    fi
    
    # Ensure mount point starts with /
    if [[ ! "$MOUNT_POINT" =~ ^/ ]]; then
        echo "Error: Mount point must be an absolute path (starting with /)"
        exit 1
    fi
else
    MOUNT_POINT="$1"
fi

# Check if script is run as root, if not re-run with sudo
if [ "$EUID" -ne 0 ]; then
    echo "This script requires root privileges. Re-running with sudo..."
    exec sudo "$0" "$MOUNT_POINT"
fi
SMB_PATH="//10.0.1.26/share"

# Get the user who invoked sudo (or current user if run directly as root)
if [ -n "$SUDO_USER" ]; then
    REAL_USER="$SUDO_USER"
    USER_ID=$(id -u "$SUDO_USER")
    GROUP_ID=$(id -g "$SUDO_USER")
else
    REAL_USER="root"
    USER_ID=0
    GROUP_ID=0
fi

echo "Setting up SMB mount for user: $REAL_USER (uid=$USER_ID, gid=$GROUP_ID)"

# Create credentials file for better security (even for guest access)
CREDS_FILE="/etc/cifs-credentials"
if [ ! -f "$CREDS_FILE" ]; then
    echo "Creating credentials file..."
    cat > "$CREDS_FILE" << EOF
username=guest
password=
domain=
EOF
    chmod 600 "$CREDS_FILE"
fi

# Improved fstab line with better options
FSTAB_LINE="$SMB_PATH $MOUNT_POINT cifs credentials=$CREDS_FILE,vers=3.0,uid=$USER_ID,gid=$GROUP_ID,iocharset=utf8,file_mode=0755,dir_mode=0755,x-systemd.automount,x-systemd.device-timeout=10,x-systemd.mount-timeout=10,_netdev,noauto 0 0"

# 1. Create mount point with proper ownership
if [ ! -d "$MOUNT_POINT" ]; then
    echo "Creating mount point: $MOUNT_POINT"
    mkdir -p "$MOUNT_POINT"
fi

# Set proper ownership of mount point
chown "$USER_ID:$GROUP_ID" "$MOUNT_POINT"
chmod 755 "$MOUNT_POINT"

# 2. Remove any existing entries for this mount point to avoid duplicates
if grep -q "$MOUNT_POINT" /etc/fstab; then
    echo "Removing existing fstab entries for $MOUNT_POINT"
    sed -i "\|$MOUNT_POINT|d" /etc/fstab
fi

# 3. Add new entry to /etc/fstab
echo "Adding new entry to /etc/fstab"
echo "$FSTAB_LINE" >> /etc/fstab

# 4. Reload systemd daemon to recognize new mount units
echo "Reloading systemd daemon..."
systemctl daemon-reload

# 5. Enable and start the automount unit
MOUNT_UNIT=$(systemd-escape --path "$MOUNT_POINT").automount
echo "Enabling systemd automount unit: $MOUNT_UNIT"
systemctl enable "$MOUNT_UNIT" || true
systemctl start "$MOUNT_UNIT" || true

# 6. Test the mount
echo "Testing mount..."
sleep 2

# Try to access the mount point to trigger automount
ls "$MOUNT_POINT" >/dev/null 2>&1 || true
sleep 1

# 7. Verification
if mountpoint -q "$MOUNT_POINT"; then
    echo "✓ SMB share mounted successfully at $MOUNT_POINT"
    echo "✓ Mount point ownership: $(ls -ld "$MOUNT_POINT" | awk '{print $3":"$4}')"
    echo "✓ Testing write access..."
    
    # Test write access as the real user
    if [ "$REAL_USER" != "root" ]; then
        su - "$REAL_USER" -c "touch '$MOUNT_POINT/.test_write' && rm -f '$MOUNT_POINT/.test_write'" 2>/dev/null && \
        echo "✓ Write access confirmed for user $REAL_USER" || \
        echo "⚠ Warning: Write access test failed for user $REAL_USER"
    else
        touch "$MOUNT_POINT/.test_write" && rm -f "$MOUNT_POINT/.test_write" 2>/dev/null && \
        echo "✓ Write access confirmed for root" || \
        echo "⚠ Warning: Write access test failed"
    fi
else
    echo "❌ Failed to mount SMB share at $MOUNT_POINT"
    echo "Checking logs..."
    journalctl -u "$(systemd-escape --path "$MOUNT_POINT").mount" --no-pager -n 10 || true
    exit 1
fi

echo ""
echo "Setup completed successfully!"
echo "The SMB share will now mount automatically on boot and on access."
echo "Mount point: $MOUNT_POINT"
echo "SMB path: $SMB_PATH"
echo "Owner: $REAL_USER ($USER_ID:$GROUP_ID)"
echo ""
echo "To manually mount/unmount:"
echo "  sudo systemctl start $(systemd-escape --path "$MOUNT_POINT").mount"
echo "  sudo systemctl stop $(systemd-escape --path "$MOUNT_POINT").mount"
echo ""
echo "To check status:"
echo "  systemctl status $(systemd-escape --path "$MOUNT_POINT").automount"
echo "  systemctl status $(systemd-escape --path "$MOUNT_POINT").mount"
