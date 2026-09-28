package config

import (
	"time"
)

// ContainerStatus represents the current lifecycle state of a container.
type ContainerStatus string

const (
	StatusCreated ContainerStatus = "Created"
	StatusRunning ContainerStatus = "Running"
	StatusExited  ContainerStatus = "Exited"
	StatusStopped ContainerStatus = "Stopped"
)

// ContainerState holds runtime information about a container.
type ContainerState struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Image       string          `json:"image"`
	Command     []string        `json:"command"`
	PID         int             `json:"pid"`
	Status      ContainerStatus `json:"status"`
	ExitCode    int             `json:"exit_code"`
	CreatedAt   time.Time       `json:"created_at"`
	MemoryLimit int64           `json:"memory_limit"` // In bytes, 0 for unlimited
	CPUShares   string          `json:"cpu_shares"`   // e.g. "50000 100000"
	PidsLimit   int64           `json:"pids_limit"`   // Max number of processes
	IPAddress   string          `json:"ip_address,omitempty"`
	RootfsPath  string          `json:"rootfs_path"`
	Volumes     []string        `json:"volumes,omitempty"`
	Env         []string        `json:"env,omitempty"`
}

// RunFlags defines the CLI flags for 'containia run'.
type RunFlags struct {
	Name        string
	Memory      string // e.g. "128m", "1g"
	CPUs        string // e.g. "0.5", "1"
	PidsLimit   int64
	Interactive bool
	Tty         bool
	Detach      bool
	Remove      bool
	Network     string // "bridge", "none"
	Env         []string
	Volumes     []string
}
