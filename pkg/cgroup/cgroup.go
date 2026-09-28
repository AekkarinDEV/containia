package cgroup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"containia/pkg/config"
)

// Manager manages a container's cgroups v2 resource boundaries.
type Manager struct {
	ContainerID string
	Path        string
}

// NewManager creates a cgroup manager for the given container ID.
func NewManager(containerID string) *Manager {
	return &Manager{
		ContainerID: containerID,
		Path:        config.GetContainerCgroupPath(containerID),
	}
}

// Initialize prepares the cgroups v2 hierarchy for containia.
// It ensures the parent directory exists and enables controllers in subtree_control.
func (m *Manager) Initialize() error {
	parentDir := config.CgroupDir
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return fmt.Errorf("failed to create cgroup root dir %s: %w", parentDir, err)
	}

	// Try enabling controllers in root cgroup subtree_control if possible
	_ = enableControllers("/sys/fs/cgroup/cgroup.subtree_control")
	_ = enableControllers(filepath.Join(parentDir, "cgroup.subtree_control"))

	// Create specific container cgroup directory
	if err := os.MkdirAll(m.Path, 0755); err != nil {
		return fmt.Errorf("failed to create container cgroup dir %s: %w", m.Path, err)
	}

	return nil
}

// ApplyLimits writes memory, cpu, and pids limits to the container cgroup.
func (m *Manager) ApplyLimits(memBytes int64, cpuQuota string, pidsLimit int64) error {
	// 1. Memory limit (memory.max)
	if memBytes > 0 {
		memFile := filepath.Join(m.Path, "memory.max")
		if err := os.WriteFile(memFile, []byte(strconv.FormatInt(memBytes, 10)), 0644); err != nil {
			return fmt.Errorf("failed to write memory.max: %w", err)
		}
	}

	// 2. CPU quota (cpu.max format: "<quota> <period>", e.g. "50000 100000")
	if cpuQuota != "" {
		cpuFile := filepath.Join(m.Path, "cpu.max")
		if err := os.WriteFile(cpuFile, []byte(cpuQuota), 0644); err != nil {
			return fmt.Errorf("failed to write cpu.max: %w", err)
		}
	}

	// 3. PIDs limit (pids.max)
	if pidsLimit > 0 {
		pidsFile := filepath.Join(m.Path, "pids.max")
		if err := os.WriteFile(pidsFile, []byte(strconv.FormatInt(pidsLimit, 10)), 0644); err != nil {
			return fmt.Errorf("failed to write pids.max: %w", err)
		}
	}

	return nil
}

// AddProcess adds a process PID to the container's cgroup.
func (m *Manager) AddProcess(pid int) error {
	procsFile := filepath.Join(m.Path, "cgroup.procs")
	if err := os.WriteFile(procsFile, []byte(strconv.Itoa(pid)), 0644); err != nil {
		return fmt.Errorf("failed to attach pid %d to cgroup: %w", pid, err)
	}
	return nil
}

// Destroy cleans up the container's cgroup directory.
func (m *Manager) Destroy() error {
	if _, err := os.Stat(m.Path); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(m.Path)
}

// Helper to write "+cpu +memory +pids" to cgroup.subtree_control
func enableControllers(subtreeFile string) error {
	content, err := os.ReadFile(subtreeFile)
	if err != nil {
		return err
	}

	current := string(content)
	var toAdd []string
	for _, ctrl := range []string{"memory", "pids", "cpu"} {
		if !strings.Contains(current, ctrl) {
			toAdd = append(toAdd, "+"+ctrl)
		}
	}

	if len(toAdd) > 0 {
		_ = os.WriteFile(subtreeFile, []byte(strings.Join(toAdd, " ")), 0644)
	}
	return nil
}

// ParseMemoryString parses strings like "128m", "512mb", "1g" into bytes.
func ParseMemoryString(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimSuffix(s, "b")

	unit := s[len(s)-1]
	valStr := s[:len(s)-1]
	val, err := strconv.ParseInt(valStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memory format: %s", s)
	}

	switch unit {
	case 'k':
		return val * 1024, nil
	case 'm':
		return val * 1024 * 1024, nil
	case 'g':
		return val * 1024 * 1024 * 1024, nil
	default:
		// Attempt parsing as pure number in bytes
		if fullVal, err := strconv.ParseInt(s, 10, 64); err == nil {
			return fullVal, nil
		}
		return 0, fmt.Errorf("unknown memory unit in %s", s)
	}
}

// ParseCPULimit parses CPU strings like "0.5", "1.5", "2" into cgroups v2 cpu.max format ("quota period").
func ParseCPULimit(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	val, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || val <= 0 {
		return "", fmt.Errorf("invalid cpu limit: %s", s)
	}

	period := 100000 // default period is 100ms (100,000us)
	quota := int(val * float64(period))
	return fmt.Sprintf("%d %d", quota, period), nil
}
