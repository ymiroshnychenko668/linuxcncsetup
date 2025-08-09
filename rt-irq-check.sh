#!/bin/bash

# RT IRQ verification and tuning script for LinuxCNC
# This script checks and displays the current real-time interrupt configuration

echo "=== Real-Time IRQ Configuration Check ==="
echo ""

# Check if rtirq is installed and running
echo "1. RTIRQ Service Status:"
if systemctl is-active --quiet rtirq 2>/dev/null; then
    echo "✓ rtirq service is running"
    echo "  Status: $(systemctl is-active rtirq 2>/dev/null)"
    echo "  Enabled: $(systemctl is-enabled rtirq 2>/dev/null)"
else
    echo "✗ rtirq service is not running"
    echo "  Run ./rt-irq-install.sh to install and configure"
fi
echo ""

# Show rtirq configuration
echo "2. RTIRQ Configuration:"
if [ -f /etc/default/rtirq ]; then
    echo "✓ Configuration file exists"
    echo "  Key settings:"
    grep -E "^(RTIRQ_ENABLED|RTIRQ_NAME_LIST|RTIRQ_CPU_LIST)" /etc/default/rtirq 2>/dev/null | sed 's/^/  /'
else
    echo "✗ No rtirq configuration found"
fi
echo ""

# Check RT kernel
echo "3. Kernel Configuration:"
KERNEL=$(uname -r)
if echo "$KERNEL" | grep -q "rt"; then
    echo "✓ Real-time kernel detected: $KERNEL"
else
    echo "⚠ Non-RT kernel: $KERNEL"
    echo "  Consider installing linux-image-rt-amd64 for better RT performance"
fi

# Check CPU isolation
echo "  CPU isolation: $(grep -o 'isolcpus=[^ ]*' /proc/cmdline 2>/dev/null || echo 'None')"
echo "  NO_HZ full: $(grep -o 'nohz_full=[^ ]*' /proc/cmdline 2>/dev/null || echo 'None')"
echo ""

# Show current RT threads
echo "4. Real-Time Threads:"
RT_THREADS=$(ps -eo pid,tid,class,rtprio,comm | grep -E "(FF|RR)" 2>/dev/null)
if [ -n "$RT_THREADS" ]; then
    echo "✓ RT threads found:"
    echo "   PID   TID CLS RTPRIO COMMAND"
    echo "$RT_THREADS" | head -10 | sed 's/^/  /'
    
    RT_COUNT=$(echo "$RT_THREADS" | wc -l)
    if [ "$RT_COUNT" -gt 10 ]; then
        echo "  ... and $((RT_COUNT - 10)) more RT threads"
    fi
else
    echo "⚠ No RT threads found (this may be normal if system is idle)"
fi
echo ""

# Show interrupt distribution
echo "5. Interrupt Distribution (Top 15 active IRQs):"
echo "   IRQ    CPU0    CPU1    CPU2    CPU3  TYPE        DEVICE"
cat /proc/interrupts | head -1 | sed 's/^/  /'
cat /proc/interrupts | grep -E "^[ ]*[0-9]+:" | sort -k2,2nr | head -15 | sed 's/^/  /'
echo ""

# Check IRQ affinity for key interrupts
echo "6. IRQ CPU Affinity (should prefer cores 0-1):"
CRITICAL_IRQS=$(grep -E "(timer|rtc|parport)" /proc/interrupts | grep -o "^[ ]*[0-9]*" | tr -d ' ')
if [ -n "$CRITICAL_IRQS" ]; then
    for irq in $CRITICAL_IRQS; do
        if [ -r "/proc/irq/$irq/smp_affinity_list" ]; then
            affinity=$(cat "/proc/irq/$irq/smp_affinity_list" 2>/dev/null || echo "N/A")
            irq_name=$(grep "^ *$irq:" /proc/interrupts | awk '{print $NF}' | head -1)
            printf "  IRQ %3s: %s -> %s\n" "$irq" "$affinity" "$irq_name"
        fi
    done
else
    echo "  No critical IRQs found (timer, rtc, parport)"
fi
echo ""

# Check system latency indicators
echo "7. System Load and Performance:"
echo "  Load average: $(cat /proc/loadavg | awk '{print $1, $2, $3}')"
echo "  Context switches/sec: $(grep ctxt /proc/stat | awk '{print $2}')"
echo "  Interrupts/sec: $(grep intr /proc/stat | awk '{print $2}')"

# Check for potential latency sources
echo ""
echo "8. Potential Latency Sources:"
ISSUES=""

# Check for power management
if grep -q "intel_idle.max_cstate=0" /proc/cmdline; then
    echo "  ✓ CPU idle states disabled"
else
    echo "  ⚠ CPU idle states may be active (add intel_idle.max_cstate=0)"
    ISSUES="yes"
fi

# Check for SMI
if grep -q "nosoftlockup" /proc/cmdline; then
    echo "  ✓ Soft lockup detection disabled"
else
    echo "  ⚠ Soft lockup detection active (add nosoftlockup)"
    ISSUES="yes"
fi

# Check for mitigations
if grep -q "mitigations=off" /proc/cmdline; then
    echo "  ✓ CPU mitigations disabled"
else
    echo "  ⚠ CPU mitigations may be active (add mitigations=off)"
    ISSUES="yes"
fi

if [ -z "$ISSUES" ]; then
    echo "  ✓ No obvious latency sources detected"
fi

echo ""
echo "=== Recommendations ==="
echo ""
echo "To test real-time performance:"
echo "  cyclictest -m -p99 -t4 -i200 -d0 -q"
echo ""
echo "To monitor IRQ activity:"
echo "  watch -n 1 'cat /proc/interrupts | head -20'"
echo ""
echo "To check rtirq status:"
echo "  sudo rtirq status"
echo ""
echo "For LinuxCNC latency test:"
echo "  latency-histogram --nobase --sbindir /usr/bin"
echo ""
