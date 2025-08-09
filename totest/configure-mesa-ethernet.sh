#!/bin/bash

# MESA Ethernet Configuration Script for LinuxCNC
# This script configures network interfaces for MESA Ethernet cards
# Works universally on any Linux distribution with NetworkManager

set -e  # Exit on any error

# Configuration constants
MESA_NETWORK="192.168.1.0/24"
MESA_HOST_IP="192.168.1.1"
MESA_CARD_IP_BASE="192.168.1.1"  # Base IP for MESA cards (typically .121, .122, etc.)

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}=== MESA Ethernet Configuration Script ===${NC}"
echo "This script will configure your network interface for MESA Ethernet communication"
echo ""

# Function to print colored output
print_info() { echo -e "${BLUE}ℹ $1${NC}"; }
print_success() { echo -e "${GREEN}✓ $1${NC}"; }
print_warning() { echo -e "${YELLOW}⚠ $1${NC}"; }
print_error() { echo -e "${RED}✗ $1${NC}"; }

# Check if NetworkManager is available
if ! command -v nmcli &> /dev/null; then
    print_error "NetworkManager (nmcli) is not installed or not available"
    print_info "Please install NetworkManager: sudo apt install network-manager"
    exit 1
fi

# Function to detect available network interfaces
detect_interfaces() {
    print_info "Detecting available network interfaces..."
    
    # Get all ethernet interfaces
    local ethernet_interfaces=$(ip link show | grep -E "^[0-9]+: (eth|enp|eno|ens)" | cut -d: -f2 | tr -d ' ')
    
    if [ -z "$ethernet_interfaces" ]; then
        print_error "No ethernet interfaces found"
        exit 1
    fi
    
    echo "Available ethernet interfaces:"
    local i=1
    for iface in $ethernet_interfaces; do
        local status=$(ip link show $iface | grep -o "state [A-Z]*" | cut -d' ' -f2)
        local connection=$(nmcli -t -f DEVICE,CONNECTION device | grep "^$iface:" | cut -d: -f2)
        echo "  $i) $iface (Status: $status, Connection: ${connection:-"None"})"
        i=$((i+1))
    done
    
    echo "$ethernet_interfaces"
}

# Function to select interface
select_interface() {
    local interfaces=($1)
    
    if [ ${#interfaces[@]} -eq 1 ]; then
        echo "${interfaces[0]}"
        return
    fi
    
    echo ""
    read -p "Select interface number for MESA configuration (1-${#interfaces[@]}): " choice
    
    if [[ ! "$choice" =~ ^[0-9]+$ ]] || [ "$choice" -lt 1 ] || [ "$choice" -gt ${#interfaces[@]} ]; then
        print_error "Invalid selection"
        exit 1
    fi
    
    echo "${interfaces[$((choice-1))]}"
}

# Function to configure MESA ethernet
configure_mesa_ethernet() {
    local interface=$1
    local connection_name="MESA-$interface"
    
    print_info "Configuring $interface for MESA Ethernet..."
    
    # Check if connection already exists
    if nmcli connection show "$connection_name" &>/dev/null; then
        print_warning "Connection '$connection_name' already exists. Modifying existing connection..."
        nmcli connection modify "$connection_name" \
            ipv4.method manual \
            ipv4.addresses "$MESA_HOST_IP/24" \
            ipv4.gateway "" \
            ipv4.never-default yes \
            802-3-ethernet.auto-negotiate no \
            802-3-ethernet.speed 100 \
            802-3-ethernet.duplex full \
            connection.autoconnect yes
    else
        print_info "Creating new MESA connection '$connection_name'..."
        nmcli connection add \
            type ethernet \
            con-name "$connection_name" \
            ifname "$interface" \
            ipv4.method manual \
            ipv4.addresses "$MESA_HOST_IP/24" \
            ipv4.never-default yes \
            802-3-ethernet.auto-negotiate no \
            802-3-ethernet.speed 100 \
            802-3-ethernet.duplex full \
            connection.autoconnect yes
    fi
    
    # Apply the connection
    print_info "Activating MESA connection..."
    nmcli connection up "$connection_name"
    
    print_success "MESA Ethernet configured on $interface"
}

# Function to test MESA connectivity
test_mesa_connectivity() {
    local interface=$1
    
    print_info "Testing MESA connectivity..."
    
    # Wait a moment for interface to be ready
    sleep 2
    
    # Test common MESA card IP addresses
    local mesa_ips="192.168.1.121 192.168.1.122 192.168.1.123"
    local found_cards=0
    
    for ip in $mesa_ips; do
        print_info "Testing connection to MESA card at $ip..."
        if ping -c 1 -W 2 -I "$interface" "$ip" &>/dev/null; then
            print_success "MESA card found at $ip"
            found_cards=$((found_cards + 1))
        else
            print_warning "No response from $ip"
        fi
    done
    
    if [ $found_cards -eq 0 ]; then
        print_warning "No MESA cards detected. This is normal if cards are not powered or connected yet."
        print_info "To test manually: ping -I $interface 192.168.1.121"
    else
        print_success "Found $found_cards MESA card(s)"
    fi
}

# Function to show configuration summary
show_configuration() {
    local interface=$1
    local connection_name="MESA-$interface"
    
    echo ""
    print_info "=== MESA Configuration Summary ==="
    echo "Interface: $interface"
    echo "Connection: $connection_name"
    echo "Host IP: $MESA_HOST_IP/24"
    echo "Network: $MESA_NETWORK"
    echo "Speed: 100Mbps Full Duplex"
    echo "Auto-negotiate: Disabled"
    echo "Never default route: Yes"
    echo ""
    
    print_info "=== Current Interface Status ==="
    nmcli connection show "$connection_name" | grep -E "(ipv4|802-3|connection.autoconnect)"
    
    echo ""
    print_info "=== Network Route Information ==="
    ip route show dev "$interface" 2>/dev/null || print_warning "No routes found for $interface"
}

# Function to create udev rules for interface naming consistency
create_udev_rules() {
    local interface=$1
    
    print_info "Would you like to create udev rules for consistent interface naming? (y/n)"
    read -p "This ensures the interface always has the same name after reboots: " create_udev
    
    if [[ "$create_udev" =~ ^[Yy]$ ]]; then
        local mac_address=$(cat /sys/class/net/$interface/address)
        local udev_rule="SUBSYSTEM==\"net\", ACTION==\"add\", ATTR{address}==\"$mac_address\", NAME=\"mesa-eth\""
        
        print_info "Creating udev rule for $interface (MAC: $mac_address)..."
        echo "$udev_rule" | sudo tee /etc/udev/rules.d/70-mesa-ethernet.rules > /dev/null
        print_success "Udev rule created. Interface will be named 'mesa-eth' after reboot."
        print_warning "You may need to update your HAL files to use 'mesa-eth' instead of '$interface'"
    fi
}

# Main execution
main() {
    # Check for root privileges for some operations
    if [[ $EUID -ne 0 ]] && ! groups | grep -q sudo; then
        print_warning "Some operations may require sudo privileges"
    fi
    
    # Detect interfaces
    available_interfaces=$(detect_interfaces)
    interfaces_array=($available_interfaces)
    
    # Select interface
    print_info "Selecting interface for MESA configuration..."
    selected_interface=$(select_interface "$available_interfaces")
    
    print_info "Selected interface: $selected_interface"
    
    # Configure MESA ethernet
    configure_mesa_ethernet "$selected_interface"
    
    # Test connectivity
    test_mesa_connectivity "$selected_interface"
    
    # Show configuration
    show_configuration "$selected_interface"
    
    # Offer to create udev rules
    create_udev_rules "$selected_interface"
    
    echo ""
    print_success "MESA Ethernet configuration completed!"
    echo ""
    print_info "=== Next Steps ==="
    echo "1. Connect your MESA card to the configured interface"
    echo "2. Power on your MESA card"
    echo "3. Test connectivity: ping -I $selected_interface 192.168.1.121"
    echo "4. Configure your LinuxCNC HAL files to use this interface"
    echo ""
    print_info "=== HAL Configuration Example ==="
    echo "loadrt hostmot2"
    echo "loadrt hm2_eth board_ip=192.168.1.121"
    echo ""
    print_info "=== Troubleshooting ==="
    echo "• Check cable connections"
    echo "• Verify MESA card power"
    echo "• Check MESA card IP address (default usually 192.168.1.121)"
    echo "• Use 'mesaflash --device 7i92 --addr 192.168.1.121 --readhmid' to test"
}

# Run main function
main "$@"
