package hostprobe

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math/bits"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const mebibyte = 1024 * 1024

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Collect observes local Linux state without changing it. It never reads
// process environments, service credentials, runner configuration, or
// container metadata that could contain secrets.
func Collect(ctx context.Context) (Snapshot, error) {
	return collect(ctx, "/", ExecRunner{}, time.Now, os.Hostname)
}

func collect(
	ctx context.Context,
	root string,
	runner CommandRunner,
	now func() time.Time,
	hostname func() (string, error),
) (Snapshot, error) {
	if runtime.GOOS != "linux" {
		return Snapshot{}, fmt.Errorf("host preflight requires Linux")
	}
	if root == "" {
		root = "/"
	}

	osRelease, err := parseKeyValueFile(filepath.Join(root, "etc", "os-release"))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read os-release: %w", err)
	}
	memory, err := parseMeminfo(filepath.Join(root, "proc", "meminfo"))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read meminfo: %w", err)
	}
	memory.OOMKillsTotal, err = parseOOMKills(filepath.Join(root, "proc", "vmstat"))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read vmstat OOM counter: %w", err)
	}
	pressure, err := parsePressure(filepath.Join(root, "proc", "pressure"))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read pressure stall information: %w", err)
	}
	load1, load5, load15, err := parseLoadavg(filepath.Join(root, "proc", "loadavg"))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read loadavg: %w", err)
	}
	host, err := hostname()
	if err != nil {
		return Snapshot{}, fmt.Errorf("read hostname: %w", err)
	}

	logical, physical, sockets, threads, numa := cpuTopology(root)
	filesystem, err := rootFilesystem(root, runner, ctx)
	if err != nil {
		return Snapshot{}, err
	}
	kvm := kvmState(root)

	kernel, _ := runCommand(ctx, runner, "uname", "-r")
	architecture, _ := runCommand(ctx, runner, "uname", "-m")
	if architecture == "" {
		architecture = runtime.GOARCH
	}
	virtualization, _ := runCommand(ctx, runner, "systemd-detect-virt")
	if virtualization == "" {
		virtualization = "none"
	}

	incusVersion, incusErr := runCommand(ctx, runner, "incus", "version")
	dockerVersion, dockerErr := runCommand(ctx, runner, "docker", "version", "--format={{.Server.Version}}")
	systemState, _ := runCommand(ctx, runner, "systemctl", "is-system-running")
	failedUnits := failedServiceUnits(ctx, runner)
	listeners, workers := runnerProcesses(filepath.Join(root, "proc"))

	return Snapshot{
		SchemaVersion: 1,
		CapturedAt:    now().UTC().Format(time.RFC3339),
		Hostname:      strings.TrimSpace(host),
		OperatingSystem: OperatingSystem{
			ID:           osRelease["ID"],
			VersionID:    osRelease["VERSION_ID"],
			PrettyName:   osRelease["PRETTY_NAME"],
			Kernel:       kernel,
			Architecture: architecture,
		},
		Virtualization: virtualization,
		CPU: CPU{
			Logical:        logical,
			PhysicalCores:  physical,
			Sockets:        sockets,
			ThreadsPerCore: threads,
			NUMANodes:      numa,
			Load1:          load1,
			Load5:          load5,
			Load15:         load15,
		},
		Memory:         memory,
		Pressure:       pressure,
		RootFilesystem: filesystem,
		KVM:            kvm,
		Maintenance: Maintenance{
			RebootRequired: fileExists(filepath.Join(root, "var", "run", "reboot-required")),
			SystemState:    systemState,
			FailedUnits:    failedUnits,
		},
		Software: Software{
			Incus:  softwareVersion(incusVersion, incusErr),
			Docker: softwareVersion(dockerVersion, dockerErr),
		},
		LegacyRunners: LegacyRunners{Listeners: listeners, Workers: workers},
	}, nil
}

func parseOOMKills(path string) (uint64, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || fields[0] != "oom_kill" {
			continue
		}
		value, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr != nil {
			return 0, fmt.Errorf("parse oom_kill: %w", parseErr)
		}
		return value, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, nil
}

// ReadPressure observes Linux pressure-stall information under root without
// running the rest of the host preflight. The compute-member observer needs
// exactly this and nothing else: it must not shell out, inspect services or
// read anything that could carry a secret.
func ReadPressure(root string) (Pressure, error) {
	return parsePressure(filepath.Join(root, "proc", "pressure"))
}

func parsePressure(directory string) (Pressure, error) {
	result := Pressure{}
	for _, resource := range []struct {
		name   string
		target *PressureResource
	}{
		{name: "cpu", target: &result.CPU},
		{name: "memory", target: &result.Memory},
		{name: "io", target: &result.IO},
	} {
		parsed, available, err := parsePressureResource(filepath.Join(directory, resource.name))
		if err != nil {
			return Pressure{}, fmt.Errorf("parse %s pressure: %w", resource.name, err)
		}
		if !available {
			return Pressure{}, nil
		}
		*resource.target = parsed
	}
	result.Available = true
	return result, nil
}

func parsePressureResource(path string) (PressureResource, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return PressureResource{}, false, nil
	}
	if err != nil {
		return PressureResource{}, false, err
	}
	defer file.Close()
	result := PressureResource{}
	seen := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 5 || (fields[0] != "some" && fields[0] != "full") {
			return PressureResource{}, false, fmt.Errorf("unexpected pressure line %q", scanner.Text())
		}
		window, err := parsePressureWindow(fields[1:])
		if err != nil {
			return PressureResource{}, false, err
		}
		if fields[0] == "some" {
			result.Some = window
		} else {
			result.Full = window
		}
		seen = true
	}
	if err := scanner.Err(); err != nil {
		return PressureResource{}, false, err
	}
	if !seen {
		return PressureResource{}, false, fmt.Errorf("pressure file is empty")
	}
	return result, true, nil
}

func parsePressureWindow(fields []string) (PressureWindow, error) {
	values := make(map[string]string, len(fields))
	for _, field := range fields {
		key, value, found := strings.Cut(field, "=")
		if !found {
			return PressureWindow{}, fmt.Errorf("invalid pressure field %q", field)
		}
		values[key] = value
	}
	var result PressureWindow
	var err error
	for key, target := range map[string]*float64{"avg10": &result.Avg10, "avg60": &result.Avg60, "avg300": &result.Avg300} {
		*target, err = strconv.ParseFloat(values[key], 64)
		if err != nil {
			return PressureWindow{}, fmt.Errorf("parse %s: %w", key, err)
		}
		if *target < 0 || *target > 100 {
			return PressureWindow{}, fmt.Errorf("%s is outside 0..100", key)
		}
	}
	result.TotalMicros, err = strconv.ParseUint(values["total"], 10, 64)
	if err != nil {
		return PressureWindow{}, fmt.Errorf("parse total: %w", err)
	}
	return result, nil
}

func parseKeyValueFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		values[strings.TrimSpace(key)] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func parseMeminfo(path string) (Memory, error) {
	file, err := os.Open(path)
	if err != nil {
		return Memory{}, err
	}
	defer file.Close()

	values := make(map[string]int64)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, parseErr := strconv.ParseInt(fields[1], 10, 64)
		if parseErr != nil {
			return Memory{}, fmt.Errorf("parse %s: %w", fields[0], parseErr)
		}
		values[strings.TrimSuffix(fields[0], ":")] = value / 1024
	}
	if err := scanner.Err(); err != nil {
		return Memory{}, err
	}
	if values["MemTotal"] <= 0 {
		return Memory{}, fmt.Errorf("MemTotal is missing")
	}
	return Memory{
		TotalMiB:     int(values["MemTotal"]),
		AvailableMiB: int(values["MemAvailable"]),
		SwapTotalMiB: int(values["SwapTotal"]),
		SwapFreeMiB:  int(values["SwapFree"]),
	}, nil
}

func parseLoadavg(path string) (float64, float64, float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0, fmt.Errorf("loadavg needs at least three fields")
	}
	values := make([]float64, 3)
	for index := range values {
		values[index], err = strconv.ParseFloat(fields[index], 64)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("parse loadavg field %d: %w", index, err)
		}
	}
	return values[0], values[1], values[2], nil
}

func cpuTopology(root string) (logical, physical, sockets, threads, numa int) {
	cpuPaths, _ := filepath.Glob(filepath.Join(root, "sys", "devices", "system", "cpu", "cpu[0-9]*"))
	cores := make(map[string]struct{})
	packages := make(map[string]struct{})
	for _, cpuPath := range cpuPaths {
		packageID := strings.TrimSpace(readOptional(filepath.Join(cpuPath, "topology", "physical_package_id")))
		coreID := strings.TrimSpace(readOptional(filepath.Join(cpuPath, "topology", "core_id")))
		if packageID == "" {
			packageID = "0"
		}
		if coreID == "" {
			coreID = filepath.Base(cpuPath)
		}
		packages[packageID] = struct{}{}
		cores[packageID+":"+coreID] = struct{}{}
	}
	logical = len(cpuPaths)
	if logical == 0 {
		logical = runtime.NumCPU()
	}
	physical = len(cores)
	if physical == 0 {
		physical = logical
	}
	sockets = len(packages)
	if sockets == 0 {
		sockets = 1
	}
	threads = max(1, logical/physical)
	numaPaths, _ := filepath.Glob(filepath.Join(root, "sys", "devices", "system", "node", "node[0-9]*"))
	numa = max(1, len(numaPaths))
	return logical, physical, sockets, threads, numa
}

func rootFilesystem(root string, runner CommandRunner, ctx context.Context) (Filesystem, error) {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(root, &stats); err != nil {
		return Filesystem{}, fmt.Errorf("stat root filesystem: %w", err)
	}
	total := uint64(stats.Blocks) * uint64(stats.Bsize)
	available := uint64(stats.Bavail) * uint64(stats.Bsize)
	var rootStat syscall.Stat_t
	loopAllocated := uint64(0)
	if syscall.Stat(root, &rootStat) == nil {
		loopAllocated = loopBackingAllocatedOn(root, uint64(rootStat.Dev))
	}
	freePercent := usableFreePercent(total, available, loopAllocated)
	freeInodesPercent := 100
	if stats.Files > 0 {
		freeInodesPercent = percent(uint64(stats.Ffree), uint64(stats.Files))
	}

	filesystem := Filesystem{
		Type:              filesystemType(stats.Type),
		TotalMiB:          total / mebibyte,
		AvailableMiB:      available / mebibyte,
		FreePercent:       freePercent,
		FreeInodesPercent: freeInodesPercent,
	}
	findmnt, findmntErr := runCommand(ctx, runner, "findmnt", "-n", "-o", "SOURCE,FSTYPE", root)
	if findmntErr != nil {
		findmnt = ""
	}
	fields := strings.Fields(findmnt)
	if len(fields) >= 1 {
		filesystem.Source = fields[0]
	}
	if len(fields) >= 2 {
		filesystem.Type = fields[1]
	}
	if filesystem.Source != "" {
		rotational, rotationalErr := runCommand(ctx, runner, "lsblk", "-ndo", "ROTA", filesystem.Source)
		if rotationalErr != nil {
			rotational = ""
		}
		switch strings.TrimSpace(rotational) {
		case "0":
			filesystem.RotationalKnown = true
		case "1":
			filesystem.RotationalKnown = true
			filesystem.Rotational = true
		}
	}
	return filesystem, nil
}

// usableFreePercent is the free share of the host disk that jobs, caches and
// logs can actually consume. The Incus loop-backed thin pool is a fixed
// reservation on the same root filesystem; counting it as tenant-used space
// made admission and compute_root_disk_low fire while the pool itself was
// mostly empty. docs/maintenance-windows.md requires both values; this is the
// host-side one. Placement already reads the thin pool.
func usableFreePercent(total, available, loopAllocated uint64) int {
	if loopAllocated == 0 || loopAllocated >= total {
		return percent(available, total)
	}
	usableTotal := total - loopAllocated
	if available >= usableTotal {
		return 100
	}
	return percent(available, usableTotal)
}

func loopBackingAllocatedOn(root string, rootDev uint64) uint64 {
	matches, err := filepath.Glob(filepath.Join(root, "var", "lib", "incus", "disks", "*.img"))
	if err != nil {
		return 0
	}
	var allocated uint64
	for _, path := range matches {
		var st syscall.Stat_t
		if syscall.Stat(path, &st) != nil || uint64(st.Dev) != rootDev {
			continue
		}
		allocated += uint64(st.Blocks) * 512
	}
	return allocated
}

func kvmState(root string) KVM {
	device := filepath.Join(root, "dev", "kvm")
	present := fileExists(device)
	accessible := false
	if present {
		file, err := os.OpenFile(device, os.O_RDWR, 0)
		if err == nil {
			accessible = true
			_ = file.Close()
		}
	}
	nested := false
	for _, path := range []string{
		filepath.Join(root, "sys", "module", "kvm_intel", "parameters", "nested"),
		filepath.Join(root, "sys", "module", "kvm_amd", "parameters", "nested"),
	} {
		value := strings.ToLower(strings.TrimSpace(readOptional(path)))
		if value == "1" || value == "y" || value == "yes" {
			nested = true
		}
	}
	return KVM{Present: present, Accessible: accessible, Nested: nested}
}

func runnerProcesses(procRoot string) (listeners, workers int) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0, 0
	}
	for _, entry := range entries {
		if !entry.IsDir() || !allDigits(entry.Name()) {
			continue
		}
		command := strings.TrimSpace(readOptional(filepath.Join(procRoot, entry.Name(), "comm")))
		cgroup := readOptional(filepath.Join(procRoot, entry.Name(), "cgroup"))
		// The host /proc includes processes from Incus containers. Counting
		// their official one-job listeners as legacy host services makes a busy
		// healthy fleet appear to retain exactly as many legacy listeners as it
		// currently has container workers.
		if strings.Contains(cgroup, "/lxc.payload.") || strings.Contains(cgroup, "/machine.slice/") {
			continue
		}
		switch command {
		case "Runner.Listener":
			listeners++
		case "Runner.Worker":
			workers++
		}
	}
	return listeners, workers
}

// failedServiceUnits lists the service units systemd currently reports as
// failed. A missing or unreadable listing yields no names, which evaluation
// treats as an unclassifiable degradation rather than as proof of health.
func failedServiceUnits(ctx context.Context, runner CommandRunner) []string {
	commandContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := runner.Run(commandContext, "systemctl", "list-units", "--type=service",
		"--state=failed", "--no-legend", "--plain", "--no-pager")
	if err != nil {
		return nil
	}
	units := make([]string, 0)
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		units = append(units, fields[0])
	}
	if len(units) == 0 {
		return nil
	}
	slices.Sort(units)
	return slices.Compact(units)
}

func runCommand(ctx context.Context, runner CommandRunner, name string, args ...string) (string, error) {
	commandContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := runner.Run(commandContext, name, args...)
	return firstLine(string(output)), err
}

func softwareVersion(value string, err error) SoftwareVersion {
	if err != nil || value == "" {
		return SoftwareVersion{}
	}
	return SoftwareVersion{Present: true, Version: value}
}

func firstLine(value string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(value), "\n")
	return strings.TrimSpace(line)
}

func filesystemType(value int64) string {
	switch uint64(value) {
	case 0xef53:
		return "ext4"
	case 0x58465342:
		return "xfs"
	case 0x9123683e:
		return "btrfs"
	case 0x2fc12fc1:
		return "zfs"
	case 0x794c7630:
		return "overlay"
	default:
		return fmt.Sprintf("0x%x", uint64(value))
	}
}

func percent(part, total uint64) int {
	if total == 0 {
		return 0
	}
	whole := part / total
	remainder := part % total
	high, low := bits.Mul64(remainder, 100)
	fraction, leftover := bits.Div64(high, low, total)
	if leftover > 0 {
		fraction++
	}
	return int(whole*100 + fraction)
}

func readOptional(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}
