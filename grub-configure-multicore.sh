#!/bin/bash

# Backup the existing grub configuration
sudo cp /etc/default/grub "/etc/default/grub.backup.$(date +%Y%m%d%H%M%S)"

echo "Updating GRUB_CMDLINE_LINUX_DEFAULT for LinuxCNC on dual-processor system..."

# Detect number of CPU cores
CPU_CORES=$(nproc)
echo "Detected $CPU_CORES CPU cores"

# Calculate CPU isolation strategy for dual-processor systems
if [ "$CPU_CORES" -ge 8 ]; then
    # For 8+ cores: Reserve first 2 cores for system, isolate the rest for real-time
    ISOLCPUS="2-$((CPU_CORES-1))"
    NOHZ_FULL="2-$((CPU_CORES-1))"
    RCU_NOCBS="2-$((CPU_CORES-1))"
    IRQ_AFFINITY="0-1"
    KTHREAD_CPUS="0-1"
    echo "Dual-processor configuration: Isolating CPUs $ISOLCPUS for real-time tasks"
elif [ "$CPU_CORES" -ge 6 ]; then
    # For 6-7 cores: Reserve first 2 cores for system, isolate the rest
    ISOLCPUS="2-$((CPU_CORES-1))"
    NOHZ_FULL="2-$((CPU_CORES-1))"
    RCU_NOCBS="2-$((CPU_CORES-1))"
    IRQ_AFFINITY="0-1"
    KTHREAD_CPUS="0-1"
    echo "Multi-core configuration: Isolating CPUs $ISOLCPUS for real-time tasks"
elif [ "$CPU_CORES" -ge 4 ]; then
    # For 4-5 cores: Reserve first 2 cores for system, isolate 2-3 cores
    ISOLCPUS="2,3"
    NOHZ_FULL="2,3"
    RCU_NOCBS="2,3"
    IRQ_AFFINITY="0-1"
    KTHREAD_CPUS="0-1"
    echo "Quad-core configuration: Isolating CPUs $ISOLCPUS for real-time tasks"
else
    # For 2-3 cores: Reserve first core, isolate the rest
    ISOLCPUS="1-$((CPU_CORES-1))"
    NOHZ_FULL="1-$((CPU_CORES-1))"
    RCU_NOCBS="1-$((CPU_CORES-1))"
    IRQ_AFFINITY="0"
    KTHREAD_CPUS="0"
    echo "Dual-core configuration: Isolating CPUs $ISOLCPUS for real-time tasks"
fi

# Define the new GRUB parameters with dynamic CPU isolation
NEW_PARAMS="GRUB_CMDLINE_LINUX_DEFAULT=\"quiet isolcpus=$ISOLCPUS nohz_full=$NOHZ_FULL rcu_nocbs=$RCU_NOCBS irqaffinity=$IRQ_AFFINITY kthread_cpus=$KTHREAD_CPUS processor.max_cstate=1 intel_idle.max_cstate=0 idle=poll mitigations=off nosoftlockup tsc=reliable clocksource=tsc nmi_watchdog=0 rcu_nocb_poll pci=nommconf isolcpus_reboot=1 splash\""

echo "Applying GRUB parameters: $NEW_PARAMS"

# Use sed to replace the existing GRUB_CMDLINE_LINUX_DEFAULT line
sudo sed -i 's/^GRUB_CMDLINE_LINUX_DEFAULT=.*/'"$NEW_PARAMS"'/g' /etc/default/grub

# Disable GRUB menu (set timeout to 0)
sudo sed -i 's/^GRUB_TIMEOUT=.*/GRUB_TIMEOUT=0/' /etc/default/grub
sudo sed -i 's/^GRUB_TIMEOUT_STYLE=.*/GRUB_TIMEOUT_STYLE=hidden/' /etc/default/grub

# Additional optimizations for dual-processor systems
echo "Applying additional dual-processor optimizations..."

# Set GRUB recordfail timeout to 0 to prevent boot delays
echo "GRUB_RECORDFAIL_TIMEOUT=0" | sudo tee -a /etc/default/grub

# Enable GRUB_DISABLE_RECOVERY to remove recovery mode entries
sudo sed -i 's/^#GRUB_DISABLE_RECOVERY=.*/GRUB_DISABLE_RECOVERY="true"/' /etc/default/grub

# Install Plymouth (splash screen manager)
sudo apt update
sudo apt install -y plymouth plymouth-themes

# Select Plymouth theme
sudo plymouth-set-default-theme -R spinner

# Configure CPU frequency governor for performance
echo "Configuring CPU governor for maximum performance..."
echo 'GOVERNOR="performance"' | sudo tee /etc/default/cpufrequtils

# Create CPU affinity script for LinuxCNC
cat << 'EOF' | sudo tee /usr/local/bin/linuxcnc-cpu-setup.sh
#!/bin/bash
# LinuxCNC CPU affinity setup for dual-processor systems

# Detect CPU count
CPU_CORES=$(nproc)

# Calculate default IRQ affinity
if [ "$CPU_CORES" -ge 4 ]; then
    DEFAULT_IRQ_AFFINITY="0-1"
else
    DEFAULT_IRQ_AFFINITY="0"
fi

echo "============================================"
echo "LinuxCNC CPU Affinity Setup"
echo "============================================"
echo "Total CPU cores: $CPU_CORES"
echo "Default IRQ affinity: CPUs $DEFAULT_IRQ_AFFINITY"
echo ""

# Set CPU governor to performance mode
echo "Setting CPU governor to performance mode..."
for cpu in /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor; do
    [ -f "$cpu" ] && echo performance | sudo tee "$cpu" > /dev/null 2>&1
done

# Disable CPU frequency scaling
echo "Configuring CPU frequency scaling..."
for cpu in /sys/devices/system/cpu/cpu*/cpufreq/scaling_max_freq; do
    if [ -f "$cpu" ]; then
        max_freq=$(cat "$cpu")
        echo $max_freq | sudo tee ${cpu/scaling_max_freq/scaling_min_freq} > /dev/null 2>&1
    fi
done

# Set default IRQ affinity
echo "Setting default IRQ affinity to CPUs $DEFAULT_IRQ_AFFINITY..."
echo $DEFAULT_IRQ_AFFINITY | sudo tee /proc/irq/default_smp_affinity > /dev/null

echo ""
echo "Default CPU setup completed."
echo ""

# Interactive IRQ management
read -p "Do you want to configure individual IRQ affinities? (y/n): " configure_irq

if [[ "$configure_irq" =~ ^[Yy]$ ]]; then
    echo ""
    echo "============================================"
    echo "Current Interrupt Assignments"
    echo "============================================"
    cat /proc/interrupts
    echo ""
    echo "============================================"
    echo "Available IRQs for configuration:"
    echo "============================================"

    # List all IRQ directories
    for irq_dir in /proc/irq/[0-9]*; do
        irq=$(basename "$irq_dir")
        if [ -f "$irq_dir/smp_affinity" ]; then
            current_affinity=$(cat "$irq_dir/smp_affinity" 2>/dev/null)
            irq_name=$(grep "^ *$irq:" /proc/interrupts | awk -F: '{print $2}' | sed 's/^[^a-zA-Z]*//' | awk '{print $NF}')
            [ -z "$irq_name" ] && irq_name="(no name)"
            printf "IRQ %3s: %s (current affinity: 0x%s)\n" "$irq" "$irq_name" "$current_affinity"
        fi
    done

    echo ""
    echo "============================================"
    echo "IRQ Affinity Configuration"
    echo "============================================"
    echo "You can now configure individual IRQ affinities."
    echo "Enter IRQ number and CPU affinity (e.g., '16 0-1' or '16 0,2')"
    echo "Type 'done' when finished, 'list' to show interrupts again"
    echo "Type 'help' for affinity format examples"
    echo ""

    while true; do
        read -p "Configure IRQ (irq_number cpu_list) or 'done': " input

        if [ "$input" = "done" ]; then
            break
        elif [ "$input" = "list" ]; then
            echo ""
            cat /proc/interrupts
            echo ""
            continue
        elif [ "$input" = "help" ]; then
            echo ""
            echo "CPU affinity format examples:"
            echo "  0      - CPU 0 only"
            echo "  0-1    - CPUs 0 and 1"
            echo "  0,2    - CPUs 0 and 2"
            echo "  0-1,3  - CPUs 0, 1, and 3"
            echo ""
            echo "You'll need to convert to hex bitmask:"
            echo "  CPU 0:     1 (0x1)"
            echo "  CPU 1:     2 (0x2)"
            echo "  CPU 0-1:   3 (0x3)"
            echo "  CPU 2-3:   c (0xc)"
            echo "  CPU 0-3:   f (0xf)"
            echo ""
            continue
        fi

        irq_num=$(echo "$input" | awk '{print $1}')
        cpu_affinity=$(echo "$input" | awk '{print $2}')

        if [ -z "$irq_num" ] || [ -z "$cpu_affinity" ]; then
            echo "Invalid input. Usage: IRQ_NUMBER CPU_LIST (e.g., '16 0-1')"
            continue
        fi

        # Convert CPU list to bitmask (simplified)
        # This is a helper - user can also enter hex directly
        if [[ "$cpu_affinity" =~ ^[0-9a-fA-F]+$ ]] && [ ${#cpu_affinity} -le 8 ]; then
            # Looks like hex already
            bitmask="$cpu_affinity"
        else
            # Try to convert CPU list to hex bitmask
            echo "Enter the hex bitmask for CPUs $cpu_affinity (or press Enter to calculate):"
            read -p "Bitmask (hex): " user_bitmask

            if [ -n "$user_bitmask" ]; then
                bitmask="$user_bitmask"
            else
                # Simple conversion for common cases
                case "$cpu_affinity" in
                    "0") bitmask="1" ;;
                    "1") bitmask="2" ;;
                    "0-1"|"0,1") bitmask="3" ;;
                    "2") bitmask="4" ;;
                    "3") bitmask="8" ;;
                    "2-3"|"2,3") bitmask="c" ;;
                    "0-3") bitmask="f" ;;
                    *)
                        echo "Cannot auto-convert. Please enter hex bitmask manually."
                        read -p "Bitmask (hex): " bitmask
                        ;;
                esac
            fi
        fi

        # Apply the affinity
        if [ -f "/proc/irq/$irq_num/smp_affinity" ]; then
            echo "$bitmask" | sudo tee "/proc/irq/$irq_num/smp_affinity" > /dev/null 2>&1
            if [ $? -eq 0 ]; then
                new_affinity=$(cat "/proc/irq/$irq_num/smp_affinity" 2>/dev/null)
                echo "✓ IRQ $irq_num affinity set to 0x$new_affinity"
            else
                echo "✗ Failed to set affinity for IRQ $irq_num"
            fi
        else
            echo "✗ IRQ $irq_num not found or cannot be configured"
        fi
        echo ""
    done

    echo ""
    echo "IRQ configuration completed."
fi

echo ""
echo "============================================"
echo "Final IRQ Affinity Summary"
echo "============================================"
for irq_dir in /proc/irq/[0-9]*; do
    irq=$(basename "$irq_dir")
    if [ -f "$irq_dir/smp_affinity" ]; then
        affinity=$(cat "$irq_dir/smp_affinity" 2>/dev/null)
        irq_name=$(grep "^ *$irq:" /proc/interrupts | awk -F: '{print $2}' | sed 's/^[^a-zA-Z]*//' | awk '{print $NF}')
        [ -z "$irq_name" ] && irq_name="(no name)"
        printf "IRQ %3s: %-30s -> CPUs 0x%s\n" "$irq" "$irq_name" "$affinity"
    fi
done

echo ""
echo "CPU setup completed for LinuxCNC configuration"
EOF

sudo chmod +x /usr/local/bin/linuxcnc-cpu-setup.sh

# Update initramfs to apply the changes
sudo update-initramfs -u

# Update GRUB
sudo update-grub

# Install and configure LightDM
sudo apt install lightdm -y

# Enable LightDM for graphical login
sudo systemctl enable lightdm

# Configure LightDM for autologin
sudo sed -i '/^\\[Seat:\\*\\]/a autologin-user='"$(whoami)"'\\nautologin-user-timeout=0' /etc/lightdm/lightdm.conf

# Create systemd service to run CPU setup on boot
cat << EOF | sudo tee /etc/systemd/system/linuxcnc-cpu-setup.service
[Unit]
Description=LinuxCNC CPU Setup for Dual-Processor Systems
After=multi-user.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/linuxcnc-cpu-setup.sh
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl enable linuxcnc-cpu-setup.service

# Display configuration summary
echo ""
echo "============================================"
echo "GRUB Configuration Summary:"
echo "============================================"
echo "Total CPU cores detected: $CPU_CORES"
echo "Isolated CPUs for real-time: $ISOLCPUS"
echo "System CPUs: $IRQ_AFFINITY"
echo "Additional optimizations applied for dual-processor systems"
echo ""
echo "GRUB and system updated successfully."
echo "Please reboot your system to apply all changes."
echo ""
echo "After reboot, run: sudo systemctl status linuxcnc-cpu-setup.service"
echo "to verify CPU optimizations are active."
