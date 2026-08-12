package ui

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	managedIRQConfigPath = "/etc/linuxcncsetup/irq-affinity.yml"
	managedIRQHelperPath = "/usr/local/libexec/linuxcncsetup-irq-affinity"
	managedIRQService    = "linuxcncsetup-irq-affinity.service"
	managedIRQResultPath = "/run/linuxcncsetup/irq-affinity-result.json"
)

// CPUInfo describes one online logical CPU and its topology, where available.
// CoreID and PackageID are -1 when the kernel did not expose those values.
type CPUInfo struct {
	ID             int
	CoreID         int
	PackageID      int
	ThreadSiblings []int
}

// IRQEntry is the current affinity state for one numeric IRQ.
type IRQEntry struct {
	Number               int
	Name                 string
	Description          string
	RequestedAffinity    string
	RequestedCPUs        []int
	EffectiveAffinity    string
	EffectiveCPUs        []int
	AffinityReadable     bool
	EffectiveReadable    bool
	AffinityFileWritable bool
}

// IRQBalanceStatus records whether irqbalance exists and, when systemd can be
// queried, whether its unit is active and enabled.
type IRQBalanceStatus struct {
	Installed    bool
	Active       bool
	ActiveKnown  bool
	Enabled      bool
	EnabledKnown bool
}

// ManagedIRQConfig is the on-disk policy installed by linuxcncsetup.
type ManagedIRQConfig struct {
	SchemaVersion       int
	HousekeepingCPUList string
	ProtectedCPUList    string
	HousekeepingCPUs    []int
	ProtectedCPUs       []int
	DefaultPolicy       *ManagedIRQDefaultPolicy
	DeviceRules         []ManagedIRQDeviceRule
}

// ManagedIRQResultPolicy is the policy recorded in the latest boot result.
type ManagedIRQResultPolicy struct {
	HousekeepingCPUList string
	ProtectedCPUList    string
	HousekeepingCPUs    []int
	ProtectedCPUs       []int
	DefaultPolicy       *ManagedIRQDefaultPolicy
	DeviceRules         []ManagedIRQDeviceRule
}

// ManagedIRQResultCounts summarizes how the boot helper classified IRQs.
type ManagedIRQResultCounts struct {
	Applied             int `json:"applied"`
	Constrained         int `json:"constrained"`
	KernelManaged       int `json:"kernel_managed"`
	Unwritable          int `json:"unwritable"`
	NoAffinityInterface int `json:"no_affinity_interface"`
	Disappeared         int `json:"disappeared"`
	Failed              int `json:"failed"`
}

// ManagedIRQResultEntry is one IRQ result emitted by the boot helper.
type ManagedIRQResultEntry struct {
	IRQ       int    `json:"irq"`
	Status    string `json:"status"`
	Requested string `json:"requested"`
	Effective string `json:"effective"`
	Detail    string `json:"detail"`
}

// ManagedIRQDeviceRuleCounts summarizes stable device-selector outcomes.
type ManagedIRQDeviceRuleCounts struct {
	Configured        int `json:"configured"`
	Matched           int `json:"matched"`
	NoMatch           int `json:"no_match"`
	UnsafeSelector    int `json:"unsafe_selector"`
	AmbiguousSelector int `json:"ambiguous_selector"`
	Applied           int `json:"applied"`
	Partial           int `json:"partial"`
	Failed            int `json:"failed"`
}

// ManagedIRQDeviceRuleResult is one resolved device rule from a boot or live
// result. MatchedIRQs are diagnostic only and are never persisted as policy.
type ManagedIRQDeviceRuleResult struct {
	Selector      IRQDeviceSelector
	Label         string
	Requested     string
	RequestedCPUs []int
	Status        string
	Detail        string
	MatchedIRQs   []int
	Counts        ManagedIRQResultCounts
	IRQs          []ManagedIRQResultEntry
}

// ManagedIRQResult is the latest boot-time application result.
type ManagedIRQResult struct {
	SchemaVersion      int
	Operation          string
	GeneratedAt        string
	Status             string
	Message            string
	Policy             ManagedIRQResultPolicy
	OnlineCPUList      string
	OnlineCPUs         []int
	DefaultSMPAffinity string
	Counts             ManagedIRQResultCounts
	IRQs               []ManagedIRQResultEntry
	DeviceRuleCounts   ManagedIRQDeviceRuleCounts
	DeviceRules        []ManagedIRQDeviceRuleResult
}

// ManagedIRQComponentStatus records one managed file's expected path and
// whether that path currently exists.
type ManagedIRQComponentStatus struct {
	Path    string
	Present bool
}

// ManagedIRQPolicyStatus describes linuxcncsetup's persistent IRQ policy.
type ManagedIRQPolicyStatus struct {
	Config         ManagedIRQComponentStatus
	Helper         ManagedIRQComponentStatus
	Service        ManagedIRQComponentStatus
	Result         ManagedIRQComponentStatus
	ConfigPath     string
	ConfigPresent  bool
	ConfigData     *ManagedIRQConfig
	HelperPath     string
	HelperPresent  bool
	ServicePath    string
	ServicePresent bool
	Active         bool
	ActiveKnown    bool
	Enabled        bool
	EnabledKnown   bool
	ResultPath     string
	ResultPresent  bool
	ResultData     *ManagedIRQResult
}

// IRQSnapshot is a read-only view of the machine's CPU and IRQ configuration.
type IRQSnapshot struct {
	OnlineCPUs        []int
	CPUs              []CPUInfo
	KernelCommandLine string
	IsolatedCPUs      []int
	NoHZFullCPUs      []int
	KernelIRQAffinity []int
	DefaultAffinity   []int
	IRQs              []IRQEntry
	IRQBalance        IRQBalanceStatus
	ManagedPolicy     ManagedIRQPolicyStatus
	Problems          []string
}

// IRQProbePaths makes every filesystem source replaceable by tests.
type IRQProbePaths struct {
	ProcRoot  string
	SysRoot   string
	EtcRoot   string
	RunRoot   string
	USRRoot   string
	LibRoot   string
	VarRoot   string
	LocalRoot string
}

// IRQCommandRunner runs a read-only status command such as systemctl is-active.
type IRQCommandRunner func(name string, args ...string) ([]byte, error)

// IRQProbeOptions controls filesystem and command access for a probe.
type IRQProbeOptions struct {
	Paths         IRQProbePaths
	CommandRunner IRQCommandRunner
}

// DefaultIRQProbePaths returns paths for the live host.
func DefaultIRQProbePaths() IRQProbePaths {
	return IRQProbePaths{
		ProcRoot:  "/proc",
		SysRoot:   "/sys",
		EtcRoot:   "/etc",
		RunRoot:   "/run",
		USRRoot:   "/usr",
		LibRoot:   "/lib",
		VarRoot:   "/var",
		LocalRoot: "/usr/local",
	}
}

// IRQProbePathsForRoot returns a complete path set below a fixture root.
func IRQProbePathsForRoot(root string) IRQProbePaths {
	return IRQProbePaths{
		ProcRoot:  filepath.Join(root, "proc"),
		SysRoot:   filepath.Join(root, "sys"),
		EtcRoot:   filepath.Join(root, "etc"),
		RunRoot:   filepath.Join(root, "run"),
		USRRoot:   filepath.Join(root, "usr"),
		LibRoot:   filepath.Join(root, "lib"),
		VarRoot:   filepath.Join(root, "var"),
		LocalRoot: filepath.Join(root, "usr", "local"),
	}
}

// ProbeIRQSnapshot inspects the live host without changing it.
func ProbeIRQSnapshot() (IRQSnapshot, error) {
	return ProbeIRQSnapshotWithOptions(IRQProbeOptions{
		Paths: DefaultIRQProbePaths(),
		CommandRunner: func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).CombinedOutput()
		},
	})
}

// ProbeIRQSnapshotWithOptions inspects CPU and IRQ state through injected paths.
func ProbeIRQSnapshotWithOptions(options IRQProbeOptions) (IRQSnapshot, error) {
	paths := options.Paths
	if paths.ProcRoot == "" {
		paths = DefaultIRQProbePaths()
	}

	online, err := probeOnlineCPUs(paths)
	if err != nil {
		return IRQSnapshot{}, err
	}

	snapshot := IRQSnapshot{OnlineCPUs: online}
	snapshot.CPUs = probeCPUTopology(paths, online, &snapshot.Problems)
	probeKernelCommandLine(paths, &snapshot)
	probeDefaultAffinity(paths, &snapshot)
	snapshot.IRQs = probeIRQs(paths, len(online), &snapshot.Problems)
	snapshot.IRQBalance = probeIRQBalance(paths, options.CommandRunner)
	snapshot.ManagedPolicy = probeManagedIRQPolicy(
		paths,
		options.CommandRunner,
		online,
		&snapshot.Problems,
	)
	return snapshot, nil
}

// ParseCPUList parses the Linux CPU-list format, such as "0-3,6,8-9".
func ParseCPUList(value string) ([]int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return []int{}, nil
	}

	seen := make(map[int]struct{})
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("invalid empty CPU-list element in %q", value)
		}

		bounds := strings.Split(part, "-")
		if len(bounds) > 2 {
			return nil, fmt.Errorf("invalid CPU range %q", part)
		}
		first, err := parseCPUID(bounds[0])
		if err != nil {
			return nil, err
		}
		last := first
		if len(bounds) == 2 {
			last, err = parseCPUID(bounds[1])
			if err != nil {
				return nil, err
			}
			if last < first {
				return nil, fmt.Errorf("descending CPU range %q", part)
			}
		}
		if last-first > 1_048_576 {
			return nil, fmt.Errorf("CPU range %q is unreasonably large", part)
		}
		for cpu := first; cpu <= last; cpu++ {
			seen[cpu] = struct{}{}
		}
	}

	cpus := make([]int, 0, len(seen))
	for cpu := range seen {
		cpus = append(cpus, cpu)
	}
	sort.Ints(cpus)
	return cpus, nil
}

// FormatCPUList returns a sorted, deduplicated, canonical Linux CPU list.
func FormatCPUList(cpus []int) string {
	if len(cpus) == 0 {
		return ""
	}

	canonical := sortedUniqueNonNegative(cpus)
	var parts []string
	for startIndex := 0; startIndex < len(canonical); {
		endIndex := startIndex
		for endIndex+1 < len(canonical) && canonical[endIndex+1] == canonical[endIndex]+1 {
			endIndex++
		}
		if startIndex == endIndex {
			parts = append(parts, strconv.Itoa(canonical[startIndex]))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", canonical[startIndex], canonical[endIndex]))
		}
		startIndex = endIndex + 1
	}
	return strings.Join(parts, ",")
}

// ParseCPUMask converts a kernel hexadecimal CPU mask to logical CPU IDs.
func ParseCPUMask(value string) ([]int, error) {
	compact := strings.ReplaceAll(strings.TrimSpace(value), ",", "")
	compact = strings.TrimPrefix(strings.TrimPrefix(compact, "0x"), "0X")
	if compact == "" {
		return nil, errors.New("empty CPU mask")
	}

	var cpus []int
	for nibbleIndex, position := 0, len(compact)-1; position >= 0; nibbleIndex, position = nibbleIndex+1, position-1 {
		nibble, err := strconv.ParseUint(string(compact[position]), 16, 4)
		if err != nil {
			return nil, fmt.Errorf("invalid CPU mask %q: %w", value, err)
		}
		for bit := 0; bit < 4; bit++ {
			if nibble&(1<<bit) != 0 {
				cpus = append(cpus, nibbleIndex*4+bit)
			}
		}
	}
	return cpus, nil
}

// RecommendedProtectedCPUs prefers a usable existing isolation policy. With no
// usable policy it selects the highest online CPU, or the highest two on hosts
// with at least six online CPUs.
func RecommendedProtectedCPUs(online, isolated []int) []int {
	online = sortedUniqueNonNegative(online)
	if len(online) < 2 {
		return []int{}
	}

	existing := intersection(online, isolated)
	if len(existing) > 0 && len(existing) < len(online) {
		return existing
	}

	count := 1
	if len(online) >= 6 {
		count = 2
	}
	return append([]int(nil), online[len(online)-count:]...)
}

// HousekeepingCPUs returns the online CPUs that are not protected.
func HousekeepingCPUs(online, protected []int) []int {
	protectedSet := intSet(protected)
	var housekeeping []int
	for _, cpu := range sortedUniqueNonNegative(online) {
		if _, found := protectedSet[cpu]; !found {
			housekeeping = append(housekeeping, cpu)
		}
	}
	return housekeeping
}

// ValidateIRQPolicy checks that every online CPU has exactly one nonempty role.
func ValidateIRQPolicy(online, protected, housekeeping []int) error {
	online = sortedUniqueNonNegative(online)
	if len(online) == 0 {
		return errors.New("no online CPUs were detected")
	}
	if len(protected) == 0 {
		return errors.New("at least one protected CPU is required")
	}
	if len(housekeeping) == 0 {
		return errors.New("at least one housekeeping CPU is required")
	}

	onlineSet := intSet(online)
	protectedSet := make(map[int]struct{}, len(protected))
	for _, cpu := range protected {
		if cpu < 0 {
			return fmt.Errorf("protected CPU %d is invalid", cpu)
		}
		if _, found := onlineSet[cpu]; !found {
			return fmt.Errorf("protected CPU %d is not online", cpu)
		}
		if _, duplicate := protectedSet[cpu]; duplicate {
			return fmt.Errorf("protected CPU %d is selected more than once", cpu)
		}
		protectedSet[cpu] = struct{}{}
	}

	housekeepingSet := make(map[int]struct{}, len(housekeeping))
	for _, cpu := range housekeeping {
		if cpu < 0 {
			return fmt.Errorf("housekeeping CPU %d is invalid", cpu)
		}
		if _, found := onlineSet[cpu]; !found {
			return fmt.Errorf("housekeeping CPU %d is not online", cpu)
		}
		if _, duplicate := housekeepingSet[cpu]; duplicate {
			return fmt.Errorf("housekeeping CPU %d is selected more than once", cpu)
		}
		if _, protected := protectedSet[cpu]; protected {
			return fmt.Errorf("CPU %d cannot be both protected and housekeeping", cpu)
		}
		housekeepingSet[cpu] = struct{}{}
	}

	for _, cpu := range online {
		if _, protected := protectedSet[cpu]; protected {
			continue
		}
		if _, housekeeping := housekeepingSet[cpu]; !housekeeping {
			return fmt.Errorf("online CPU %d has no policy role", cpu)
		}
	}
	return nil
}

func parseCPUID(value string) (int, error) {
	value = strings.TrimSpace(value)
	cpu, err := strconv.Atoi(value)
	if err != nil || cpu < 0 {
		return 0, fmt.Errorf("invalid CPU ID %q", value)
	}
	if cpu > 1_048_576 {
		return 0, fmt.Errorf("CPU ID %d is unreasonably large", cpu)
	}
	return cpu, nil
}

func sortedUniqueNonNegative(values []int) []int {
	set := make(map[int]struct{}, len(values))
	for _, value := range values {
		if value >= 0 {
			set[value] = struct{}{}
		}
	}
	result := make([]int, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Ints(result)
	return result
}

func intSet(values []int) map[int]struct{} {
	result := make(map[int]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func intersection(left, right []int) []int {
	rightSet := intSet(right)
	var result []int
	for _, value := range sortedUniqueNonNegative(left) {
		if _, found := rightSet[value]; found {
			result = append(result, value)
		}
	}
	return result
}

func probeOnlineCPUs(paths IRQProbePaths) ([]int, error) {
	onlinePath := filepath.Join(paths.SysRoot, "devices/system/cpu/online")
	data, err := os.ReadFile(onlinePath)
	if err == nil {
		cpus, parseErr := ParseCPUList(string(data))
		if parseErr != nil {
			return nil, fmt.Errorf("parse %s: %w", onlinePath, parseErr)
		}
		if len(cpus) == 0 {
			return nil, fmt.Errorf("%s contains no CPUs", onlinePath)
		}
		return cpus, nil
	}

	cpuRoot := filepath.Join(paths.SysRoot, "devices/system/cpu")
	entries, directoryErr := os.ReadDir(cpuRoot)
	if directoryErr != nil {
		return nil, fmt.Errorf("read online CPUs: %w", err)
	}
	var cpus []int
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "cpu") {
			continue
		}
		cpu, parseErr := strconv.Atoi(strings.TrimPrefix(entry.Name(), "cpu"))
		if parseErr != nil {
			continue
		}
		onlineData, onlineErr := os.ReadFile(filepath.Join(cpuRoot, entry.Name(), "online"))
		if onlineErr == nil && strings.TrimSpace(string(onlineData)) == "0" {
			continue
		}
		cpus = append(cpus, cpu)
	}
	cpus = sortedUniqueNonNegative(cpus)
	if len(cpus) == 0 {
		return nil, fmt.Errorf("read online CPUs: %w", err)
	}
	return cpus, nil
}

func probeCPUTopology(paths IRQProbePaths, online []int, problems *[]string) []CPUInfo {
	cpus := make([]CPUInfo, 0, len(online))
	for _, cpu := range online {
		topologyRoot := filepath.Join(paths.SysRoot, "devices/system/cpu", fmt.Sprintf("cpu%d", cpu), "topology")
		info := CPUInfo{
			ID:        cpu,
			CoreID:    readIntOr(topologyRoot, "core_id", -1),
			PackageID: readIntOr(topologyRoot, "physical_package_id", -1),
		}
		if data, err := os.ReadFile(filepath.Join(topologyRoot, "thread_siblings_list")); err == nil {
			siblings, parseErr := ParseCPUList(string(data))
			if parseErr != nil {
				*problems = append(*problems, fmt.Sprintf("parse CPU %d thread siblings: %v", cpu, parseErr))
			} else {
				info.ThreadSiblings = intersection(online, siblings)
			}
		}
		cpus = append(cpus, info)
	}
	return cpus
}

func readIntOr(directory, name string, fallback int) int {
	data, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return fallback
	}
	return value
}

func probeKernelCommandLine(paths IRQProbePaths, snapshot *IRQSnapshot) {
	path := filepath.Join(paths.ProcRoot, "cmdline")
	data, err := os.ReadFile(path)
	if err != nil {
		snapshot.Problems = append(snapshot.Problems, fmt.Sprintf("read %s: %v", path, err))
		return
	}
	snapshot.KernelCommandLine = strings.TrimSpace(string(data))

	parameters := make(map[string]string)
	for _, field := range strings.Fields(snapshot.KernelCommandLine) {
		key, value, found := strings.Cut(field, "=")
		if found {
			parameters[key] = value
		}
	}

	for key, destination := range map[string]*[]int{
		"nohz_full":   &snapshot.NoHZFullCPUs,
		"irqaffinity": &snapshot.KernelIRQAffinity,
	} {
		value, found := parameters[key]
		if !found {
			continue
		}
		cpus, parseErr := ParseCPUList(value)
		if parseErr != nil {
			snapshot.Problems = append(snapshot.Problems, fmt.Sprintf("parse kernel %s=%s: %v", key, value, parseErr))
			continue
		}
		*destination = cpus
	}

	if value, found := parameters["isolcpus"]; found {
		cpus, parseErr := parseIsolCPUs(value)
		if parseErr != nil {
			snapshot.Problems = append(snapshot.Problems, fmt.Sprintf("parse kernel isolcpus=%s: %v", value, parseErr))
		} else {
			snapshot.IsolatedCPUs = cpus
		}
	}
}

func parseIsolCPUs(value string) ([]int, error) {
	parts := strings.Split(value, ",")
	start := 0
	for start < len(parts) {
		part := strings.TrimSpace(parts[start])
		if part != "domain" && part != "nohz" && part != "managed_irq" {
			break
		}
		start++
	}
	if start == len(parts) {
		return nil, fmt.Errorf("no CPU list in %q", value)
	}
	return ParseCPUList(strings.Join(parts[start:], ","))
}

func probeDefaultAffinity(paths IRQProbePaths, snapshot *IRQSnapshot) {
	path := filepath.Join(paths.ProcRoot, "irq/default_smp_affinity")
	data, err := os.ReadFile(path)
	if err != nil {
		snapshot.Problems = append(snapshot.Problems, fmt.Sprintf("read %s: %v", path, err))
		return
	}
	cpus, parseErr := ParseCPUMask(string(data))
	if parseErr != nil {
		snapshot.Problems = append(snapshot.Problems, fmt.Sprintf("parse %s: %v", path, parseErr))
		return
	}
	snapshot.DefaultAffinity = cpus
}

type interruptDescription struct {
	name        string
	description string
}

func probeIRQs(paths IRQProbePaths, onlineCPUCount int, problems *[]string) []IRQEntry {
	descriptions := readInterruptDescriptions(filepath.Join(paths.ProcRoot, "interrupts"), onlineCPUCount, problems)
	irqRoot := filepath.Join(paths.ProcRoot, "irq")
	numbers := make(map[int]struct{}, len(descriptions))
	for number := range descriptions {
		numbers[number] = struct{}{}
	}
	if directories, err := os.ReadDir(irqRoot); err == nil {
		for _, directory := range directories {
			if !directory.IsDir() {
				continue
			}
			number, parseErr := strconv.Atoi(directory.Name())
			if parseErr == nil && number >= 0 {
				numbers[number] = struct{}{}
			}
		}
	} else {
		*problems = append(*problems, fmt.Sprintf("read %s: %v", irqRoot, err))
	}

	sortedNumbers := make([]int, 0, len(numbers))
	for number := range numbers {
		sortedNumbers = append(sortedNumbers, number)
	}
	sort.Ints(sortedNumbers)

	irqs := make([]IRQEntry, 0, len(sortedNumbers))
	for _, number := range sortedNumbers {
		description := descriptions[number]
		entry := IRQEntry{
			Number:      number,
			Name:        description.name,
			Description: description.description,
		}
		entry.readAffinities(paths, irqRoot)
		irqs = append(irqs, entry)
	}
	return irqs
}

func readInterruptDescriptions(path string, onlineCPUCount int, problems *[]string) map[int]interruptDescription {
	result := make(map[int]interruptDescription)
	file, err := os.Open(path)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("read %s: %v", path, err))
		return result
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	cpuColumns := onlineCPUCount
	if scanner.Scan() {
		header := strings.Fields(scanner.Text())
		count := 0
		for _, field := range header {
			if strings.HasPrefix(field, "CPU") {
				count++
			}
		}
		if count > 0 {
			cpuColumns = count
		}
	}
	for scanner.Scan() {
		line := scanner.Text()
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		number, parseErr := strconv.Atoi(strings.TrimSpace(line[:colon]))
		if parseErr != nil {
			continue
		}
		fields := strings.Fields(line[colon+1:])
		if len(fields) <= cpuColumns {
			result[number] = interruptDescription{}
			continue
		}
		descriptionFields := fields[cpuColumns:]
		result[number] = interruptDescription{
			name:        descriptionFields[len(descriptionFields)-1],
			description: strings.Join(descriptionFields, " "),
		}
	}
	if err := scanner.Err(); err != nil {
		*problems = append(*problems, fmt.Sprintf("scan %s: %v", path, err))
	}
	return result
}

func (entry *IRQEntry) readAffinities(paths IRQProbePaths, irqRoot string) {
	directory := filepath.Join(irqRoot, strconv.Itoa(entry.Number))
	requestedPath := filepath.Join(directory, "smp_affinity_list")
	if data, err := os.ReadFile(requestedPath); err == nil {
		entry.AffinityReadable = true
		entry.RequestedAffinity = strings.TrimSpace(string(data))
		if cpus, parseErr := ParseCPUList(entry.RequestedAffinity); parseErr == nil {
			entry.RequestedCPUs = cpus
		}
	}
	if info, err := os.Stat(requestedPath); err == nil {
		entry.AffinityFileWritable = info.Mode().Perm()&0o222 != 0
	}

	effectivePath := filepath.Join(directory, "effective_affinity_list")
	if data, err := os.ReadFile(effectivePath); err == nil {
		entry.EffectiveReadable = true
		entry.EffectiveAffinity = strings.TrimSpace(string(data))
		if cpus, parseErr := ParseCPUList(entry.EffectiveAffinity); parseErr == nil {
			entry.EffectiveCPUs = cpus
		}
	}

	actionsPath := filepath.Join(paths.SysRoot, "kernel/irq", strconv.Itoa(entry.Number), "actions")
	if data, err := os.ReadFile(actionsPath); err == nil {
		if actions := strings.TrimSpace(string(data)); actions != "" {
			entry.Name = actions
		}
	}
}

func probeIRQBalance(paths IRQProbePaths, runner IRQCommandRunner) IRQBalanceStatus {
	status := IRQBalanceStatus{}
	for _, path := range []string{
		filepath.Join(paths.USRRoot, "sbin/irqbalance"),
		filepath.Join(paths.USRRoot, "bin/irqbalance"),
		filepath.Join(paths.USRRoot, "lib/systemd/system/irqbalance.service"),
		filepath.Join(paths.LibRoot, "systemd/system/irqbalance.service"),
		filepath.Join(paths.EtcRoot, "systemd/system/irqbalance.service"),
	} {
		if pathExists(path) {
			status.Installed = true
			break
		}
	}
	if runner == nil {
		return status
	}
	status.Active, status.ActiveKnown = querySystemdBool(runner, "is-active", "active", "irqbalance.service")
	status.Enabled, status.EnabledKnown = querySystemdBool(runner, "is-enabled", "enabled", "irqbalance.service")
	return status
}

func probeManagedIRQPolicy(
	paths IRQProbePaths,
	runner IRQCommandRunner,
	online []int,
	problems *[]string,
) ManagedIRQPolicyStatus {
	configPath := filepath.Join(paths.EtcRoot, strings.TrimPrefix(managedIRQConfigPath, "/etc/"))
	helperPath := filepath.Join(paths.LocalRoot, strings.TrimPrefix(managedIRQHelperPath, "/usr/local/"))
	resultPath := filepath.Join(paths.RunRoot, strings.TrimPrefix(managedIRQResultPath, "/run/"))
	servicePath := filepath.Join(paths.EtcRoot, "systemd/system", managedIRQService)
	vendorServiceCandidates := []string{
		filepath.Join(paths.USRRoot, "lib/systemd/system", managedIRQService),
		filepath.Join(paths.LibRoot, "systemd/system", managedIRQService),
	}

	status := ManagedIRQPolicyStatus{
		ConfigPath:     configPath,
		ConfigPresent:  pathExists(configPath),
		HelperPath:     helperPath,
		HelperPresent:  pathExists(helperPath),
		ServicePath:    servicePath,
		ServicePresent: pathExists(servicePath),
		ResultPath:     resultPath,
		ResultPresent:  pathExists(resultPath),
	}
	status.Config = ManagedIRQComponentStatus{Path: configPath, Present: status.ConfigPresent}
	status.Helper = ManagedIRQComponentStatus{Path: helperPath, Present: status.HelperPresent}
	status.Result = ManagedIRQComponentStatus{Path: resultPath, Present: status.ResultPresent}
	status.Service = ManagedIRQComponentStatus{
		Path:    servicePath,
		Present: status.ServicePresent,
	}
	for _, candidate := range vendorServiceCandidates {
		if pathExists(candidate) {
			*problems = append(
				*problems,
				"found an unmanaged IRQ-affinity service outside /etc: "+candidate,
			)
		}
	}

	if status.ConfigPresent {
		data, err := os.ReadFile(configPath)
		if err != nil {
			*problems = append(*problems, fmt.Sprintf("read managed IRQ config: %v", err))
		} else if parsed, parseErr := parseManagedIRQConfig(data); parseErr != nil {
			*problems = append(*problems, fmt.Sprintf("parse managed IRQ config: %v", parseErr))
		} else {
			validationErr := validateManagedIRQConfigForOnline(parsed, online)
			if validationErr != nil {
				*problems = append(
					*problems,
					fmt.Sprintf("validate managed IRQ config: %v", validationErr),
				)
			} else {
				status.ConfigData = parsed
			}
		}
	}
	if status.ResultPresent {
		data, err := os.ReadFile(resultPath)
		if err != nil {
			*problems = append(*problems, fmt.Sprintf("read managed IRQ result: %v", err))
		} else if parsed, parseErr := parseManagedIRQResult(data); parseErr != nil {
			*problems = append(*problems, fmt.Sprintf("parse managed IRQ result: %v", parseErr))
		} else {
			status.ResultData = parsed
		}
	}
	if runner != nil {
		status.Active, status.ActiveKnown = querySystemdBool(runner, "is-active", "active", managedIRQService)
		status.Enabled, status.EnabledKnown = querySystemdBool(runner, "is-enabled", "enabled", managedIRQService)
	}
	return status
}

func validateManagedIRQConfigForOnline(
	config *ManagedIRQConfig,
	online []int,
) error {
	if config.DefaultPolicy != nil {
		if err := ValidateIRQPolicy(
			online,
			config.DefaultPolicy.ProtectedCPUs,
			config.DefaultPolicy.HousekeepingCPUs,
		); err != nil {
			return fmt.Errorf("default_policy: %w", err)
		}
	}
	onlineSet := intSet(online)
	for index, rule := range config.DeviceRules {
		for _, cpu := range rule.CPUs {
			if _, found := onlineSet[cpu]; !found {
				return fmt.Errorf(
					"device_rules[%d] references offline CPU %d",
					index,
					cpu,
				)
			}
		}
	}
	return nil
}

func querySystemdBool(runner IRQCommandRunner, operation, affirmative, unit string) (bool, bool) {
	output, _ := runner("systemctl", operation, unit)
	value := strings.TrimSpace(string(output))
	if value == affirmative {
		return true, true
	}
	knownPositive := map[string]struct{}{}
	switch operation {
	case "is-active":
		knownPositive = map[string]struct{}{
			"activating":   {},
			"deactivating": {},
			"reloading":    {},
		}
	case "is-enabled":
		knownPositive = map[string]struct{}{
			"enabled-runtime": {},
			"linked":          {},
			"linked-runtime":  {},
			"alias":           {},
		}
	}
	if _, known := knownPositive[value]; known {
		return true, true
	}
	knownNegative := map[string]struct{}{
		"inactive": {}, "failed": {},
		"disabled": {}, "static": {}, "masked": {}, "indirect": {}, "not-found": {},
	}
	if _, known := knownNegative[value]; known {
		return false, true
	}
	return false, false
}

type managedIRQSelectorWire struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type managedIRQDeviceRuleWire struct {
	Selector managedIRQSelectorWire `json:"selector"`
	CPUs     string                 `json:"cpus"`
	Label    string                 `json:"label"`
}

type managedIRQDefaultPolicyWire struct {
	HousekeepingCPUs string `json:"housekeeping_cpus"`
	ProtectedCPUs    string `json:"protected_cpus"`
}

func parseManagedIRQSelector(
	wire managedIRQSelectorWire,
	path string,
) (IRQDeviceSelector, error) {
	kind := IRQDeviceSelectorKind(wire.Kind)
	value := wire.Value
	if !isManagedIRQPrintableText(value, 128, false) {
		return IRQDeviceSelector{}, fmt.Errorf(
			"%s.value is not a safe exact selector",
			path,
		)
	}
	switch kind {
	case IRQDeviceSelectorPCIBDF:
		value = strings.ToLower(value)
		if match := interruptPCIBDFPattern.FindString(value); match != value {
			return IRQDeviceSelector{}, fmt.Errorf(
				"%s.value is not a PCI BDF",
				path,
			)
		}
	case IRQDeviceSelectorAction:
		if strings.Contains(value, ",") || isUnicodeDigits(value) {
			return IRQDeviceSelector{}, fmt.Errorf(
				"%s.value is not a safe exact action",
				path,
			)
		}
	default:
		return IRQDeviceSelector{}, fmt.Errorf(
			"%s.kind %q is unsupported",
			path,
			wire.Kind,
		)
	}
	return IRQDeviceSelector{Kind: kind, Value: value}, nil
}

func parseManagedIRQDeviceRuleWire(
	wire managedIRQDeviceRuleWire,
	path string,
) (ManagedIRQDeviceRule, error) {
	selector, err := parseManagedIRQSelector(wire.Selector, path+".selector")
	if err != nil {
		return ManagedIRQDeviceRule{}, err
	}
	cpus, err := parseManagedIRQNonEmptyCPUList(wire.CPUs, path+".cpus")
	if err != nil {
		return ManagedIRQDeviceRule{}, err
	}
	if !isManagedIRQPrintableText(wire.Label, 128, true) {
		return ManagedIRQDeviceRule{}, fmt.Errorf("%s.label is invalid", path)
	}
	return ManagedIRQDeviceRule{
		Selector: selector,
		CPUList:  FormatCPUList(cpus),
		CPUs:     cpus,
		Label:    wire.Label,
	}, nil
}

func parseManagedIRQNonEmptyCPUList(value, path string) ([]int, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return nil, fmt.Errorf("%s is not a nonempty Linux CPU list", path)
	}
	cpus, err := ParseCPUList(value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(cpus) == 0 {
		return nil, fmt.Errorf("%s must not be empty", path)
	}
	if FormatCPUList(cpus) != value {
		return nil, fmt.Errorf("%s is not a canonical Linux CPU list", path)
	}
	return cpus, nil
}

func isManagedIRQPrintableText(value string, maximumRunes int, allowEmpty bool) bool {
	if !utf8.ValidString(value) ||
		(!allowEmpty && value == "") ||
		strings.TrimSpace(value) != value ||
		utf8.RuneCountInString(value) > maximumRunes {
		return false
	}
	for _, character := range value {
		if !unicode.IsPrint(character) {
			return false
		}
	}
	return true
}

func isUnicodeDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if !unicode.IsDigit(character) {
			return false
		}
	}
	return true
}

func managedIRQSelectorKey(selector IRQDeviceSelector) string {
	return string(selector.Kind) + "\x00" + selector.Value
}

func parseManagedIRQConfig(data []byte) (*ManagedIRQConfig, error) {
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, err
	}
	switch header.SchemaVersion {
	case 1:
		return parseManagedIRQConfigV1(data)
	case 2:
		return parseManagedIRQConfigV2(data)
	default:
		return nil, fmt.Errorf(
			"unsupported schema_version %d",
			header.SchemaVersion,
		)
	}
}

func parseManagedIRQConfigV1(data []byte) (*ManagedIRQConfig, error) {
	var wire struct {
		SchemaVersion    int    `json:"schema_version"`
		HousekeepingCPUs string `json:"housekeeping_cpus"`
		ProtectedCPUs    string `json:"protected_cpus"`
	}
	if err := decodeStrictJSON(data, &wire); err != nil {
		return nil, err
	}
	defaultPolicy, err := parseManagedIRQDefaultPolicy(
		wire.HousekeepingCPUs,
		wire.ProtectedCPUs,
		"",
	)
	if err != nil {
		return nil, err
	}
	return &ManagedIRQConfig{
		SchemaVersion:       wire.SchemaVersion,
		HousekeepingCPUList: wire.HousekeepingCPUs,
		ProtectedCPUList:    wire.ProtectedCPUs,
		HousekeepingCPUs:    defaultPolicy.HousekeepingCPUs,
		ProtectedCPUs:       defaultPolicy.ProtectedCPUs,
		DefaultPolicy:       defaultPolicy,
	}, nil
}

func parseManagedIRQConfigV2(data []byte) (*ManagedIRQConfig, error) {
	var wire struct {
		SchemaVersion int                          `json:"schema_version"`
		DefaultPolicy *managedIRQDefaultPolicyWire `json:"default_policy"`
		DeviceRules   []managedIRQDeviceRuleWire   `json:"device_rules"`
	}
	if err := decodeStrictJSON(data, &wire); err != nil {
		return nil, err
	}
	if wire.SchemaVersion != 2 {
		return nil, fmt.Errorf("unsupported schema_version %d", wire.SchemaVersion)
	}

	config := &ManagedIRQConfig{
		SchemaVersion: 2,
		DeviceRules:   make([]ManagedIRQDeviceRule, 0, len(wire.DeviceRules)),
	}
	if wire.DefaultPolicy != nil {
		defaultPolicy, err := parseManagedIRQDefaultPolicy(
			wire.DefaultPolicy.HousekeepingCPUs,
			wire.DefaultPolicy.ProtectedCPUs,
			"default_policy.",
		)
		if err != nil {
			return nil, err
		}
		config.DefaultPolicy = defaultPolicy
		config.HousekeepingCPUList = defaultPolicy.HousekeepingCPUList
		config.ProtectedCPUList = defaultPolicy.ProtectedCPUList
		config.HousekeepingCPUs = defaultPolicy.HousekeepingCPUs
		config.ProtectedCPUs = defaultPolicy.ProtectedCPUs
	}

	seenSelectors := make(map[string]struct{}, len(wire.DeviceRules))
	for index, wireRule := range wire.DeviceRules {
		rule, err := parseManagedIRQDeviceRuleWire(
			wireRule,
			fmt.Sprintf("device_rules[%d]", index),
		)
		if err != nil {
			return nil, err
		}
		selectorKey := managedIRQSelectorKey(rule.Selector)
		if _, duplicate := seenSelectors[selectorKey]; duplicate {
			return nil, fmt.Errorf(
				"device_rules[%d] duplicates selector %s",
				index,
				rule.Selector.Value,
			)
		}
		seenSelectors[selectorKey] = struct{}{}
		config.DeviceRules = append(config.DeviceRules, rule)
	}
	if config.DefaultPolicy == nil && len(config.DeviceRules) == 0 {
		return nil, errors.New(
			"managed IRQ config must contain a default policy or a device rule",
		)
	}
	return config, nil
}

func parseManagedIRQDefaultPolicy(
	housekeepingValue string,
	protectedValue string,
	prefix string,
) (*ManagedIRQDefaultPolicy, error) {
	housekeeping, err := ParseCPUList(housekeepingValue)
	if err != nil {
		return nil, fmt.Errorf("%shousekeeping_cpus: %w", prefix, err)
	}
	protected, err := ParseCPUList(protectedValue)
	if err != nil {
		return nil, fmt.Errorf("%sprotected_cpus: %w", prefix, err)
	}
	if len(housekeeping) == 0 {
		return nil, fmt.Errorf("%shousekeeping_cpus must not be empty", prefix)
	}
	if len(protected) == 0 {
		return nil, fmt.Errorf("%sprotected_cpus must not be empty", prefix)
	}
	protectedSet := intSet(protected)
	for _, cpu := range housekeeping {
		if _, found := protectedSet[cpu]; found {
			return nil, fmt.Errorf(
				"%sCPU %d appears in both policy roles",
				prefix,
				cpu,
			)
		}
	}
	return &ManagedIRQDefaultPolicy{
		HousekeepingCPUList: FormatCPUList(housekeeping),
		ProtectedCPUList:    FormatCPUList(protected),
		HousekeepingCPUs:    housekeeping,
		ProtectedCPUs:       protected,
	}, nil
}

func parseManagedIRQResult(data []byte) (*ManagedIRQResult, error) {
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, err
	}
	switch header.SchemaVersion {
	case 1:
		return parseManagedIRQResultV1(data)
	case 2:
		return parseManagedIRQResultV2(data)
	default:
		return nil, fmt.Errorf(
			"unsupported schema_version %d",
			header.SchemaVersion,
		)
	}
}

func parseManagedIRQResultV1(data []byte) (*ManagedIRQResult, error) {
	var wire struct {
		SchemaVersion int    `json:"schema_version"`
		GeneratedAt   string `json:"generated_at"`
		Status        string `json:"status"`
		Message       string `json:"message"`
		Policy        struct {
			HousekeepingCPUs string `json:"housekeeping_cpus"`
			ProtectedCPUs    string `json:"protected_cpus"`
		} `json:"policy"`
		OnlineCPUs         string                  `json:"online_cpus"`
		DefaultSMPAffinity string                  `json:"default_smp_affinity"`
		Counts             ManagedIRQResultCounts  `json:"counts"`
		IRQs               []ManagedIRQResultEntry `json:"irqs"`
	}
	if err := decodeStrictJSON(data, &wire); err != nil {
		return nil, err
	}
	if wire.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported schema_version %d", wire.SchemaVersion)
	}
	if wire.Status != "applied" && wire.Status != "failed" {
		return nil, fmt.Errorf("unsupported result status %q", wire.Status)
	}
	if wire.GeneratedAt == "" {
		return nil, errors.New("generated_at must not be empty")
	}

	var housekeeping, protected, online []int
	hasPolicy := wire.Policy.HousekeepingCPUs != "" ||
		wire.Policy.ProtectedCPUs != "" ||
		wire.OnlineCPUs != ""
	if wire.Status == "applied" || hasPolicy {
		var err error
		housekeeping, err = ParseCPUList(wire.Policy.HousekeepingCPUs)
		if err != nil {
			return nil, fmt.Errorf("policy.housekeeping_cpus: %w", err)
		}
		protected, err = ParseCPUList(wire.Policy.ProtectedCPUs)
		if err != nil {
			return nil, fmt.Errorf("policy.protected_cpus: %w", err)
		}
		online, err = ParseCPUList(wire.OnlineCPUs)
		if err != nil {
			return nil, fmt.Errorf("online_cpus: %w", err)
		}
		if err := ValidateIRQPolicy(online, protected, housekeeping); err != nil {
			return nil, fmt.Errorf("result policy: %w", err)
		}
	}
	if wire.DefaultSMPAffinity != "" {
		if _, err := ParseCPUMask(wire.DefaultSMPAffinity); err != nil {
			return nil, fmt.Errorf("default_smp_affinity: %w", err)
		}
	}
	counts := []int{
		wire.Counts.Applied,
		wire.Counts.Constrained,
		wire.Counts.KernelManaged,
		wire.Counts.Unwritable,
		wire.Counts.NoAffinityInterface,
		wire.Counts.Disappeared,
		wire.Counts.Failed,
	}
	for _, count := range counts {
		if count < 0 {
			return nil, errors.New("result counts must not be negative")
		}
	}

	actualCounts := ManagedIRQResultCounts{}
	for index, entry := range wire.IRQs {
		if entry.IRQ < 0 {
			return nil, fmt.Errorf("irqs[%d].irq must not be negative", index)
		}
		if entry.Requested != "" {
			if _, err := ParseCPUList(entry.Requested); err != nil {
				return nil, fmt.Errorf("irqs[%d].requested: %w", index, err)
			}
		}
		if entry.Effective != "" {
			if _, err := ParseCPUList(entry.Effective); err != nil {
				return nil, fmt.Errorf("irqs[%d].effective: %w", index, err)
			}
		}
		switch entry.Status {
		case "applied":
			actualCounts.Applied++
		case "constrained":
			actualCounts.Constrained++
		case "kernel_managed":
			actualCounts.KernelManaged++
		case "unwritable":
			actualCounts.Unwritable++
		case "no_affinity_interface":
			actualCounts.NoAffinityInterface++
		case "disappeared":
			actualCounts.Disappeared++
		case "failed":
			actualCounts.Failed++
		default:
			return nil, fmt.Errorf(
				"irqs[%d] has unsupported status %q",
				index,
				entry.Status,
			)
		}
	}
	if actualCounts != wire.Counts {
		return nil, fmt.Errorf(
			"result counts %+v do not match IRQ records %+v",
			wire.Counts,
			actualCounts,
		)
	}
	if wire.IRQs == nil {
		wire.IRQs = []ManagedIRQResultEntry{}
	}
	return &ManagedIRQResult{
		SchemaVersion: wire.SchemaVersion,
		GeneratedAt:   wire.GeneratedAt,
		Status:        wire.Status,
		Message:       wire.Message,
		Policy: ManagedIRQResultPolicy{
			HousekeepingCPUList: wire.Policy.HousekeepingCPUs,
			ProtectedCPUList:    wire.Policy.ProtectedCPUs,
			HousekeepingCPUs:    housekeeping,
			ProtectedCPUs:       protected,
			DefaultPolicy: func() *ManagedIRQDefaultPolicy {
				if len(housekeeping) == 0 && len(protected) == 0 {
					return nil
				}
				return &ManagedIRQDefaultPolicy{
					HousekeepingCPUList: wire.Policy.HousekeepingCPUs,
					ProtectedCPUList:    wire.Policy.ProtectedCPUs,
					HousekeepingCPUs:    housekeeping,
					ProtectedCPUs:       protected,
				}
			}(),
		},
		OnlineCPUList:      wire.OnlineCPUs,
		OnlineCPUs:         online,
		DefaultSMPAffinity: wire.DefaultSMPAffinity,
		Counts:             wire.Counts,
		IRQs:               wire.IRQs,
	}, nil
}

type managedIRQResultPolicyWireV2 struct {
	DefaultPolicy *managedIRQDefaultPolicyWire `json:"default_policy"`
	DeviceRules   []managedIRQDeviceRuleWire   `json:"device_rules"`
}

type managedIRQDeviceRuleResultWire struct {
	Selector    managedIRQSelectorWire  `json:"selector"`
	Label       string                  `json:"label"`
	Requested   string                  `json:"requested"`
	Status      string                  `json:"status"`
	Detail      string                  `json:"detail"`
	MatchedIRQs []int                   `json:"matched_irqs"`
	Counts      ManagedIRQResultCounts  `json:"counts"`
	IRQs        []ManagedIRQResultEntry `json:"irqs"`
}

type managedIRQResultWireV2 struct {
	SchemaVersion      int                              `json:"schema_version"`
	Operation          string                           `json:"operation"`
	GeneratedAt        string                           `json:"generated_at"`
	Status             string                           `json:"status"`
	Message            string                           `json:"message"`
	Policy             managedIRQResultPolicyWireV2     `json:"policy"`
	OnlineCPUs         string                           `json:"online_cpus"`
	DefaultSMPAffinity string                           `json:"default_smp_affinity"`
	Counts             ManagedIRQResultCounts           `json:"counts"`
	IRQs               []ManagedIRQResultEntry          `json:"irqs"`
	DeviceRuleCounts   ManagedIRQDeviceRuleCounts       `json:"device_rule_counts"`
	DeviceRules        []managedIRQDeviceRuleResultWire `json:"device_rules"`
}

func parseManagedIRQResultV2(data []byte) (*ManagedIRQResult, error) {
	if err := validateManagedIRQResultV2Shape(data); err != nil {
		return nil, err
	}
	var wire managedIRQResultWireV2
	if err := decodeStrictJSON(data, &wire); err != nil {
		return nil, err
	}
	if wire.SchemaVersion != 2 {
		return nil, fmt.Errorf("unsupported schema_version %d", wire.SchemaVersion)
	}
	switch wire.Operation {
	case "boot_apply", "apply_device_live":
	default:
		return nil, fmt.Errorf("unsupported result operation %q", wire.Operation)
	}
	switch wire.Status {
	case "applied", "applied_with_warnings", "failed":
	default:
		return nil, fmt.Errorf("unsupported result status %q", wire.Status)
	}
	if wire.GeneratedAt == "" {
		return nil, errors.New("generated_at must not be empty")
	}
	if _, err := time.Parse(time.RFC3339Nano, wire.GeneratedAt); err != nil {
		return nil, fmt.Errorf("generated_at is not RFC3339: %w", err)
	}

	policy, err := parseManagedIRQResultPolicyV2(wire.Policy)
	if err != nil {
		return nil, err
	}
	online, err := parseManagedIRQResultOnlineCPUs(wire.OnlineCPUs)
	if err != nil {
		return nil, err
	}
	hasPolicy := policy.DefaultPolicy != nil || len(policy.DeviceRules) > 0
	if hasPolicy != (len(online) > 0) {
		return nil, errors.New(
			"result policy and online_cpus must either both be populated or both be empty",
		)
	}
	if wire.Status != "failed" && !hasPolicy {
		return nil, errors.New("successful result must contain a policy")
	}
	if err := validateManagedIRQResultPolicyForOnline(policy, online); err != nil {
		return nil, err
	}

	if wire.DefaultSMPAffinity != "" {
		maskCPUs, parseErr := ParseCPUMask(wire.DefaultSMPAffinity)
		if parseErr != nil {
			return nil, fmt.Errorf("default_smp_affinity: %w", parseErr)
		}
		if policy.DefaultPolicy == nil {
			return nil, errors.New(
				"default_smp_affinity is set without a default policy",
			)
		}
		if FormatCPUList(maskCPUs) !=
			FormatCPUList(policy.DefaultPolicy.HousekeepingCPUs) {
			return nil, errors.New(
				"default_smp_affinity does not match housekeeping CPUs",
			)
		}
	} else if policy.DefaultPolicy != nil && wire.Status != "failed" {
		return nil, errors.New(
			"successful default policy result has no default_smp_affinity",
		)
	}

	expectedBroadRequest := ""
	if policy.DefaultPolicy != nil {
		expectedBroadRequest = policy.DefaultPolicy.HousekeepingCPUList
	}
	if err := validateManagedIRQResultEntries(
		wire.IRQs,
		wire.Counts,
		"irqs",
		expectedBroadRequest,
	); err != nil {
		return nil, err
	}
	if policy.DefaultPolicy == nil &&
		(len(wire.IRQs) > 0 || wire.Counts != (ManagedIRQResultCounts{})) {
		return nil, errors.New(
			"device-only result contains broad IRQ records or counts",
		)
	}

	deviceRules := make([]ManagedIRQDeviceRuleResult, 0, len(wire.DeviceRules))
	for index, wireRule := range wire.DeviceRules {
		rule, parseErr := parseManagedIRQDeviceRuleResult(
			wireRule,
			index,
			wire.Status,
			online,
		)
		if parseErr != nil {
			return nil, parseErr
		}
		deviceRules = append(deviceRules, rule)
	}
	if len(deviceRules) != 0 {
		if len(deviceRules) != len(policy.DeviceRules) {
			return nil, errors.New(
				"resolved device rule count does not match result policy",
			)
		}
		for index := range deviceRules {
			resultRule := deviceRules[index]
			policyRule := policy.DeviceRules[index]
			if resultRule.Selector != policyRule.Selector ||
				resultRule.Label != policyRule.Label ||
				FormatCPUList(resultRule.RequestedCPUs) !=
					FormatCPUList(policyRule.CPUs) {
				return nil, fmt.Errorf(
					"device_rules[%d] does not match policy.device_rules[%d]",
					index,
					index,
				)
			}
		}
	}
	actualDeviceCounts := summarizeManagedIRQDeviceRuleResults(deviceRules)
	if err := validateManagedIRQDeviceRuleCounts(wire.DeviceRuleCounts); err != nil {
		return nil, err
	}
	if actualDeviceCounts != wire.DeviceRuleCounts {
		return nil, fmt.Errorf(
			"device_rule_counts %+v do not match device rule records %+v",
			wire.DeviceRuleCounts,
			actualDeviceCounts,
		)
	}

	if wire.Operation == "apply_device_live" {
		if policy.DefaultPolicy != nil ||
			len(policy.DeviceRules) > 1 ||
			len(wire.IRQs) != 0 ||
			wire.Counts != (ManagedIRQResultCounts{}) ||
			wire.DefaultSMPAffinity != "" {
			return nil, errors.New(
				"live device result contains broad or multiple-device policy data",
			)
		}
		if wire.Status != "failed" &&
			(len(policy.DeviceRules) != 1 ||
				len(deviceRules) != 1 ||
				deviceRules[0].Status != "applied") {
			return nil, errors.New(
				"successful live device result must contain one applied rule",
			)
		}
	}

	hasBroadWarnings := wire.Counts.Constrained > 0 ||
		wire.Counts.KernelManaged > 0 ||
		wire.Counts.Unwritable > 0 ||
		wire.Counts.NoAffinityInterface > 0 ||
		wire.Counts.Disappeared > 0
	hasDeviceWarnings := wire.DeviceRuleCounts.NoMatch > 0 ||
		wire.DeviceRuleCounts.Partial > 0
	hasFailures := wire.Counts.Failed > 0 ||
		wire.DeviceRuleCounts.Failed > 0 ||
		wire.DeviceRuleCounts.UnsafeSelector > 0 ||
		wire.DeviceRuleCounts.AmbiguousSelector > 0
	for _, rule := range deviceRules {
		if rule.Status == "ready" {
			hasFailures = true
		}
	}
	switch wire.Status {
	case "applied":
		if hasBroadWarnings || hasDeviceWarnings || hasFailures {
			return nil, errors.New(
				"applied result contains warning or failure classifications",
			)
		}
	case "applied_with_warnings":
		if !hasBroadWarnings && !hasDeviceWarnings {
			return nil, errors.New(
				"applied_with_warnings result contains no warning classification",
			)
		}
		if hasFailures {
			return nil, errors.New(
				"applied_with_warnings result contains failure classifications",
			)
		}
	case "failed":
		// Preflight failures may happen before any per-IRQ classification.
	}

	if wire.IRQs == nil {
		wire.IRQs = []ManagedIRQResultEntry{}
	}
	if deviceRules == nil {
		deviceRules = []ManagedIRQDeviceRuleResult{}
	}
	return &ManagedIRQResult{
		SchemaVersion:      wire.SchemaVersion,
		Operation:          wire.Operation,
		GeneratedAt:        wire.GeneratedAt,
		Status:             wire.Status,
		Message:            wire.Message,
		Policy:             policy,
		OnlineCPUList:      wire.OnlineCPUs,
		OnlineCPUs:         online,
		DefaultSMPAffinity: wire.DefaultSMPAffinity,
		Counts:             wire.Counts,
		IRQs:               wire.IRQs,
		DeviceRuleCounts:   wire.DeviceRuleCounts,
		DeviceRules:        deviceRules,
	}, nil
}

func parseManagedIRQResultPolicyV2(
	wire managedIRQResultPolicyWireV2,
) (ManagedIRQResultPolicy, error) {
	policy := ManagedIRQResultPolicy{
		DeviceRules: make([]ManagedIRQDeviceRule, 0, len(wire.DeviceRules)),
	}
	if wire.DefaultPolicy != nil {
		defaultPolicy, err := parseManagedIRQDefaultPolicy(
			wire.DefaultPolicy.HousekeepingCPUs,
			wire.DefaultPolicy.ProtectedCPUs,
			"policy.default_policy.",
		)
		if err != nil {
			return ManagedIRQResultPolicy{}, err
		}
		policy.DefaultPolicy = defaultPolicy
		policy.HousekeepingCPUList = defaultPolicy.HousekeepingCPUList
		policy.ProtectedCPUList = defaultPolicy.ProtectedCPUList
		policy.HousekeepingCPUs = defaultPolicy.HousekeepingCPUs
		policy.ProtectedCPUs = defaultPolicy.ProtectedCPUs
	}
	seenSelectors := make(map[string]struct{}, len(wire.DeviceRules))
	for index, wireRule := range wire.DeviceRules {
		rule, err := parseManagedIRQDeviceRuleWire(
			wireRule,
			fmt.Sprintf("policy.device_rules[%d]", index),
		)
		if err != nil {
			return ManagedIRQResultPolicy{}, err
		}
		key := managedIRQSelectorKey(rule.Selector)
		if _, duplicate := seenSelectors[key]; duplicate {
			return ManagedIRQResultPolicy{}, fmt.Errorf(
				"policy.device_rules[%d] duplicates selector %s",
				index,
				rule.Selector.Value,
			)
		}
		seenSelectors[key] = struct{}{}
		policy.DeviceRules = append(policy.DeviceRules, rule)
	}
	return policy, nil
}

func parseManagedIRQResultOnlineCPUs(value string) ([]int, error) {
	if value == "" {
		return []int{}, nil
	}
	cpus, err := parseManagedIRQNonEmptyCPUList(value, "online_cpus")
	if err != nil {
		return nil, err
	}
	return cpus, nil
}

func validateManagedIRQResultPolicyForOnline(
	policy ManagedIRQResultPolicy,
	online []int,
) error {
	if policy.DefaultPolicy != nil {
		if err := ValidateIRQPolicy(
			online,
			policy.DefaultPolicy.ProtectedCPUs,
			policy.DefaultPolicy.HousekeepingCPUs,
		); err != nil {
			return fmt.Errorf("result default_policy: %w", err)
		}
	}
	onlineSet := intSet(online)
	for index, rule := range policy.DeviceRules {
		for _, cpu := range rule.CPUs {
			if _, found := onlineSet[cpu]; !found {
				return fmt.Errorf(
					"policy.device_rules[%d] references offline CPU %d",
					index,
					cpu,
				)
			}
		}
	}
	return nil
}

func parseManagedIRQDeviceRuleResult(
	wire managedIRQDeviceRuleResultWire,
	index int,
	resultStatus string,
	online []int,
) (ManagedIRQDeviceRuleResult, error) {
	path := fmt.Sprintf("device_rules[%d]", index)
	selector, err := parseManagedIRQSelector(wire.Selector, path+".selector")
	if err != nil {
		return ManagedIRQDeviceRuleResult{}, err
	}
	if !isManagedIRQPrintableText(wire.Label, 128, true) {
		return ManagedIRQDeviceRuleResult{}, fmt.Errorf("%s.label is invalid", path)
	}
	requested, err := parseManagedIRQNonEmptyCPUList(
		wire.Requested,
		path+".requested",
	)
	if err != nil {
		return ManagedIRQDeviceRuleResult{}, err
	}
	onlineSet := intSet(online)
	for _, cpu := range requested {
		if _, found := onlineSet[cpu]; !found {
			return ManagedIRQDeviceRuleResult{}, fmt.Errorf(
				"%s.requested references offline CPU %d",
				path,
				cpu,
			)
		}
	}
	if err := validateManagedIRQResultEntries(
		wire.IRQs,
		wire.Counts,
		path+".irqs",
		FormatCPUList(requested),
	); err != nil {
		return ManagedIRQDeviceRuleResult{}, err
	}

	matchedSet := make(map[int]struct{}, len(wire.MatchedIRQs))
	for matchedIndex, irq := range wire.MatchedIRQs {
		if irq < 0 {
			return ManagedIRQDeviceRuleResult{}, fmt.Errorf(
				"%s.matched_irqs[%d] must not be negative",
				path,
				matchedIndex,
			)
		}
		if _, duplicate := matchedSet[irq]; duplicate {
			return ManagedIRQDeviceRuleResult{}, fmt.Errorf(
				"%s.matched_irqs contains duplicate IRQ %d",
				path,
				irq,
			)
		}
		matchedSet[irq] = struct{}{}
	}
	entrySet := make(map[int]struct{}, len(wire.IRQs))
	for _, entry := range wire.IRQs {
		entrySet[entry.IRQ] = struct{}{}
	}

	switch wire.Status {
	case "ready":
		if resultStatus != "failed" {
			return ManagedIRQDeviceRuleResult{}, fmt.Errorf(
				"%s has ready status in a non-failed result",
				path,
			)
		}
		if len(wire.MatchedIRQs) == 0 ||
			len(wire.IRQs) != 0 ||
			wire.Counts != (ManagedIRQResultCounts{}) {
			return ManagedIRQDeviceRuleResult{}, fmt.Errorf(
				"%s has invalid ready classification",
				path,
			)
		}
	case "no_match":
		if len(wire.MatchedIRQs) != 0 ||
			len(wire.IRQs) != 0 ||
			wire.Counts != (ManagedIRQResultCounts{}) {
			return ManagedIRQDeviceRuleResult{}, fmt.Errorf(
				"%s has invalid no_match classification",
				path,
			)
		}
	case "unsafe_selector", "ambiguous_selector":
		if resultStatus != "failed" ||
			len(wire.MatchedIRQs) == 0 ||
			len(wire.IRQs) != 0 ||
			wire.Counts != (ManagedIRQResultCounts{}) {
			return ManagedIRQDeviceRuleResult{}, fmt.Errorf(
				"%s has invalid %s classification",
				path,
				wire.Status,
			)
		}
	case "applied", "partial", "failed":
		if len(wire.MatchedIRQs) == 0 ||
			len(wire.IRQs) != len(wire.MatchedIRQs) ||
			!equalIntSets(matchedSet, entrySet) {
			return ManagedIRQDeviceRuleResult{}, fmt.Errorf(
				"%s IRQ records do not match matched_irqs",
				path,
			)
		}
		switch wire.Status {
		case "applied":
			if wire.Counts.Applied != len(wire.IRQs) ||
				managedIRQResultCountTotal(wire.Counts) != len(wire.IRQs) {
				return ManagedIRQDeviceRuleResult{}, fmt.Errorf(
					"%s has invalid applied classification",
					path,
				)
			}
		case "partial":
			if wire.Counts.Failed != 0 ||
				wire.Counts.Applied == len(wire.IRQs) {
				return ManagedIRQDeviceRuleResult{}, fmt.Errorf(
					"%s has invalid partial classification",
					path,
				)
			}
		case "failed":
			if wire.Counts.Failed == 0 || resultStatus != "failed" {
				return ManagedIRQDeviceRuleResult{}, fmt.Errorf(
					"%s has invalid failed classification",
					path,
				)
			}
		}
	default:
		return ManagedIRQDeviceRuleResult{}, fmt.Errorf(
			"%s has unsupported status %q",
			path,
			wire.Status,
		)
	}
	if wire.Detail == "" && wire.Status != "ready" {
		return ManagedIRQDeviceRuleResult{}, fmt.Errorf("%s.detail is empty", path)
	}
	if wire.IRQs == nil {
		wire.IRQs = []ManagedIRQResultEntry{}
	}
	if wire.MatchedIRQs == nil {
		wire.MatchedIRQs = []int{}
	}
	return ManagedIRQDeviceRuleResult{
		Selector:      selector,
		Label:         wire.Label,
		Requested:     wire.Requested,
		RequestedCPUs: requested,
		Status:        wire.Status,
		Detail:        wire.Detail,
		MatchedIRQs:   wire.MatchedIRQs,
		Counts:        wire.Counts,
		IRQs:          wire.IRQs,
	}, nil
}

func validateManagedIRQResultEntries(
	entries []ManagedIRQResultEntry,
	counts ManagedIRQResultCounts,
	path string,
	expectedRequested string,
) error {
	if err := validateManagedIRQResultCounts(counts, path+".counts"); err != nil {
		return err
	}
	actual := ManagedIRQResultCounts{}
	seenIRQs := make(map[int]struct{}, len(entries))
	for index, entry := range entries {
		entryPath := fmt.Sprintf("%s[%d]", path, index)
		if entry.IRQ < 0 {
			return fmt.Errorf("%s.irq must not be negative", entryPath)
		}
		if _, duplicate := seenIRQs[entry.IRQ]; duplicate {
			return fmt.Errorf("%s contains duplicate IRQ %d", path, entry.IRQ)
		}
		seenIRQs[entry.IRQ] = struct{}{}
		requested, err := parseManagedIRQNonEmptyCPUList(
			entry.Requested,
			entryPath+".requested",
		)
		if err != nil {
			return err
		}
		if expectedRequested == "" ||
			FormatCPUList(requested) != expectedRequested {
			return fmt.Errorf(
				"%s.requested does not match its policy",
				entryPath,
			)
		}
		if entry.Effective != "" {
			if _, err := parseManagedIRQNonEmptyCPUList(
				entry.Effective,
				entryPath+".effective",
			); err != nil {
				return err
			}
		}
		switch entry.Status {
		case "applied":
			actual.Applied++
		case "constrained":
			actual.Constrained++
		case "kernel_managed":
			actual.KernelManaged++
		case "unwritable":
			actual.Unwritable++
		case "no_affinity_interface":
			actual.NoAffinityInterface++
		case "disappeared":
			actual.Disappeared++
		case "failed":
			actual.Failed++
		default:
			return fmt.Errorf(
				"%s has unsupported status %q",
				entryPath,
				entry.Status,
			)
		}
	}
	if actual != counts {
		return fmt.Errorf(
			"%s counts %+v do not match IRQ records %+v",
			path,
			counts,
			actual,
		)
	}
	return nil
}

func validateManagedIRQResultCounts(
	counts ManagedIRQResultCounts,
	path string,
) error {
	for _, count := range []int{
		counts.Applied,
		counts.Constrained,
		counts.KernelManaged,
		counts.Unwritable,
		counts.NoAffinityInterface,
		counts.Disappeared,
		counts.Failed,
	} {
		if count < 0 {
			return fmt.Errorf("%s must not contain negative values", path)
		}
	}
	return nil
}

func validateManagedIRQDeviceRuleCounts(
	counts ManagedIRQDeviceRuleCounts,
) error {
	for _, count := range []int{
		counts.Configured,
		counts.Matched,
		counts.NoMatch,
		counts.UnsafeSelector,
		counts.AmbiguousSelector,
		counts.Applied,
		counts.Partial,
		counts.Failed,
	} {
		if count < 0 {
			return errors.New("device_rule_counts must not contain negative values")
		}
	}
	return nil
}

func summarizeManagedIRQDeviceRuleResults(
	rules []ManagedIRQDeviceRuleResult,
) ManagedIRQDeviceRuleCounts {
	counts := ManagedIRQDeviceRuleCounts{Configured: len(rules)}
	for _, rule := range rules {
		if len(rule.MatchedIRQs) > 0 {
			counts.Matched++
		}
		switch rule.Status {
		case "no_match":
			counts.NoMatch++
		case "unsafe_selector":
			counts.UnsafeSelector++
		case "ambiguous_selector":
			counts.AmbiguousSelector++
		case "applied":
			counts.Applied++
		case "partial":
			counts.Partial++
		case "failed":
			counts.Failed++
		case "ready":
			// The helper intentionally leaves preflight-ready rules out of the
			// terminal-status counters when another selector aborts the apply.
		}
	}
	return counts
}

func managedIRQResultCountTotal(counts ManagedIRQResultCounts) int {
	return counts.Applied +
		counts.Constrained +
		counts.KernelManaged +
		counts.Unwritable +
		counts.NoAffinityInterface +
		counts.Disappeared +
		counts.Failed
}

func equalIntSets(left, right map[int]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for value := range left {
		if _, found := right[value]; !found {
			return false
		}
	}
	return true
}

func validateManagedIRQResultV2Shape(data []byte) error {
	root, err := requireManagedIRQJSONObject(
		data,
		"result",
		"schema_version",
		"operation",
		"generated_at",
		"status",
		"message",
		"policy",
		"online_cpus",
		"default_smp_affinity",
		"counts",
		"irqs",
		"device_rule_counts",
		"device_rules",
	)
	if err != nil {
		return err
	}
	policy, err := requireManagedIRQJSONObject(
		root["policy"],
		"policy",
		"default_policy",
		"device_rules",
	)
	if err != nil {
		return err
	}
	if string(bytes.TrimSpace(policy["default_policy"])) != "null" {
		if _, err := requireManagedIRQJSONObject(
			policy["default_policy"],
			"policy.default_policy",
			"housekeeping_cpus",
			"protected_cpus",
		); err != nil {
			return err
		}
	}
	if err := validateManagedIRQPolicyRuleArrayShape(
		policy["device_rules"],
		"policy.device_rules",
	); err != nil {
		return err
	}
	if _, err := requireManagedIRQJSONObject(
		root["counts"],
		"counts",
		"applied",
		"constrained",
		"kernel_managed",
		"unwritable",
		"no_affinity_interface",
		"disappeared",
		"failed",
	); err != nil {
		return err
	}
	if err := validateManagedIRQEntryArrayShape(root["irqs"], "irqs"); err != nil {
		return err
	}
	if _, err := requireManagedIRQJSONObject(
		root["device_rule_counts"],
		"device_rule_counts",
		"configured",
		"matched",
		"no_match",
		"unsafe_selector",
		"ambiguous_selector",
		"applied",
		"partial",
		"failed",
	); err != nil {
		return err
	}
	rules, err := requireManagedIRQJSONArray(root["device_rules"], "device_rules")
	if err != nil {
		return err
	}
	for index, ruleData := range rules {
		path := fmt.Sprintf("device_rules[%d]", index)
		rule, objectErr := requireManagedIRQJSONObject(
			ruleData,
			path,
			"selector",
			"label",
			"requested",
			"status",
			"detail",
			"matched_irqs",
			"counts",
			"irqs",
		)
		if objectErr != nil {
			return objectErr
		}
		if err := validateManagedIRQSelectorShape(
			rule["selector"],
			path+".selector",
		); err != nil {
			return err
		}
		if _, err := requireManagedIRQJSONArray(
			rule["matched_irqs"],
			path+".matched_irqs",
		); err != nil {
			return err
		}
		if _, err := requireManagedIRQJSONObject(
			rule["counts"],
			path+".counts",
			"applied",
			"constrained",
			"kernel_managed",
			"unwritable",
			"no_affinity_interface",
			"disappeared",
			"failed",
		); err != nil {
			return err
		}
		if err := validateManagedIRQEntryArrayShape(
			rule["irqs"],
			path+".irqs",
		); err != nil {
			return err
		}
	}
	return nil
}

func validateManagedIRQPolicyRuleArrayShape(data []byte, path string) error {
	rules, err := requireManagedIRQJSONArray(data, path)
	if err != nil {
		return err
	}
	for index, ruleData := range rules {
		rulePath := fmt.Sprintf("%s[%d]", path, index)
		rule, objectErr := requireManagedIRQJSONObject(
			ruleData,
			rulePath,
			"selector",
			"cpus",
			"label",
		)
		if objectErr != nil {
			return objectErr
		}
		if err := validateManagedIRQSelectorShape(
			rule["selector"],
			rulePath+".selector",
		); err != nil {
			return err
		}
	}
	return nil
}

func validateManagedIRQSelectorShape(data []byte, path string) error {
	_, err := requireManagedIRQJSONObject(data, path, "kind", "value")
	return err
}

func validateManagedIRQEntryArrayShape(data []byte, path string) error {
	entries, err := requireManagedIRQJSONArray(data, path)
	if err != nil {
		return err
	}
	for index, entry := range entries {
		if _, err := requireManagedIRQJSONObject(
			entry,
			fmt.Sprintf("%s[%d]", path, index),
			"irq",
			"status",
			"requested",
			"effective",
			"detail",
		); err != nil {
			return err
		}
	}
	return nil
}

func requireManagedIRQJSONObject(
	data []byte,
	path string,
	expectedKeys ...string,
) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("%s must be an object: %w", path, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%s must be an object", path)
	}
	expected := make(map[string]struct{}, len(expectedKeys))
	for _, key := range expectedKeys {
		expected[key] = struct{}{}
	}
	if len(object) != len(expected) {
		return nil, fmt.Errorf("%s has missing or unknown fields", path)
	}
	for key := range object {
		if _, found := expected[key]; !found {
			return nil, fmt.Errorf("%s has unknown field %q", path, key)
		}
	}
	return object, nil
}

func requireManagedIRQJSONArray(
	data []byte,
	path string,
) ([]json.RawMessage, error) {
	var array []json.RawMessage
	if err := json.Unmarshal(data, &array); err != nil {
		return nil, fmt.Errorf("%s must be an array: %w", path, err)
	}
	if array == nil {
		return nil, fmt.Errorf("%s must be an array", path)
	}
	return array, nil
}

func decodeStrictJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return fmt.Errorf("read trailing JSON data: %w", err)
	}
	return nil
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
