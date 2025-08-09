#!/bin/bash

# Interactive i3 Kiosk Setup Script with LinuxCNC Integration
# This script sets up i3 window manager with polybar and allows selection of LinuxCNC configurations

set -e  # Exit immediately if a command exits with a non-zero status

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Function to print colored output
print_info() { echo -e "${BLUE}ℹ $1${NC}"; }
print_success() { echo -e "${GREEN}✓ $1${NC}"; }
print_warning() { echo -e "${YELLOW}⚠ $1${NC}"; }
print_error() { echo -e "${RED}✗ $1${NC}"; }

echo -e "${BLUE}=== Interactive i3 Kiosk Setup with LinuxCNC ===${NC}"
echo "This script will set up i3 window manager with polybar and LinuxCNC integration"
echo ""

# Function to find LinuxCNC configurations
find_linuxcnc_configs() {
    print_info "Searching for LinuxCNC configurations..."
    
    local config_files=()
    local config_names=()
    local config_paths=()
    
    # Search in common LinuxCNC configuration directories
    local search_dirs=(
        "$HOME/linuxcnc/configs"
        "/usr/share/linuxcnc/configs"
        "/opt/linuxcnc/configs"
        "/etc/linuxcnc/configs"
        "/usr/local/share/linuxcnc/configs"
    )
    
    for dir in "${search_dirs[@]}"; do
        if [ -d "$dir" ]; then
            while IFS= read -r -d '' ini_file; do
                config_files+=("$ini_file")
                # Extract config name from path
                local config_name=$(basename "$(dirname "$ini_file")").$(basename "$ini_file" .ini)
                config_names+=("$config_name")
                config_paths+=("$ini_file")
            done < <(find "$dir" -name "*.ini" -type f -print0 2>/dev/null)
        fi
    done
    
    # Also search current directory and subdirectories for any .ini files that might be LinuxCNC configs
    while IFS= read -r -d '' ini_file; do
        # Check if it's likely a LinuxCNC config by looking for LinuxCNC-specific content
        if grep -q -i -E "(EMC|DISPLAY.*axis|HAL|TRAJ)" "$ini_file" 2>/dev/null; then
            config_files+=("$ini_file")
            local config_name=$(basename "$(dirname "$ini_file")").$(basename "$ini_file" .ini)
            config_names+=("$config_name")
            config_paths+=("$ini_file")
        fi
    done < <(find "$PWD" -name "*.ini" -type f -print0 2>/dev/null)
    
    echo "${#config_files[@]} ${config_files[*]} ${config_names[*]} ${config_paths[*]}"
}

# Function to select LinuxCNC configuration
select_linuxcnc_config() {
    local count=$1
    shift
    local configs=($@)
    
    if [ $count -eq 0 ]; then
        print_warning "No LinuxCNC configurations found."
        print_info "You can manually specify a config path or skip LinuxCNC integration."
        echo ""
        echo "Options:"
        echo "  1) Skip LinuxCNC integration"
        echo "  2) Enter custom config path"
        read -p "Select option (1-2): " choice
        
        case $choice in
            1)
                echo "SKIP"
                return
                ;;
            2)
                read -p "Enter full path to LinuxCNC .ini file: " custom_path
                if [ -f "$custom_path" ]; then
                    echo "$custom_path"
                    return
                else
                    print_error "File not found: $custom_path"
                    echo "SKIP"
                    return
                fi
                ;;
            *)
                print_error "Invalid option"
                echo "SKIP"
                return
                ;;
        esac
    fi
    
    print_success "Found $count LinuxCNC configuration(s):"
    echo ""
    
    # Split the combined array back into separate arrays
    local files=()
    local names=()
    local paths=()
    
    for i in $(seq 1 $count); do
        files+=("${configs[$i]}")
    done
    
    for i in $(seq $((count + 1)) $((count * 2))); do
        names+=("${configs[$i]}")
    done
    
    for i in $(seq $((count * 2 + 1)) $((count * 3))); do
        paths+=("${configs[$i]}")
    done
    
    # Display options
    for i in $(seq 0 $((count - 1))); do
        echo "  $((i + 1))) ${names[$i]}"
        echo "      Path: ${paths[$i]}"
        echo ""
    done
    
    echo "  $((count + 1))) Skip LinuxCNC integration"
    echo "  $((count + 2))) Enter custom config path"
    
    echo ""
    read -p "Select LinuxCNC configuration (1-$((count + 2))): " choice
    
    if [[ ! "$choice" =~ ^[0-9]+$ ]] || [ "$choice" -lt 1 ] || [ "$choice" -gt $((count + 2)) ]; then
        print_error "Invalid selection"
        echo "SKIP"
        return
    fi
    
    if [ "$choice" -eq $((count + 1)) ]; then
        echo "SKIP"
        return
    elif [ "$choice" -eq $((count + 2)) ]; then
        read -p "Enter full path to LinuxCNC .ini file: " custom_path
        if [ -f "$custom_path" ]; then
            echo "$custom_path"
            return
        else
            print_error "File not found: $custom_path"
            echo "SKIP"
            return
        fi
    else
        local selected_index=$((choice - 1))
        echo "${paths[$selected_index]}"
        return
    fi
}

# Update and install required packages
print_info "Updating package repositories and installing required packages..."
sudo apt update
sudo apt install -y i3-wm polybar rofi firefox-esr code i3status dex xss-lock network-manager-gnome pulseaudio-utils

# Register i3 as the default window and session manager
print_info "Registering i3 as default window manager..."
sudo update-alternatives --install /usr/bin/x-window-manager x-window-manager /usr/bin/i3 50
sudo update-alternatives --set x-window-manager /usr/bin/i3
sudo update-alternatives --install /usr/bin/x-session-manager x-session-manager /usr/bin/i3 50
sudo update-alternatives --set x-session-manager /usr/bin/i3

# Enable LightDM for graphical login
print_info "Enabling LightDM for graphical login..."
sudo systemctl enable lightdm

# Configure .xsession to start i3 automatically
print_info "Configuring X session to start i3..."
echo "exec i3" > ~/.xsession
chmod +x ~/.xsession

# Find and select LinuxCNC configuration
print_info "Setting up LinuxCNC integration..."
configs_result=$(find_linuxcnc_configs)
config_data=($configs_result)
config_count=${config_data[0]}

selected_config=$(select_linuxcnc_config $config_count "${config_data[@]:1}")

if [ "$selected_config" != "SKIP" ]; then
    print_success "Selected LinuxCNC configuration: $selected_config"
    LINUXCNC_CONFIG="$selected_config"
else
    print_warning "Skipping LinuxCNC integration"
    LINUXCNC_CONFIG=""
fi

# Function to update polybar config with selected LinuxCNC configuration
update_polybar_config() {
    local config_file="$1"
    local linuxcnc_config="$2"
    
    if [ -n "$linuxcnc_config" ]; then
        # Update the LinuxCNC menu entry in polybar config
        sed -i "s|menu-0-1-exec = .*|menu-0-1-exec = linuxcnc $linuxcnc_config|" "$config_file"
        print_success "Updated polybar config with LinuxCNC: $linuxcnc_config"
    else
        # Remove LinuxCNC menu entry if no config selected
        sed -i '/menu-0-1 = Linux cnc/d' "$config_file"
        sed -i '/menu-0-1-exec = linuxcnc/d' "$config_file"
        # Renumber remaining menu items
        sed -i 's/menu-0-2/menu-0-1/g; s/menu-0-3/menu-0-2/g; s/menu-0-4/menu-0-3/g' "$config_file"
        print_info "Removed LinuxCNC from polybar menu"
    fi
}

# Function to update i3 config with selected LinuxCNC configuration
update_i3_config() {
    local config_file="$1"
    local linuxcnc_config="$2"
    
    if [ -n "$linuxcnc_config" ]; then
        # Update the autostart LinuxCNC line
        sed -i "s|exec --no-startup-id linuxcnc.*|exec --no-startup-id linuxcnc $linuxcnc_config|" "$config_file"
        print_success "Updated i3 config to autostart LinuxCNC: $linuxcnc_config"
    else
        # Remove LinuxCNC autostart line
        sed -i '/exec --no-startup-id linuxcnc/d' "$config_file"
        print_info "Removed LinuxCNC autostart from i3 config"
    fi
}

# Setup Polybar configuration
print_info "Setting up Polybar configuration..."
mkdir -p ~/.config/polybar

if [ -f "./config.ini" ]; then
    cp ./config.ini ~/.config/polybar/config.ini
    update_polybar_config ~/.config/polybar/config.ini "$LINUXCNC_CONFIG"
else
    print_warning "Polybar config.ini not found, creating basic configuration"
    # Create a basic polybar config if none exists
    cat > ~/.config/polybar/config.ini << 'EOF'
[colors]
background = #282A2E
foreground = #C5C8C6
primary = #F0C674

[bar/example]
width = 100%
height = 24pt
background = ${colors.background}
foreground = ${colors.foreground}
modules-left = xworkspaces menu-apps
modules-right = date

[module/xworkspaces]
type = internal/xworkspaces
label-active = %name%
label-active-padding = 1
label-occupied = %name%
label-occupied-padding = 1
label-empty = %name%
label-empty-padding = 1

[module/menu-apps]
type = custom/menu
expand-right = true
menu-0-0 = Browser
menu-0-0-exec = firefox-esr
label-open = Apps
label-close = x

[module/date]
type = internal/date
date = %Y-%m-%d %H:%M
EOF
    
    if [ -n "$LINUXCNC_CONFIG" ]; then
        # Add LinuxCNC to the basic config
        sed -i '/menu-0-0-exec = firefox-esr/a menu-0-1 = LinuxCNC\nmenu-0-1-exec = linuxcnc '"$LINUXCNC_CONFIG" ~/.config/polybar/config.ini
    fi
fi

# Copy Polybar helper scripts
print_info "Setting up Polybar helper scripts..."
mkdir -p ~/.config/polybar/scripts
for script in logout.sh reboot.sh shutdown.sh; do
    if [ -f "./$script" ]; then
        install -m 755 "./$script" ~/.config/polybar/scripts/
        print_success "Installed polybar script: $script"
    fi
done

# Create basic helper scripts if they don't exist
if [ ! -f ~/.config/polybar/scripts/logout.sh ]; then
    cat > ~/.config/polybar/scripts/logout.sh << 'EOF'
#!/bin/bash
i3-msg exit
EOF
    chmod +x ~/.config/polybar/scripts/logout.sh
fi

if [ ! -f ~/.config/polybar/scripts/reboot.sh ]; then
    cat > ~/.config/polybar/scripts/reboot.sh << 'EOF'
#!/bin/bash
systemctl reboot
EOF
    chmod +x ~/.config/polybar/scripts/reboot.sh
fi

if [ ! -f ~/.config/polybar/scripts/shutdown.sh ]; then
    cat > ~/.config/polybar/scripts/shutdown.sh << 'EOF'
#!/bin/bash
systemctl poweroff
EOF
    chmod +x ~/.config/polybar/scripts/shutdown.sh
fi

# Setup i3 configuration
print_info "Setting up i3 configuration..."
mkdir -p ~/.config/i3

if [ -f "./config" ]; then
    cp ./config ~/.config/i3/config
    update_i3_config ~/.config/i3/config "$LINUXCNC_CONFIG"
else
    print_info "Using default i3 configuration"
    cp /etc/i3/config ~/.config/i3/config
    
    # Add basic i3 configuration for polybar and LinuxCNC
    cat >> ~/.config/i3/config << 'EOF'

# Polybar
exec --no-startup-id polybar &

# Autostart applications
exec --no-startup-id firefox-esr
EOF
    
    if [ -n "$LINUXCNC_CONFIG" ]; then
        echo "exec --no-startup-id linuxcnc $LINUXCNC_CONFIG" >> ~/.config/i3/config
        echo "exec --no-startup-id code" >> ~/.config/i3/config
        
        # Add LinuxCNC-specific window rules
        cat >> ~/.config/i3/config << 'EOF'

# LinuxCNC window management
set $ws2 "2:CNC"
for_window [class="qtvcp"] floating enable, resize set 1920 1056, move to workspace $ws2, move position center
EOF
    fi
fi

# Summary
echo ""
print_success "=== i3 Kiosk Setup Complete ==="
print_info "Configuration Summary:"
echo "  • i3 window manager: Installed and configured"
echo "  • Polybar: Configured with custom menu"
echo "  • Rofi launcher: Available with Win+D"
echo "  • Firefox ESR: Will autostart"
echo "  • VS Code: Installed"

if [ -n "$LINUXCNC_CONFIG" ]; then
    echo "  • LinuxCNC: Will autostart with config: $LINUXCNC_CONFIG"
    echo "  • LinuxCNC workspace: Workspace 2 (CNC)"
else
    echo "  • LinuxCNC: Not configured (skipped)"
fi

echo ""
print_info "=== Next Steps ==="
echo "1. Reboot the system to apply all changes"
echo "2. Login with your user account"
echo "3. i3 will start automatically"
echo "4. Use polybar menu (top-left 'Apps') to launch applications"
echo "5. Use Win+D for Rofi application launcher"

if [ -n "$LINUXCNC_CONFIG" ]; then
    echo "6. LinuxCNC will auto-launch and be available on workspace 2"
fi

echo ""
print_warning "Reboot recommended to ensure all settings take effect."
print_success "i3 kiosk setup completed successfully!"


