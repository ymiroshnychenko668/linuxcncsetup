#!/bin/bash

# Fix script for initramfs-tools dpkg error caused by raspi-firmware
# This issue occurs when raspi-firmware package is installed on a regular PC
# instead of a Raspberry Pi, causing initramfs update failures

set -e

echo "=== Fixing initramfs-tools dpkg configuration error ==="
echo "Issue: raspi-firmware scripts are trying to run on a non-Raspberry Pi system"
echo

# Function to check if we're on a Raspberry Pi
is_raspberry_pi() {
    if [ -f /sys/firmware/devicetree/base/model ] && grep -q "Raspberry Pi" /sys/firmware/devicetree/base/model 2>/dev/null; then
        return 0
    fi
    return 1
}

# Function to safely disable problematic scripts
disable_raspi_scripts() {
    local script_path="$1"
    local backup_path="${script_path}.disabled"
    
    if [ -f "$script_path" ] && [ -x "$script_path" ]; then
        echo "Disabling problematic script: $script_path"
        sudo mv "$script_path" "$backup_path"
        echo "Script moved to: $backup_path"
        return 0
    fi
    return 1
}

# Function to create a dummy script that exits cleanly
create_dummy_script() {
    local script_path="$1"
    echo "Creating dummy script: $script_path"
    sudo tee "$script_path" > /dev/null << 'EOF'
#!/bin/sh
# Dummy script to prevent raspi-firmware errors on non-Raspberry Pi systems
# Original script disabled because this is not a Raspberry Pi

echo "raspi-firmware: skipping on non-Raspberry Pi system"
exit 0
EOF
    sudo chmod +x "$script_path"
}

# Main fix logic
main() {
    echo "Checking system type..."
    
    if is_raspberry_pi; then
        echo "ERROR: This appears to be a Raspberry Pi system."
        echo "This fix script is designed for regular PC systems that have"
        echo "raspi-firmware installed incorrectly."
        echo "Please remove this script and investigate why raspi-firmware is failing."
        exit 1
    fi
    
    echo "Confirmed: This is not a Raspberry Pi system."
    echo "Proceeding with fix..."
    echo
    
    # List of problematic raspi-firmware scripts
    scripts_to_fix=(
        "/etc/initramfs/post-update.d/z50-raspi-firmware"
        "/etc/kernel/postinst.d/z50-raspi-firmware"
    )
    
    echo "Step 1: Disabling problematic raspi-firmware scripts..."
    
    for script in "${scripts_to_fix[@]}"; do
        if disable_raspi_scripts "$script"; then
            create_dummy_script "$script"
        else
            echo "Script not found or already disabled: $script"
        fi
    done
    
    echo
    echo "Step 2: Attempting to configure initramfs-tools..."
    
    # Try to configure the package again
    if sudo dpkg --configure initramfs-tools; then
        echo "✓ initramfs-tools configured successfully"
    else
        echo "✗ Failed to configure initramfs-tools"
        echo "Trying alternative approach..."
        
        # Force configuration of all packages
        sudo dpkg --configure -a
    fi
    
    echo
    echo "Step 3: Testing initramfs generation..."
    
    # Test that initramfs can be generated without errors
    if sudo update-initramfs -u; then
        echo "✓ initramfs update completed successfully"
    else
        echo "✗ initramfs update still failing"
        echo "You may need to remove the raspi-firmware package entirely:"
        echo "  sudo apt remove --purge raspi-firmware"
        exit 1
    fi
    
    echo
    echo "Step 4: Cleaning up any remaining package issues..."
    sudo apt-get -f install
    
    echo
    echo "=== Fix completed successfully! ==="
    echo
    echo "Summary of changes made:"
    echo "- Disabled raspi-firmware scripts that were causing failures"
    echo "- Replaced them with dummy scripts that exit cleanly"
    echo "- Configured initramfs-tools successfully"
    echo "- Verified initramfs generation works"
    echo
    echo "The disabled scripts are backed up with .disabled extension"
    echo "If you ever move this system to a Raspberry Pi, you can restore them."
    echo
    echo "Optional: If you don't need raspi-firmware at all, you can remove it:"
    echo "  sudo apt remove --purge raspi-firmware"
}

# Check if running as root
if [ "$EUID" -eq 0 ]; then
    echo "ERROR: Do not run this script as root."
    echo "The script will use sudo when needed."
    exit 1
fi

# Run the main function
main "$@"
