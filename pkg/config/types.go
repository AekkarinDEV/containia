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
	Ports       []string        `json:"ports,omitempty"`
	RootfsPath  string          `json:"rootfs_path"`
	WorkingDir  string          `json:"working_dir,omitempty"`
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
	WorkingDir  string
	Ports       []string // e.g. ["8000:8000", "3000:3000"]
	Env         []string
	Volumes     []string
}

// ImageConfigDef holds configuration such as Env, Cmd, Entrypoint, etc.
type ImageConfigDef struct {
	Env          []string               `json:"Env,omitempty"`
	Cmd          []string               `json:"Cmd,omitempty"`
	Entrypoint   []string               `json:"Entrypoint,omitempty"`
	WorkingDir   string                 `json:"WorkingDir,omitempty"`
	ExposedPorts map[string]interface{} `json:"ExposedPorts,omitempty"`
	User         string                 `json:"User,omitempty"`
}

// ImageConfigFile represents the OCI/Docker image config JSON blob.
type ImageConfigFile struct {
	Architecture string         `json:"architecture,omitempty"`
	OS           string         `json:"os,omitempty"`
	Config       ImageConfigDef `json:"config"`
}

// ImageMetadata stores full metadata of a locally stored image.
type ImageMetadata struct {
	Name         string         `json:"name"`
	Tag          string         `json:"tag"`
	ID           string         `json:"id"`            // short config digest or unique ID
	ConfigDigest string         `json:"config_digest"` // e.g. sha256:...
	Layers       []string       `json:"layers"`        // layer digests from bottom to top
	Config       ImageConfigDef `json:"config"`
	Size         int64          `json:"size"`
	CreatedAt    time.Time      `json:"created_at"`
}
