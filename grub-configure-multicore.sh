#!/bin/bash

# Backup the existing grub configuration
sudo cp /etc/default/grub "/etc/default/grub.backup.$(date +%Y%m%d%H%M%S)"

echo "Updating GRUB configuration for LinuxCNC..."

# Detect number of CPU cores
CPU_CORES=$(nproc)
echo "Detected $CPU_CORES CPU cores"

# Calculate CPU isolation strategy for dual-processor systems
if [ "$CPU_CORES" -ge 8 ]; then
    ISOLCPUS="2-$((CPU_CORES-1))"
    NOHZ_FULL="2-$((CPU_CORES-1))"
    RCU_NOCBS="2-$((CPU_CORES-1))"
    IRQ_AFFINITY="0-1"
    KTHREAD_CPUS="0-1"
elif [ "$CPU_CORES" -ge 6 ]; then
    ISOLCPUS="2-$((CPU_CORES-1))"
    NOHZ_FULL="2-$((CPU_CORES-1))"
    RCU_NOCBS="2-$((CPU_CORES-1))"
    IRQ_AFFINITY="0-1"
    KTHREAD_CPUS="0-1"
elif [ "$CPU_CORES" -ge 4 ]; then
    ISOLCPUS="2,3"
    NOHZ_FULL="2,3"
    RCU_NOCBS="2,3"
    IRQ_AFFINITY="0-1"
    KTHREAD_CPUS="0-1"
else
    ISOLCPUS="1-$((CPU_CORES-1))"
    NOHZ_FULL="1-$((CPU_CORES-1))"
    RCU_NOCBS="1-$((CPU_CORES-1))"
    IRQ_AFFINITY="0"
    KTHREAD_CPUS="0"
fi

# Define the new GRUB parameters with dynamic CPU isolation
NEW_PARAMS="GRUB_CMDLINE_LINUX_DEFAULT=\"quiet isolcpus=$ISOLCPUS nohz_full=$NOHZ_FULL rcu_nocbs=$RCU_NOCBS irqaffinity=$IRQ_AFFINITY kthread_cpus=$KTHREAD_CPUS processor.max_cstate=1 intel_idle.max_cstate=0 idle=poll mitigations=off nosoftlockup tsc=reliable clocksource=tsc nmi_watchdog=0 rcu_nocb_poll pci=nommconf isolcpus_reboot=1 splash\""

# Use sed to replace the existing GRUB_CMDLINE_LINUX_DEFAULT line
sudo sed -i 's/^GRUB_CMDLINE_LINUX_DEFAULT=.*/'"$NEW_PARAMS"'/g' /etc/default/grub

# Disable GRUB menu (set timeout to 0)
sudo sed -i 's/^GRUB_TIMEOUT=.*/GRUB_TIMEOUT=0/' /etc/default/grub
sudo sed -i 's/^GRUB_TIMEOUT_STYLE=.*/GRUB_TIMEOUT_STYLE=hidden/' /etc/default/grub

# Set GRUB recordfail timeout to 0 to prevent boot delays
echo "GRUB_RECORDFAIL_TIMEOUT=0" | sudo tee -a /etc/default/grub > /dev/null 2>&1

# Enable GRUB_DISABLE_RECOVERY to remove recovery mode entries
sudo sed -i 's/^#GRUB_DISABLE_RECOVERY=.*/GRUB_DISABLE_RECOVERY="true"/' /etc/default/grub

# Install Plymouth (splash screen manager)
echo "Installing Plymouth splash screen..."
sudo apt update
sudo apt install -y plymouth plymouth-themes

# Select Plymouth theme
sudo plymouth-set-default-theme -R spinner

# Configure CPU frequency governor for performance
echo "Configuring CPU governor..."
echo 'GOVERNOR="performance"' | sudo tee /etc/default/cpufrequtils

# Create CPU affinity script for LinuxCNC
echo "Creating CPU affinity setup script..."
cat << 'EOF' | sudo tee /usr/local/bin/linuxcnc-cpu-setup.sh
#!/bin/bash
# LinuxCNC CPU affinity setup for dual-processor systems

# Detect CPU count
CPU_CORES=$(nproc)

# Calculate default IRQ affinity (hex bitmask)
if [ "$CPU_CORES" -ge 4 ]; then
    DEFAULT_IRQ_AFFINITY_CPUS="0-1"
    DEFAULT_IRQ_AFFINITY_MASK="3"  # CPUs 0-1 = binary 0011 = hex 0x3
else
    DEFAULT_IRQ_AFFINITY_CPUS="0"
    DEFAULT_IRQ_AFFINITY_MASK="1"  # CPU 0 = binary 0001 = hex 0x1
fi

echo "============================================"
echo "LinuxCNC CPU Affinity Setup"
echo "============================================"
echo "Total CPU cores: $CPU_CORES"
echo "Default IRQ affinity: CPUs $DEFAULT_IRQ_AFFINITY_CPUS (mask: 0x$DEFAULT_IRQ_AFFINITY_MASK)"
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
echo "Setting default IRQ affinity to CPUs $DEFAULT_IRQ_AFFINITY_CPUS..."
if [ -f /proc/irq/default_smp_affinity ]; then
    echo "$DEFAULT_IRQ_AFFINITY_MASK" | sudo tee /proc/irq/default_smp_affinity > /dev/null 2>&1
    echo "✓ Default IRQ affinity set to CPUs $DEFAULT_IRQ_AFFINITY_CPUS"
else
    echo "⚠ /proc/irq/default_smp_affinity not found (may need to run on target system)"
fi

# Apply affinity to all existing IRQs using smp_affinity_list
echo "Applying affinity to all existing IRQs..."
irq_count=0
for irq_dir in /proc/irq/[0-9]*; do
    irq=$(basename "$irq_dir")
    if [ -f "$irq_dir/smp_affinity_list" ]; then
        echo "$DEFAULT_IRQ_AFFINITY_CPUS" | sudo tee "$irq_dir/smp_affinity_list" > /dev/null 2>&1
        if [ $? -eq 0 ]; then
            ((irq_count++))
        fi
    fi
done
echo "✓ Applied affinity to $irq_count IRQs"

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
            continue
        fi

        irq_num=$(echo "$input" | awk '{print $1}')
        cpu_affinity=$(echo "$input" | awk '{print $2}')

        if [ -z "$irq_num" ] || [ -z "$cpu_affinity" ]; then
            echo "Invalid input. Usage: IRQ_NUMBER CPU_LIST (e.g., '16 0-1')"
            continue
        fi

        # Apply the affinity using smp_affinity_list (accepts CPU list format directly)
        if [ -f "/proc/irq/$irq_num/smp_affinity_list" ]; then
            echo "$cpu_affinity" | sudo tee "/proc/irq/$irq_num/smp_affinity_list" > /dev/null 2>&1
            if [ $? -eq 0 ]; then
                new_affinity=$(cat "/proc/irq/$irq_num/smp_affinity_list" 2>/dev/null)
                echo "✓ IRQ $irq_num affinity set to CPUs $new_affinity"
            else
                echo "✗ Failed to set affinity for IRQ $irq_num (may be managed by hardware)"
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
    if [ -f "$irq_dir/smp_affinity_list" ]; then
        affinity=$(cat "$irq_dir/smp_affinity_list" 2>/dev/null | tr -d '\n')
        # Get IRQ name from /proc/interrupts
        irq_info=$(grep "^ *$irq:" /proc/interrupts 2>/dev/null)
        if [ -n "$irq_info" ]; then
            irq_name=$(echo "$irq_info" | sed 's/^[^:]*://' | awk '{for(i=NF;i>=1;i--) if($i !~ /^[0-9]+$/) {print $i; exit}}')
        else
            irq_name="(no name)"
        fi
        [ -z "$irq_name" ] && irq_name="(no name)"
        printf "IRQ %3s: %-30s -> CPUs %s\n" "$irq" "$irq_name" "$affinity"
    fi
done

echo ""
echo "CPU setup completed for LinuxCNC configuration"
EOF

sudo chmod +x /usr/local/bin/linuxcnc-cpu-setup.sh

# Update initramfs to apply the changes
echo "Updating initramfs..."
sudo update-initramfs -u

# Update GRUB
echo "Updating GRUB..."
sudo update-grub

# Install and configure LightDM
echo "Installing LightDM..."
sudo apt install lightdm -y

# Enable LightDM for graphical login
sudo systemctl enable lightdm

# Configure LightDM for autologin
echo "Configuring LightDM autologin..."
sudo sed -i '/^\\[Seat:\\*\\]/a autologin-user='"$(whoami)"'\\nautologin-user-timeout=0' /etc/lightdm/lightdm.conf

# Create systemd service to run CPU setup on boot
echo "Creating systemd service..."
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

echo ""
echo "============================================"
echo "Configuration Summary"
echo "============================================"
echo "CPU cores: $CPU_CORES"
echo "Isolated CPUs: $ISOLCPUS"
echo "System CPUs: $IRQ_AFFINITY"
echo ""
echo "GRUB configuration completed successfully."
echo "Please reboot to apply changes."
echo ""
