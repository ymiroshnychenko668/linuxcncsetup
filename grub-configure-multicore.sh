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
cat << EOF | sudo tee /usr/local/bin/linuxcnc-cpu-setup.sh
#!/bin/bash
# LinuxCNC CPU affinity setup for dual-processor systems

# Set CPU governor to performance mode
for cpu in /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor; do
    [ -f "\$cpu" ] && echo performance | sudo tee "\$cpu" > /dev/null
done

# Disable CPU frequency scaling
for cpu in /sys/devices/system/cpu/cpu*/cpufreq/scaling_max_freq; do
    if [ -f "\$cpu" ]; then
        max_freq=\$(cat "\$cpu")
        echo \$max_freq | sudo tee \${cpu/scaling_max_freq/scaling_min_freq} > /dev/null
    fi
done
