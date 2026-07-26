package ui

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// IRQDeviceSelectorKind identifies a selector that can be resolved again
// after IRQ numbers change at the next boot.
type IRQDeviceSelectorKind string

const (
	// IRQDeviceSelectorPCIBDF matches the PCI bus address embedded in an IRQ
	// chip name, such as 0000:0a:00.0.
	IRQDeviceSelectorPCIBDF IRQDeviceSelectorKind = "pci_bdf"
	// IRQDeviceSelectorAction matches one exact /sys/kernel/irq/N/actions
	// value. It is only safe when that value identifies one non-PCI IRQ.
	IRQDeviceSelectorAction IRQDeviceSelectorKind = "action"
)

// IRQDeviceSelector is the persistent identity for one device group.
type IRQDeviceSelector struct {
	Kind  IRQDeviceSelectorKind
	Value string
}

// IRQCPUCount is one counter column from /proc/interrupts. CPU is -1 for a
// pseudo-interrupt that exposes a single global counter rather than per-CPU
// columns.
type IRQCPUCount struct {
	CPU   int
	Count uint64
}

// IRQInterruptRow preserves one real row from /proc/interrupts.
type IRQInterruptRow struct {
	ID                string
	Number            int
	Numeric           bool
	PerCPU            []IRQCPUCount
	Total             uint64
	Controller        string
	Description       string
	Raw               string
	CompleteCPUCounts bool
}

// IRQInterruptTable is a parsed, read-only representation of
// /proc/interrupts. Rows retain their source order.
type IRQInterruptTable struct {
	CPUs []int
	Rows []IRQInterruptRow
}

// IRQDeviceIRQ combines a numeric /proc/interrupts vector with the affinity
// state already collected in IRQSnapshot.
type IRQDeviceIRQ struct {
	Number               int
	Action               string
	ChipName             string
	PCIBDF               string
	Description          string
	PerCPU               []IRQCPUCount
	Total                uint64
	RequestedAffinity    string
	RequestedCPUs        []int
	EffectiveAffinity    string
	EffectiveCPUs        []int
	AffinityReadable     bool
	EffectiveReadable    bool
	AffinityFileWritable bool
}

// IRQDeviceGroup is one device-oriented view of one or more numeric IRQ
// vectors. Persistable means the selector can safely be resolved after a
// reboot. Editable additionally requires at least one writable affinity file.
type IRQDeviceGroup struct {
	ID                   string
	Label                string
	Selector             IRQDeviceSelector
	Persistable          bool
	PersistabilityReason string
	Editable             bool
	ReadOnlyReason       string
	Actions              []string
	IRQs                 []IRQDeviceIRQ
	PerCPU               []IRQCPUCount
	Total                uint64
}

// IRQDeviceInventory contains editable numeric device groups and read-only
// pseudo-interrupt rows such as NMI and LOC.
type IRQDeviceInventory struct {
	CPUs     []int
	Devices  []IRQDeviceGroup
	Pseudo   []IRQInterruptRow
	Problems []string
}

// IRQDeviceProbeOptions makes filesystem access replaceable by tests.
type IRQDeviceProbeOptions struct {
	Paths IRQProbePaths
}

var (
	interruptCPUHeaderPattern = regexp.MustCompile(`^CPU([0-9]+)$`)
	interruptPCIBDFPattern    = regexp.MustCompile(`(?i)([0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7])`)
	nvmeQueueActionPattern    = regexp.MustCompile(`^(nvme[0-9]+)q[0-9]+$`)
	queueActionPattern        = regexp.MustCompile(`(?i)^(.+?)(?:[-_](?:txrx|rx|tx)[-_]?[0-9]+)$`)
)

// ProbeIRQDeviceInventory reads the live /proc/interrupts table and groups its
// numeric vectors using the affinities in snapshot.
func ProbeIRQDeviceInventory(snapshot IRQSnapshot) (IRQDeviceInventory, error) {
	return ProbeIRQDeviceInventoryWithOptions(snapshot, IRQDeviceProbeOptions{
		Paths: DefaultIRQProbePaths(),
	})
}

// ProbeIRQDeviceInventoryWithOptions reads a fixture-injectable interrupts
// table and groups numeric vectors by stable PCI BDF or exact action identity.
func ProbeIRQDeviceInventoryWithOptions(
	snapshot IRQSnapshot,
	options IRQDeviceProbeOptions,
) (IRQDeviceInventory, error) {
	paths := options.Paths
	if paths.ProcRoot == "" {
		paths = DefaultIRQProbePaths()
	}

	interruptsPath := filepath.Join(paths.ProcRoot, "interrupts")
	data, err := os.ReadFile(interruptsPath)
	if err != nil {
		return IRQDeviceInventory{}, fmt.Errorf("read %s: %w", interruptsPath, err)
	}
	table, err := ParseProcInterrupts(data)
	if err != nil {
		return IRQDeviceInventory{}, fmt.Errorf("parse %s: %w", interruptsPath, err)
	}
	return groupIRQDevices(table, snapshot, paths), nil
}

// ParseProcInterrupts parses the real column layout of /proc/interrupts,
// including per-CPU counters and non-numeric pseudo-interrupt rows.
func ParseProcInterrupts(data []byte) (IRQInterruptTable, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)

	var table IRQInterruptTable
	headerFound := false
	for scanner.Scan() {
		raw := scanner.Text()
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if !headerFound {
			cpus, ok, err := parseInterruptCPUHeader(raw)
			if err != nil {
				return IRQInterruptTable{}, err
			}
			if !ok {
				return IRQInterruptTable{}, fmt.Errorf("first nonempty line is not a CPU header")
			}
			table.CPUs = cpus
			headerFound = true
			continue
		}

		row, ok, err := parseInterruptRow(raw, table.CPUs)
		if err != nil {
			return IRQInterruptTable{}, err
		}
		if ok {
			table.Rows = append(table.Rows, row)
		}
	}
	if err := scanner.Err(); err != nil {
		return IRQInterruptTable{}, err
	}
	if !headerFound || len(table.CPUs) == 0 {
		return IRQInterruptTable{}, fmt.Errorf("CPU header is missing")
	}
	return table, nil
}

func parseInterruptCPUHeader(line string) ([]int, bool, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil, false, nil
	}
	cpus := make([]int, 0, len(fields))
	for _, field := range fields {
		match := interruptCPUHeaderPattern.FindStringSubmatch(field)
		if match == nil {
			return nil, false, nil
		}
		cpu, err := strconv.Atoi(match[1])
		if err != nil {
			return nil, false, fmt.Errorf("invalid CPU header %q", field)
		}
		cpus = append(cpus, cpu)
	}
	if len(sortedUniqueNonNegative(cpus)) != len(cpus) {
		return nil, false, fmt.Errorf("CPU header contains duplicate columns")
	}
	return cpus, true, nil
}

func parseInterruptRow(line string, cpus []int) (IRQInterruptRow, bool, error) {
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return IRQInterruptRow{}, false, nil
	}
	id := strings.TrimSpace(line[:colon])
	if id == "" {
		return IRQInterruptRow{}, false, nil
	}

	row := IRQInterruptRow{
		ID:     id,
		Number: -1,
		Raw:    line,
	}
	if number, err := strconv.Atoi(id); err == nil && number >= 0 {
		row.Numeric = true
		row.Number = number
	}

	fields := strings.Fields(line[colon+1:])
	countValues := make([]uint64, 0, len(cpus))
	descriptionStart := 0
	for descriptionStart < len(fields) && descriptionStart < len(cpus) {
		count, err := strconv.ParseUint(fields[descriptionStart], 10, 64)
		if err != nil {
			break
		}
		countValues = append(countValues, count)
		descriptionStart++
	}
	row.CompleteCPUCounts = len(countValues) == len(cpus)
	if row.Numeric && !row.CompleteCPUCounts {
		return IRQInterruptRow{}, false, fmt.Errorf(
			"numeric IRQ %s has %d counter columns; want %d",
			id,
			len(countValues),
			len(cpus),
		)
	}

	if !row.Numeric && len(countValues) == 1 && len(cpus) > 1 {
		row.PerCPU = []IRQCPUCount{{CPU: -1, Count: countValues[0]}}
	} else {
		row.PerCPU = make([]IRQCPUCount, 0, len(countValues))
		for index, count := range countValues {
			row.PerCPU = append(row.PerCPU, IRQCPUCount{
				CPU:   cpus[index],
				Count: count,
			})
		}
	}
	for _, count := range row.PerCPU {
		var overflow bool
		row.Total, overflow = addIRQCount(row.Total, count.Count)
		if overflow {
			return IRQInterruptRow{}, false, fmt.Errorf("interrupt count overflow on row %s", id)
		}
	}

	descriptionFields := fields[descriptionStart:]
	row.Description = strings.Join(descriptionFields, " ")
	if len(descriptionFields) > 0 {
		row.Controller = descriptionFields[0]
	}
	return row, true, nil
}

type irqDeviceGroupBuilder struct {
	group            IRQDeviceGroup
	allActionsExact  bool
	hasSharedAction  bool
	affinityWritable bool
	firstIRQ         int
	perCPU           map[int]uint64
}

func groupIRQDevices(
	table IRQInterruptTable,
	snapshot IRQSnapshot,
	paths IRQProbePaths,
) IRQDeviceInventory {
	inventory := IRQDeviceInventory{
		CPUs: append([]int(nil), table.CPUs...),
	}
	entries := make(map[int]IRQEntry, len(snapshot.IRQs))
	for _, entry := range snapshot.IRQs {
		entries[entry.Number] = entry
	}

	builders := make(map[string]*irqDeviceGroupBuilder)
	order := make([]string, 0)
	for _, row := range table.Rows {
		if !row.Numeric {
			inventory.Pseudo = append(inventory.Pseudo, cloneInterruptRow(row))
			continue
		}

		entry, inSnapshot := entries[row.Number]
		if !inSnapshot {
			inventory.Problems = append(
				inventory.Problems,
				fmt.Sprintf("IRQ %d is present in /proc/interrupts but missing from the affinity snapshot", row.Number),
			)
		}
		metadata := readIRQDeviceMetadata(paths, row, entry)
		groupKey := metadata.groupKey(row.Number)
		builder, found := builders[groupKey]
		if !found {
			builder = &irqDeviceGroupBuilder{
				group: IRQDeviceGroup{
					ID:       groupKey,
					Selector: metadata.selector(),
				},
				allActionsExact: true,
				firstIRQ:        row.Number,
				perCPU:          make(map[int]uint64),
			}
			builders[groupKey] = builder
			order = append(order, groupKey)
		}
		if row.Number < builder.firstIRQ {
			builder.firstIRQ = row.Number
		}
		if !metadata.actionExact || metadata.action == "" {
			builder.allActionsExact = false
		}
		if metadata.sharedAction {
			builder.hasSharedAction = true
		}
		if entry.AffinityFileWritable {
			builder.affinityWritable = true
		}
		if metadata.action != "" && !containsString(builder.group.Actions, metadata.action) {
			builder.group.Actions = append(builder.group.Actions, metadata.action)
		}

		deviceIRQ := IRQDeviceIRQ{
			Number:               row.Number,
			Action:               metadata.action,
			ChipName:             metadata.chipName,
			PCIBDF:               metadata.pciBDF,
			Description:          row.Description,
			PerCPU:               cloneCPUCounts(row.PerCPU),
			Total:                row.Total,
			RequestedAffinity:    entry.RequestedAffinity,
			RequestedCPUs:        append([]int(nil), entry.RequestedCPUs...),
			EffectiveAffinity:    entry.EffectiveAffinity,
			EffectiveCPUs:        append([]int(nil), entry.EffectiveCPUs...),
			AffinityReadable:     entry.AffinityReadable,
			EffectiveReadable:    entry.EffectiveReadable,
			AffinityFileWritable: entry.AffinityFileWritable,
		}
		builder.group.IRQs = append(builder.group.IRQs, deviceIRQ)
		for _, cpuCount := range row.PerCPU {
			current := builder.perCPU[cpuCount.CPU]
			next, overflow := addIRQCount(current, cpuCount.Count)
			if overflow {
				next = math.MaxUint64
				inventory.Problems = append(
					inventory.Problems,
					fmt.Sprintf("device interrupt count overflow while adding IRQ %d", row.Number),
				)
			}
			builder.perCPU[cpuCount.CPU] = next
		}
		nextTotal, overflow := addIRQCount(builder.group.Total, row.Total)
		if overflow {
			nextTotal = math.MaxUint64
			inventory.Problems = append(
				inventory.Problems,
				fmt.Sprintf("device total interrupt count overflow while adding IRQ %d", row.Number),
			)
		}
		builder.group.Total = nextTotal
	}

	sort.SliceStable(order, func(left, right int) bool {
		return builders[order[left]].firstIRQ < builders[order[right]].firstIRQ
	})
	for _, key := range order {
		builder := builders[key]
		finalizeIRQDeviceGroup(builder, table.CPUs)
		inventory.Devices = append(inventory.Devices, builder.group)
	}
	return inventory
}

type irqDeviceMetadata struct {
	action       string
	actionExact  bool
	sharedAction bool
	chipName     string
	pciBDF       string
}

func readIRQDeviceMetadata(
	paths IRQProbePaths,
	row IRQInterruptRow,
	entry IRQEntry,
) irqDeviceMetadata {
	irqRoot := filepath.Join(paths.SysRoot, "kernel/irq", strconv.Itoa(row.Number))
	metadata := irqDeviceMetadata{}

	if data, err := os.ReadFile(filepath.Join(irqRoot, "actions")); err == nil {
		actions := splitIRQActions(string(data))
		if len(actions) == 1 {
			metadata.action = actions[0]
			metadata.actionExact = true
		} else if len(actions) > 1 {
			metadata.action = strings.Join(actions, ", ")
			metadata.sharedAction = true
		}
	}
	if metadata.action == "" {
		metadata.action = strings.TrimSpace(entry.Name)
	}

	if data, err := os.ReadFile(filepath.Join(irqRoot, "chip_name")); err == nil {
		metadata.chipName = strings.TrimSpace(string(data))
	}
	if metadata.chipName == "" {
		metadata.chipName = row.Controller
	}
	metadata.pciBDF = extractPCIBDF(metadata.chipName)
	if metadata.pciBDF == "" {
		metadata.pciBDF = extractPCIBDF(row.Description)
	}
	return metadata
}

func (metadata irqDeviceMetadata) groupKey(irq int) string {
	if metadata.pciBDF != "" {
		return "pci:" + metadata.pciBDF
	}
	if metadata.actionExact && !metadata.sharedAction {
		return "action:" + metadata.action
	}
	return "irq:" + strconv.Itoa(irq)
}

func (metadata irqDeviceMetadata) selector() IRQDeviceSelector {
	if metadata.pciBDF != "" {
		return IRQDeviceSelector{
			Kind:  IRQDeviceSelectorPCIBDF,
			Value: metadata.pciBDF,
		}
	}
	if metadata.actionExact && !metadata.sharedAction {
		return IRQDeviceSelector{
			Kind:  IRQDeviceSelectorAction,
			Value: metadata.action,
		}
	}
	return IRQDeviceSelector{}
}

func finalizeIRQDeviceGroup(builder *irqDeviceGroupBuilder, cpus []int) {
	group := &builder.group
	group.PerCPU = make([]IRQCPUCount, 0, len(cpus))
	for _, cpu := range cpus {
		group.PerCPU = append(group.PerCPU, IRQCPUCount{
			CPU:   cpu,
			Count: builder.perCPU[cpu],
		})
	}
	group.Label = irqDeviceLabel(*group)

	switch {
	case builder.hasSharedAction:
		group.PersistabilityReason = "an IRQ has multiple action handlers"
	case !builder.allActionsExact:
		group.PersistabilityReason = "no exact /sys/kernel/irq action label"
	case group.Selector.Kind == IRQDeviceSelectorPCIBDF:
		group.Persistable = true
	case group.Selector.Kind == IRQDeviceSelectorAction && len(group.IRQs) == 1:
		group.Persistable = true
	case group.Selector.Kind == IRQDeviceSelectorAction:
		group.PersistabilityReason = "the same action matches multiple IRQs without a PCI device identity"
	default:
		group.PersistabilityReason = "no stable PCI or exact-action identity"
	}

	if !group.Persistable {
		group.ReadOnlyReason = group.PersistabilityReason
		return
	}
	if !builder.affinityWritable {
		group.ReadOnlyReason = "no IRQ in this device exposes a writable smp_affinity_list"
		return
	}
	group.Editable = true
}

func irqDeviceLabel(group IRQDeviceGroup) string {
	action := irqActionFamily(group.Actions)
	if action == "" {
		action = "IRQ device"
	}
	if group.Selector.Kind == IRQDeviceSelectorPCIBDF {
		return fmt.Sprintf("%s [%s]", action, group.Selector.Value)
	}
	if len(group.IRQs) == 1 {
		return action
	}
	return fmt.Sprintf("%s (%d IRQs)", action, len(group.IRQs))
}

func irqActionFamily(actions []string) string {
	if len(actions) == 0 {
		return ""
	}
	stem := irqActionStem(actions[0])
	for _, action := range actions[1:] {
		if irqActionStem(action) != stem {
			if len(actions) <= 3 {
				return strings.Join(actions, ", ")
			}
			return fmt.Sprintf("%s + %d actions", actions[0], len(actions)-1)
		}
	}
	return stem
}

func irqActionStem(action string) string {
	action = strings.TrimSpace(action)
	if match := nvmeQueueActionPattern.FindStringSubmatch(action); match != nil {
		return match[1]
	}
	if match := queueActionPattern.FindStringSubmatch(action); match != nil {
		return strings.TrimRight(match[1], "-_")
	}
	return action
}

func splitIRQActions(value string) []string {
	var actions []string
	for _, action := range strings.Split(strings.TrimSpace(value), ",") {
		action = strings.TrimSpace(action)
		// Preserve duplicates here: two handlers with the same displayed name
		// still make a shared IRQ unsafe to persist as one device selector.
		if action != "" {
			actions = append(actions, action)
		}
	}
	return actions
}

func extractPCIBDF(value string) string {
	match := interruptPCIBDFPattern.FindStringSubmatch(value)
	if match == nil {
		return ""
	}
	return strings.ToLower(match[1])
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func cloneCPUCounts(values []IRQCPUCount) []IRQCPUCount {
	return append([]IRQCPUCount(nil), values...)
}

func cloneInterruptRow(row IRQInterruptRow) IRQInterruptRow {
	row.PerCPU = cloneCPUCounts(row.PerCPU)
	return row
}

func addIRQCount(left, right uint64) (uint64, bool) {
	if math.MaxUint64-left < right {
		return math.MaxUint64, true
	}
	return left + right, false
}
