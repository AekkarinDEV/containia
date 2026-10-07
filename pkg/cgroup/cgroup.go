package cgroup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"containia/pkg/config"
)

type Manager struct {
	ContainerID string
	Path        string
}

func NewManager(containerID string) *Manager {
	return &Manager{
		ContainerID: containerID,
		Path:        config.GetContainerCgroupPath(containerID),
	}
}

func (m *Manager) Initialize() error {
	parentDir := config.CgroupDir
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return fmt.Errorf("failed to create cgroup root dir %s: %w", parentDir, err)
	}

	_ = enableControllers("/sys/fs/cgroup/cgroup.subtree_control")
	_ = enableControllers(filepath.Join(parentDir, "cgroup.subtree_control"))

	if err := os.MkdirAll(m.Path, 0755); err != nil {
		return fmt.Errorf("failed to create container cgroup dir %s: %w", m.Path, err)
	}

	return nil
}

func (m *Manager) ApplyLimits(memBytes int64, cpuQuota string, pidsLimit int64) error {
	if memBytes > 0 {
		memFile := filepath.Join(m.Path, "memory.max")
		if err := os.WriteFile(memFile, []byte(strconv.FormatInt(memBytes, 10)), 0644); err != nil {
			return fmt.Errorf("failed to write memory.max: %w", err)
		}
	}

	if cpuQuota != "" {
		cpuFile := filepath.Join(m.Path, "cpu.max")
		if err := os.WriteFile(cpuFile, []byte(cpuQuota), 0644); err != nil {
			return fmt.Errorf("failed to write cpu.max: %w", err)
		}
	}

	if pidsLimit > 0 {
		pidsFile := filepath.Join(m.Path, "pids.max")
		if err := os.WriteFile(pidsFile, []byte(strconv.FormatInt(pidsLimit, 10)), 0644); err != nil {
			return fmt.Errorf("failed to write pids.max: %w", err)
		}
	}

	return nil
}

func (m *Manager) AddProcess(pid int) error {
	procsFile := filepath.Join(m.Path, "cgroup.procs")
	if err := os.WriteFile(procsFile, []byte(strconv.Itoa(pid)), 0644); err != nil {
		return fmt.Errorf("failed to attach pid %d to cgroup: %w", pid, err)
	}
	return nil
}

func (m *Manager) Destroy() error {
	if _, err := os.Stat(m.Path); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(m.Path)
}

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
		if fullVal, err := strconv.ParseInt(s, 10, 64); err == nil {
			return fullVal, nil
		}
		return 0, fmt.Errorf("unknown memory unit in %s", s)
	}
}

func ParseCPULimit(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	val, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || val <= 0 {
		return "", fmt.Errorf("invalid cpu limit: %s", s)
	}

	period := 100000
	quota := int(val * float64(period))
	return fmt.Sprintf("%d %d", quota, period), nil
}
