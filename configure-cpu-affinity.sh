#!/bin/bash
# LinuxCNC CPU affinity configuration script
# This script creates the CPU setup script, configures IRQ affinities,
# and creates a systemd service to restore settings on boot

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

# Configure CPU frequency governor for performance
echo "Configuring CPU governor for maximum performance..."
echo 'GOVERNOR="performance"' | sudo tee /etc/default/cpufrequtils > /dev/null

# Set CPU governor to performance mode immediately
for cpu in /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor; do
    [ -f "$cpu" ] && echo performance | sudo tee "$cpu" > /dev/null 2>&1
done
echo "✓ CPU governor set to performance mode"

# Disable CPU frequency scaling
echo "Disabling CPU frequency scaling..."
for cpu in /sys/devices/system/cpu/cpu*/cpufreq/scaling_max_freq; do
    if [ -f "$cpu" ]; then
        max_freq=$(cat "$cpu")
        echo $max_freq | sudo tee ${cpu/scaling_max_freq/scaling_min_freq} > /dev/null 2>&1
    fi
done
echo "✓ CPU frequency scaling disabled"
echo ""



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

# Save current IRQ configuration to a file for restoration on boot
CONFIG_FILE="/etc/linuxcnc-irq-affinity.conf"
echo "Saving IRQ affinity configuration to $CONFIG_FILE..."
sudo rm -f "$CONFIG_FILE"
for irq_dir in /proc/irq/[0-9]*; do
    irq=$(basename "$irq_dir")
    if [ -f "$irq_dir/smp_affinity_list" ]; then
        affinity=$(cat "$irq_dir/smp_affinity_list" 2>/dev/null | tr -d '\n')
        if [ -n "$affinity" ]; then
            echo "$irq:$affinity" | sudo tee -a "$CONFIG_FILE" > /dev/null
        fi
    fi
done
echo "✓ Configuration saved"

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
    echo "You can now configure individual or multiple IRQ affinities."
    echo "Single IRQ: '16 0-1'"
    echo "Multiple IRQs: '12 23 45 56 - 0-1' (sets IRQs 12,23,45,56 to CPUs 0-1)"
    echo "Type 'done' when finished, 'list' to show interrupts again"
    echo "Type 'help' for affinity format examples"
    echo ""

    while true; do
        read -p "Configure IRQ(s) or 'done': " input

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
            echo "  Single IRQ:    16 0-1"
            echo "  Multiple IRQs: 12 23 45 - 0-1"
            echo "  Multiple IRQs: 10 11 12 - 0,2"
            echo ""
            echo "CPU patterns:"
            echo "  0      - CPU 0 only"
            echo "  0-1    - CPUs 0 and 1"
            echo "  0,2    - CPUs 0 and 2"
            echo "  0-1,3  - CPUs 0, 1, and 3"
            echo ""
            continue
        fi

        # Check if input contains dash separator for multiple IRQs
        if echo "$input" | grep -q " - "; then
            # Multiple IRQs format: "12 23 45 - 0-1"
            irq_list=$(echo "$input" | sed 's/ - .*//')
            cpu_affinity=$(echo "$input" | sed 's/.* - //')

            if [ -z "$irq_list" ] || [ -z "$cpu_affinity" ]; then
                echo "Invalid input. Usage: IRQ1 IRQ2 IRQ3 - CPU_LIST (e.g., '12 23 45 - 0-1')"
                continue
            fi

            # Process each IRQ in the list
            success_count=0
            fail_count=0
            for irq_num in $irq_list; do
                if [ -f "/proc/irq/$irq_num/smp_affinity_list" ]; then
                    echo "$cpu_affinity" | sudo tee "/proc/irq/$irq_num/smp_affinity_list" > /dev/null 2>&1
                    if [ $? -eq 0 ]; then
                        ((success_count++))
                        # Update config file
                        sudo sed -i "/^$irq_num:/d" "$CONFIG_FILE"
                        echo "$irq_num:$cpu_affinity" | sudo tee -a "$CONFIG_FILE" > /dev/null
                    else
                        echo "✗ Failed to set affinity for IRQ $irq_num"
                        ((fail_count++))
                    fi
                else
                    echo "✗ IRQ $irq_num not found"
                    ((fail_count++))
                fi
            done
            echo "✓ Set $success_count IRQs to CPUs $cpu_affinity ($fail_count failed)"
        else
            # Single IRQ format: "16 0-1"
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
                    # Update config file
                    sudo sed -i "/^$irq_num:/d" "$CONFIG_FILE"
                    echo "$irq_num:$new_affinity" | sudo tee -a "$CONFIG_FILE" > /dev/null
                else
                    echo "✗ Failed to set affinity for IRQ $irq_num (may be managed by hardware)"
                fi
            else
                echo "✗ IRQ $irq_num not found or cannot be configured"
            fi
        fi
        echo ""
    done

    echo ""
    echo "IRQ configuration completed."
    echo "Configuration saved to $CONFIG_FILE"
fi

echo ""
echo "============================================"
echo "Final IRQ Affinity Summary"
echo "============================================"
cat /proc/interrupts

# Create the boot-time restoration script
echo ""
echo "Creating boot-time CPU setup script..."
cat << 'EOFSCRIPT' | sudo tee /usr/local/bin/linuxcnc-cpu-setup.sh > /dev/null
#!/bin/bash
# LinuxCNC CPU affinity restoration script
# This script runs on boot to restore CPU and IRQ affinity settings

# Set CPU governor to performance mode
for cpu in /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor; do
    [ -f "$cpu" ] && echo performance | sudo tee "$cpu" > /dev/null 2>&1
done

# Disable CPU frequency scaling
for cpu in /sys/devices/system/cpu/cpu*/cpufreq/scaling_max_freq; do
    if [ -f "$cpu" ]; then
        max_freq=$(cat "$cpu")
        echo $max_freq | sudo tee ${cpu/scaling_max_freq/scaling_min_freq} > /dev/null 2>&1
    fi
done

# Restore IRQ affinities from config file
CONFIG_FILE="/etc/linuxcnc-irq-affinity.conf"
if [ -f "$CONFIG_FILE" ]; then
    while IFS=: read -r irq affinity; do
        if [ -n "$irq" ] && [ -n "$affinity" ] && [ -f "/proc/irq/$irq/smp_affinity_list" ]; then
            echo "$affinity" | sudo tee "/proc/irq/$irq/smp_affinity_list" > /dev/null 2>&1
        fi
    done < "$CONFIG_FILE"
fi
EOFSCRIPT

sudo chmod +x /usr/local/bin/linuxcnc-cpu-setup.sh
echo "✓ Boot script created at /usr/local/bin/linuxcnc-cpu-setup.sh"

# Create systemd service to run CPU setup on boot
echo "Creating systemd service..."
cat << 'EOFSERVICE' | sudo tee /etc/systemd/system/linuxcnc-cpu-setup.service > /dev/null
[Unit]
Description=LinuxCNC CPU and IRQ Affinity Setup
After=multi-user.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/linuxcnc-cpu-setup.sh
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOFSERVICE

sudo systemctl daemon-reload
sudo systemctl enable linuxcnc-cpu-setup.service
echo "✓ Systemd service enabled"

echo ""
echo "============================================"
echo "CPU Affinity Setup Complete"
echo "============================================"
echo "Configuration saved to: $CONFIG_FILE"
echo "Boot script created: /usr/local/bin/linuxcnc-cpu-setup.sh"
echo "Service enabled: linuxcnc-cpu-setup.service"
echo ""
echo "Settings will be automatically restored on every boot."
echo "Reboot to apply GRUB changes."
echo ""
