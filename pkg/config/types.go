package config

import (
	"time"
)

type ContainerStatus string

const (
	StatusCreated ContainerStatus = "Created"
	StatusRunning ContainerStatus = "Running"
	StatusExited  ContainerStatus = "Exited"
	StatusStopped ContainerStatus = "Stopped"
)

type ContainerState struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Image       string          `json:"image"`
	Command     []string        `json:"command"`
	PID         int             `json:"pid"`
	Status      ContainerStatus `json:"status"`
	ExitCode    int             `json:"exit_code"`
	CreatedAt   time.Time       `json:"created_at"`
	MemoryLimit int64           `json:"memory_limit"`
	CPUShares   string          `json:"cpu_shares"`
	PidsLimit   int64           `json:"pids_limit"`
	IPAddress   string          `json:"ip_address,omitempty"`
	Ports       []string        `json:"ports,omitempty"`
	RootfsPath  string          `json:"rootfs_path"`
	WorkingDir  string          `json:"working_dir,omitempty"`
	Volumes     []string        `json:"volumes,omitempty"`
	Env         []string        `json:"env,omitempty"`
}

type RunFlags struct {
	Name                string
	Memory              string
	CPUs                string
	PidsLimit           int64
	Interactive         bool
	Tty                 bool
	Detach              bool
	Remove              bool
	Network             string
	WorkingDir          string
	Ports               []string
	PublishExposedPorts bool
	Env                 []string
	Volumes             []string
}

type ImageConfigDef struct {
	Env          []string               `json:"Env,omitempty"`
	Cmd          []string               `json:"Cmd,omitempty"`
	Entrypoint   []string               `json:"Entrypoint,omitempty"`
	WorkingDir   string                 `json:"WorkingDir,omitempty"`
	ExposedPorts map[string]interface{} `json:"ExposedPorts,omitempty"`
	User         string                 `json:"User,omitempty"`
}

type ImageConfigFile struct {
	Architecture string         `json:"architecture,omitempty"`
	OS           string         `json:"os,omitempty"`
	Config       ImageConfigDef `json:"config"`
}

type ImageMetadata struct {
	Name         string         `json:"name"`
	Tag          string         `json:"tag"`
	Internal     bool           `json:"internal,omitempty"`
	ID           string         `json:"id"`
	ConfigDigest string         `json:"config_digest"`
	Layers       []string       `json:"layers"`
	Config       ImageConfigDef `json:"config"`
	Size         int64          `json:"size"`
	CreatedAt    time.Time      `json:"created_at"`
}
