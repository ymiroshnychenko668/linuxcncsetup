#!/bin/bash

# Permanent prevention script for raspi-firmware conflicts on PC systems
# This script sets up preventive measures to avoid future raspi-firmware issues

set -e

echo "=== Setting up permanent raspi-firmware conflict prevention ==="
echo

# Function to check if we're on a Raspberry Pi
is_raspberry_pi() {
    if [ -f /sys/firmware/devicetree/base/model ] && grep -q "Raspberry Pi" /sys/firmware/devicetree/base/model 2>/dev/null; then
        return 0
    fi
    return 1
}

# Function to create APT preferences to prevent raspi-firmware installation
create_apt_preferences() {
    local pref_file="/etc/apt/preferences.d/99-no-raspi-firmware"
    
    echo "Creating APT preference to prevent raspi-firmware installation..."
    sudo tee "$pref_file" > /dev/null << 'EOF'
# Prevent raspi-firmware from being installed on non-Raspberry Pi systems
# This package is only needed on Raspberry Pi hardware

Package: raspi-firmware
Pin: origin *
Pin-Priority: -1

Package: raspberrypi-bootloader
Pin: origin *
Pin-Priority: -1

Package: raspberrypi-kernel
Pin: origin *
Pin-Priority: -1
EOF
    
    echo "APT preferences created: $pref_file"
}

# Function to create a system check script
create_system_check_script() {
    local check_script="/usr/local/bin/check-raspi-firmware"
    
    echo "Creating system check script..."
    sudo tee "$check_script" > /dev/null << 'EOF'
#!/bin/bash
# System check script to detect and warn about raspi-firmware on PC systems

# Function to check if we're on a Raspberry Pi
is_raspberry_pi() {
    if [ -f /sys/firmware/devicetree/base/model ] && grep -q "Raspberry Pi" /sys/firmware/devicetree/base/model 2>/dev/null; then
        return 0
    fi
    return 1
}

# Check if raspi-firmware is installed on a non-Pi system
if ! is_raspberry_pi && dpkg -l | grep -q raspi-firmware; then
    echo "WARNING: raspi-firmware is installed on a non-Raspberry Pi system!"
    echo "This can cause initramfs-tools configuration failures."
    echo "Consider running: sudo apt remove --purge raspi-firmware"
    echo "Or run the fix script: /home/user/linuxcncsetup/fix-initramfs-tools.sh"
    exit 1
fi

exit 0
EOF
    
    sudo chmod +x "$check_script"
    echo "System check script created: $check_script"
}

# Function to create a systemd service for the check
create_systemd_service() {
    local service_file="/etc/systemd/system/check-raspi-firmware.service"
    
    echo "Creating systemd service for raspi-firmware checking..."
    sudo tee "$service_file" > /dev/null << 'EOF'
[Unit]
Description=Check for raspi-firmware conflicts on PC systems
After=multi-user.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/check-raspi-firmware
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF
    
    echo "Systemd service created: $service_file"
    
    # Enable the service
    sudo systemctl daemon-reload
    sudo systemctl enable check-raspi-firmware.service
    echo "Service enabled to run at boot"
}

# Function to add package installation hook
create_dpkg_hook() {
    local hook_file="/etc/apt/apt.conf.d/99-check-raspi-firmware"
    
    echo "Creating dpkg hook to check for raspi-firmware installations..."
    sudo tee "$hook_file" > /dev/null << 'EOF'
// Check for raspi-firmware package installations on non-Pi systems
DPkg::Post-Invoke {
    "if [ -x /usr/local/bin/check-raspi-firmware ]; then /usr/local/bin/check-raspi-firmware; fi";
};
EOF
    
    echo "dpkg hook created: $hook_file"
}

# Main function
main() {
    echo "Checking system type..."
    
    if is_raspberry_pi; then
        echo "This is a Raspberry Pi system - no prevention needed."
        echo "raspi-firmware is appropriate for this system."
        exit 0
    fi
    
    echo "Confirmed: This is not a Raspberry Pi system."
    echo "Setting up prevention measures..."
    echo
    
    # Check if raspi-firmware is currently installed
    if dpkg -l | grep -q raspi-firmware; then
        echo "WARNING: raspi-firmware is currently installed!"
        echo "You should run the fix script first: ./fix-initramfs-tools.sh"
        echo "Or remove the package: sudo apt remove --purge raspi-firmware"
        echo
        read -p "Continue with prevention setup anyway? [y/N]: " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            echo "Aborted."
            exit 1
        fi
    fi
    
    echo "Step 1: Creating APT preferences to block raspi-firmware..."
    create_apt_preferences
    
    echo
    echo "Step 2: Creating system check script..."
    create_system_check_script
    
    echo
    echo "Step 3: Setting up systemd service..."
    create_systemd_service
    
    echo
    echo "Step 4: Creating package installation hook..."
    create_dpkg_hook
    
    echo
    echo "=== Prevention setup completed! ==="
    echo
    echo "Preventive measures installed:"
    echo "1. APT preferences to block raspi-firmware installation"
    echo "2. System check script that warns about conflicts"
    echo "3. Systemd service that checks at boot"
    echo "4. Package installation hook for real-time monitoring"
    echo
    echo "These measures will help prevent future raspi-firmware conflicts."
    echo "You can disable them later if needed by removing the files created."
}

# Check if running as root
if [ "$EUID" -eq 0 ]; then
    echo "ERROR: Do not run this script as root."
    echo "The script will use sudo when needed."
    exit 1
fi

# Run the main function
main "$@"
