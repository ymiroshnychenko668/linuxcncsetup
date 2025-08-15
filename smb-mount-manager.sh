#!/bin/bash

# SMB Mount Manager - Helper script for troubleshooting and managing SMB mounts
# Usage: ./smb-mount-manager.sh [command] [mount_point]

SCRIPT_NAME=$(basename "$0")

show_help() {
    cat << EOF
SMB Mount Manager - Helper script for managing SMB mounts

Usage: $SCRIPT_NAME [command] [mount_point]

Commands:
  status [mount_point]     - Show status of SMB mount
  check [mount_point]      - Check connectivity and mount health
  remount [mount_point]    - Force remount of SMB share
  unmount [mount_point]    - Unmount SMB share
  logs [mount_point]       - Show recent mount logs
  cleanup                  - Clean up failed mount attempts
  list                     - List all SMB mounts in fstab
  help                     - Show this help message

Examples:
  $SCRIPT_NAME status /mnt/smb_share
  $SCRIPT_NAME check /mnt/smb_share
  $SCRIPT_NAME remount /mnt/smb_share
  $SCRIPT_NAME list
  $SCRIPT_NAME cleanup

EOF
}

get_mount_info() {
    local mount_point="$1"
    if [ -z "$mount_point" ]; then
        echo "Error: Mount point not specified"
        return 1
    fi
    
    # Get SMB path from fstab
    SMB_PATH=$(grep " $mount_point " /etc/fstab | awk '{print $1}' | head -n1)
    if [ -z "$SMB_PATH" ]; then
        echo "Error: Mount point $mount_point not found in /etc/fstab"
        return 1
    fi
    
    # Extract server IP/hostname
    SERVER=$(echo "$SMB_PATH" | sed 's|//||' | cut -d'/' -f1)
    
    echo "Mount Point: $mount_point"
    echo "SMB Path: $SMB_PATH"
    echo "Server: $SERVER"
}

check_connectivity() {
    local server="$1"
    local port="${2:-445}"
    
    echo "Checking connectivity to $server:$port..."
    
    if command -v nc >/dev/null 2>&1; then
        if timeout 5 nc -z "$server" "$port" 2>/dev/null; then
            echo "✓ Server $server is reachable on port $port"
            return 0
        else
            echo "❌ Server $server is not reachable on port $port"
            return 1
        fi
    else
        if ping -c 1 -W 3 "$server" >/dev/null 2>&1; then
            echo "✓ Server $server is reachable (ping)"
            return 0
        else
            echo "❌ Server $server is not reachable (ping)"
            return 1
        fi
    fi
}

status_command() {
    local mount_point="$1"
    
    echo "=== SMB Mount Status ==="
    get_mount_info "$mount_point" || return 1
    
    local server=$(echo "$SMB_PATH" | sed 's|//||' | cut -d'/' -f1)
    local mount_unit=$(systemd-escape --path "$mount_point")
    
    echo ""
    echo "Mount Status:"
    if mountpoint -q "$mount_point"; then
        echo "✓ Currently mounted"
        
        # Show mount details
        mount | grep "$mount_point" || true
        
        # Show space usage
        echo ""
        echo "Disk Usage:"
        df -h "$mount_point" 2>/dev/null || echo "Could not get disk usage"
        
        # Test access
        echo ""
        echo "Access Test:"
        if [ -r "$mount_point" ]; then
            echo "✓ Read access: OK"
        else
            echo "❌ Read access: Failed"
        fi
        
        if [ -w "$mount_point" ]; then
            echo "✓ Write access: OK"
        else
            echo "❌ Write access: Failed"
        fi
        
    else
        echo "❌ Not currently mounted"
    fi
    
    echo ""
    echo "Systemd Status:"
    systemctl is-active "${mount_unit}.automount" >/dev/null 2>&1 && \
        echo "✓ Automount unit: Active" || echo "❌ Automount unit: Inactive"
    
    systemctl is-active "${mount_unit}.mount" >/dev/null 2>&1 && \
        echo "✓ Mount unit: Active" || echo "❌ Mount unit: Inactive"
    
    echo ""
    check_connectivity "$server"
}

check_command() {
    local mount_point="$1"
    
    echo "=== SMB Mount Health Check ==="
    get_mount_info "$mount_point" || return 1
    
    local server=$(echo "$SMB_PATH" | sed 's|//||' | cut -d'/' -f1)
    local mount_unit=$(systemd-escape --path "$mount_point")
    
    echo ""
    echo "1. Checking network connectivity..."
    if ! check_connectivity "$server"; then
        echo "   → Network issue detected. Check network connection and server availability."
    fi
    
    echo ""
    echo "2. Checking systemd units..."
    systemctl is-enabled "${mount_unit}.automount" >/dev/null 2>&1 && \
        echo "✓ Automount unit enabled" || echo "❌ Automount unit not enabled"
    
    echo ""
    echo "3. Checking credentials file..."
    if [ -f "/etc/cifs-credentials" ]; then
        echo "✓ Credentials file exists"
        ls -la /etc/cifs-credentials
    else
        echo "❌ Credentials file missing"
    fi
    
    echo ""
    echo "4. Checking mount point permissions..."
    if [ -d "$mount_point" ]; then
        echo "✓ Mount point exists"
        ls -ld "$mount_point"
    else
        echo "❌ Mount point does not exist"
    fi
    
    echo ""
    echo "5. Testing manual mount..."
    if mountpoint -q "$mount_point"; then
        echo "✓ Already mounted"
    else
        echo "Attempting manual mount..."
        if sudo mount "$mount_point" 2>/dev/null; then
            echo "✓ Manual mount successful"
        else
            echo "❌ Manual mount failed"
            echo "Check logs with: sudo journalctl -u ${mount_unit}.mount -n 20"
        fi
    fi
}

remount_command() {
    local mount_point="$1"
    
    echo "=== Remounting SMB Share ==="
    get_mount_info "$mount_point" || return 1
    
    local mount_unit=$(systemd-escape --path "$mount_point")
    
    echo "Stopping mount unit..."
    sudo systemctl stop "${mount_unit}.mount" 2>/dev/null || true
    
    echo "Waiting for unmount..."
    sleep 2
    
    echo "Starting mount unit..."
    sudo systemctl start "${mount_unit}.mount"
    
    echo "Checking mount status..."
    sleep 1
    
    if mountpoint -q "$mount_point"; then
        echo "✓ Remount successful"
    else
        echo "❌ Remount failed"
        return 1
    fi
}

unmount_command() {
    local mount_point="$1"
    
    echo "=== Unmounting SMB Share ==="
    get_mount_info "$mount_point" || return 1
    
    local mount_unit=$(systemd-escape --path "$mount_point")
    
    echo "Stopping automount unit..."
    sudo systemctl stop "${mount_unit}.automount" 2>/dev/null || true
    
    echo "Stopping mount unit..."
    sudo systemctl stop "${mount_unit}.mount" 2>/dev/null || true
    
    echo "Forcing unmount if needed..."
    sudo umount "$mount_point" 2>/dev/null || true
    
    if mountpoint -q "$mount_point"; then
        echo "❌ Failed to unmount $mount_point"
        return 1
    else
        echo "✓ Successfully unmounted $mount_point"
    fi
}

logs_command() {
    local mount_point="$1"
    
    echo "=== SMB Mount Logs ==="
    if [ -n "$mount_point" ]; then
        get_mount_info "$mount_point" || return 1
        local mount_unit=$(systemd-escape --path "$mount_point")
        echo "Recent logs for $mount_point:"
        sudo journalctl -u "${mount_unit}.mount" -u "${mount_unit}.automount" --no-pager -n 30
    else
        echo "Recent CIFS-related logs:"
        sudo journalctl --no-pager -n 50 | grep -i cifs || echo "No CIFS logs found"
    fi
}

cleanup_command() {
    echo "=== Cleaning up failed mounts ==="
    
    echo "Reloading systemd daemon..."
    sudo systemctl daemon-reload
    
    echo "Resetting failed units..."
    sudo systemctl reset-failed
    
    echo "Checking for stale mount points..."
    mount | grep cifs || echo "No CIFS mounts found"
    
    echo "Cleanup completed"
}

list_command() {
    echo "=== SMB Mounts in /etc/fstab ==="
    
    if grep -q "cifs" /etc/fstab; then
        echo "Found CIFS entries in /etc/fstab:"
        echo ""
        grep "cifs" /etc/fstab | while read line; do
            smb_path=$(echo "$line" | awk '{print $1}')
            mount_point=$(echo "$line" | awk '{print $2}')
            echo "SMB Path: $smb_path"
            echo "Mount Point: $mount_point"
            if mountpoint -q "$mount_point" 2>/dev/null; then
                echo "Status: ✓ Mounted"
            else
                echo "Status: ❌ Not mounted"
            fi
            echo ""
        done
    else
        echo "No CIFS entries found in /etc/fstab"
    fi
}

# Main script logic
case "${1:-help}" in
    "status")
        status_command "$2"
        ;;
    "check")
        check_command "$2"
        ;;
    "remount")
        remount_command "$2"
        ;;
    "unmount")
        unmount_command "$2"
        ;;
    "logs")
        logs_command "$2"
        ;;
    "cleanup")
        cleanup_command
        ;;
    "list")
        list_command
        ;;
    "help"|*)
        show_help
        ;;
esac
