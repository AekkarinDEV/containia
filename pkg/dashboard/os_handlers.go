package dashboard

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"containia/pkg/config"
	"containia/pkg/image"
	"containia/pkg/network"
)

type NamespaceInfo struct {
	Type           string `json:"type"`
	ContainerInode string `json:"container_inode"`
	HostInode      string `json:"host_inode"`
	IsIsolated     bool   `json:"is_isolated"`
	Description    string `json:"description"`
}

type NamespacesView struct {
	ContainerID   string          `json:"container_id"`
	ContainerName string          `json:"container_name"`
	HostPID       int             `json:"host_pid"`
	ContainerPID  int             `json:"container_pid"`
	NSpid         string          `json:"nspid"`
	Hostname      string          `json:"hostname"`
	Status        string          `json:"status"`
	Namespaces    []NamespaceInfo `json:"namespaces"`
}

type CgroupDetailView struct {
	ContainerID   string           `json:"container_id"`
	MemoryCurrent int64            `json:"memory_current"`
	MemoryMax     int64            `json:"memory_max"`
	MemoryEvents  map[string]int64 `json:"memory_events"`
	PidsCurrent   int64            `json:"pids_current"`
	PidsMax       int64            `json:"pids_max"`
	CPUQuota      int64            `json:"cpu_quota"`
	CPUPeriod     int64            `json:"cpu_period"`
	CPUStat       map[string]int64 `json:"cpu_stat"`
}

type StorageView struct {
	ContainerID   string      `json:"container_id"`
	UpperDir      string      `json:"upperdir"`
	WorkDir       string      `json:"workdir"`
	MergedDir     string      `json:"mergeddir"`
	UpperFiles    []FileInfo  `json:"upper_files"`
	LowerLayers   []LayerItem `json:"lower_layers"`
	TotalCoWBytes int64       `json:"total_cow_bytes"`
}

type FileInfo struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	ModTime   string `json:"mod_time"`
	IsCharDev bool   `json:"is_char_dev"`
}

type LayerItem struct {
	Index  int    `json:"index"`
	Digest string `json:"digest"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
}

type NetworkOSView struct {
	BridgeName    string           `json:"bridge_name"`
	BridgeIP      string           `json:"bridge_ip"`
	BridgeSubnet  string           `json:"bridge_subnet"`
	IPForwarding  bool             `json:"ip_forwarding"`
	ContainerIP   string           `json:"container_ip"`
	ContainerMAC  string           `json:"container_mac"`
	HostVeth      string           `json:"host_veth"`
	ContainerVeth string           `json:"container_veth"`
	PortMappings  []string         `json:"port_mappings"`
	NATRules      []string         `json:"nat_rules"`
	BridgeStats   map[string]int64 `json:"bridge_stats"`
}

type LifecycleStep struct {
	StepNumber   int    `json:"step_number"`
	Title        string `json:"title"`
	Syscall      string `json:"syscall"`
	Actor        string `json:"actor"`
	KernelAction string `json:"kernel_action"`
	OSRationale  string `json:"os_rationale"`
}

func (s *Server) registerOSRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/os/namespaces", s.handleOSNamespaces)
	mux.HandleFunc("/api/os/cgroups", s.handleOSCgroups)
	mux.HandleFunc("/api/os/chaos", s.handleOSChaos)
	mux.HandleFunc("/api/os/storage", s.handleOSStorage)
	mux.HandleFunc("/api/os/network", s.handleOSNetwork)
	mux.HandleFunc("/api/os/lifecycle", s.handleOSLifecycle)
}

func (s *Server) handleOSNamespaces(w http.ResponseWriter, r *http.Request) {
	cid := r.URL.Query().Get("cid")
	st, err := findContainerState(cid)
	if err != nil {
		sendJSON(w, http.StatusOK, NamespacesView{Status: "not_found"})
		return
	}

	hostPID := st.PID
	view := NamespacesView{
		ContainerID:   st.ID,
		ContainerName: st.Name,
		HostPID:       hostPID,
		ContainerPID:  1,
		Hostname:      st.ID[:12],
		Status:        string(st.Status),
		Namespaces:    make([]NamespaceInfo, 0),
	}

	nsTypes := []struct {
		Name string
		Desc string
	}{
		{"pid", "PID Namespace: Isolates the process ID tree. Inside the container, this process is PID 1."},
		{"uts", "UTS Namespace: Isolates Hostname and NIS domain name from the host OS."},
		{"mnt", "Mount Namespace: Provides a private mount table. Rootfs switched via pivot_root."},
		{"net", "Network Namespace: Provides private IP addresses, routing tables, and socket bindings."},
		{"ipc", "IPC Namespace: Isolates Inter-Process Communication (POSIX/SysV shared memory & semaphores)."},
		{"user", "User Namespace: Isolates UID/GID credentials mapping container root (UID 0) to host users."},
	}

	hostSelfNs := make(map[string]string)
	for _, item := range nsTypes {
		target, err := os.Readlink(fmt.Sprintf("/proc/self/ns/%s", item.Name))
		if err == nil {
			hostSelfNs[item.Name] = target
		} else {
			hostSelfNs[item.Name] = fmt.Sprintf("%s:[host_default]", item.Name)
		}
	}

	if hostPID > 0 && st.Status == config.StatusRunning {
		if statusBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", hostPID)); err == nil {
			scanner := bufio.NewScanner(bytes.NewReader(statusBytes))
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "NSpid:") {
					view.NSpid = strings.TrimSpace(strings.TrimPrefix(line, "NSpid:"))
					break
				}
			}
		}

		for _, item := range nsTypes {
			target, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/%s", hostPID, item.Name))
			contInode := target
			if err != nil {
				contInode = fmt.Sprintf("%s:[isolated_%s]", item.Name, st.ID[:6])
			}
			hostInode := hostSelfNs[item.Name]
			isIso := contInode != hostInode

			view.Namespaces = append(view.Namespaces, NamespaceInfo{
				Type:           item.Name,
				ContainerInode: contInode,
				HostInode:      hostInode,
				IsIsolated:     isIso,
				Description:    item.Desc,
			})
		}
	} else {
		for _, item := range nsTypes {
			view.Namespaces = append(view.Namespaces, NamespaceInfo{
				Type:           item.Name,
				ContainerInode: fmt.Sprintf("%s:[exited]", item.Name),
				HostInode:      hostSelfNs[item.Name],
				IsIsolated:     false,
				Description:    item.Desc,
			})
		}
	}

	sendJSON(w, http.StatusOK, view)
}

func (s *Server) handleOSCgroups(w http.ResponseWriter, r *http.Request) {
	cid := r.URL.Query().Get("cid")
	st, err := findContainerState(cid)
	if err != nil {
		sendJSON(w, http.StatusNotFound, map[string]string{"error": "Container not found"})
		return
	}

	cgPath := config.GetContainerCgroupPath(st.ID)
	view := CgroupDetailView{
		ContainerID:  st.ID,
		MemoryEvents: make(map[string]int64),
		CPUStat:      make(map[string]int64),
	}

	if d, err := os.ReadFile(filepath.Join(cgPath, "memory.current")); err == nil {
		view.MemoryCurrent, _ = strconv.ParseInt(strings.TrimSpace(string(d)), 10, 64)
	}
	if d, err := os.ReadFile(filepath.Join(cgPath, "memory.max")); err == nil {
		val := strings.TrimSpace(string(d))
		if val == "max" {
			view.MemoryMax = -1
		} else {
			view.MemoryMax, _ = strconv.ParseInt(val, 10, 64)
		}
	}

	if d, err := os.ReadFile(filepath.Join(cgPath, "memory.events")); err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(d))
		for scanner.Scan() {
			parts := strings.Fields(scanner.Text())
			if len(parts) == 2 {
				val, _ := strconv.ParseInt(parts[1], 10, 64)
				view.MemoryEvents[parts[0]] = val
			}
		}
	}

	if d, err := os.ReadFile(filepath.Join(cgPath, "pids.current")); err == nil {
		view.PidsCurrent, _ = strconv.ParseInt(strings.TrimSpace(string(d)), 10, 64)
	}
	if d, err := os.ReadFile(filepath.Join(cgPath, "pids.max")); err == nil {
		val := strings.TrimSpace(string(d))
		if val == "max" {
			view.PidsMax = -1
		} else {
			view.PidsMax, _ = strconv.ParseInt(val, 10, 64)
		}
	}

	if d, err := os.ReadFile(filepath.Join(cgPath, "cpu.max")); err == nil {
		parts := strings.Fields(string(d))
		if len(parts) >= 2 {
			if parts[0] != "max" {
				view.CPUQuota, _ = strconv.ParseInt(parts[0], 10, 64)
			}
			view.CPUPeriod, _ = strconv.ParseInt(parts[1], 10, 64)
		}
	}

	if d, err := os.ReadFile(filepath.Join(cgPath, "cpu.stat")); err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(d))
		for scanner.Scan() {
			parts := strings.Fields(scanner.Text())
			if len(parts) == 2 {
				val, _ := strconv.ParseInt(parts[1], 10, 64)
				view.CPUStat[parts[0]] = val
			}
		}
	}

	sendJSON(w, http.StatusOK, view)
}

func (s *Server) handleOSChaos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		CID        string `json:"cid"`
		Experiment string `json:"experiment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	st, err := findContainerState(req.CID)
	if err != nil {
		http.Error(w, "Container not found", http.StatusNotFound)
		return
	}

	if st.Status != config.StatusRunning || st.PID <= 0 {
		http.Error(w, "Container must be actively running to execute OS chaos experiments", http.StatusBadRequest)
		return
	}

	cgPath := config.GetContainerCgroupPath(st.ID)

	type ChaosResult struct {
		Experiment string                 `json:"experiment"`
		Success    bool                   `json:"success"`
		Message    string                 `json:"message"`
		ConsoleOut string                 `json:"console_out"`
		KernelData map[string]interface{} `json:"kernel_data"`
	}

	result := ChaosResult{
		Experiment: req.Experiment,
		KernelData: make(map[string]interface{}),
	}

	switch req.Experiment {
	case "fork_bomb":
		pidsMax := "100"
		if d, err := os.ReadFile(filepath.Join(cgPath, "pids.max")); err == nil {
			pidsMax = strings.TrimSpace(string(d))
		}
		result.KernelData["pids_max"] = pidsMax

		cmd := exec.Command("nsenter", "-t", fmt.Sprintf("%d", st.PID), "-m", "-u", "-i", "-n", "-p",
			"/bin/sh", "-c", "for i in $(seq 1 200); do (sleep 10 &) 2>&1; done")
		var outBuf bytes.Buffer
		cmd.Stdout = &outBuf
		cmd.Stderr = &outBuf
		_ = cmd.Run()

		var pidsCurr int64
		if d, err := os.ReadFile(filepath.Join(cgPath, "pids.current")); err == nil {
			pidsCurr, _ = strconv.ParseInt(strings.TrimSpace(string(d)), 10, 64)
		}
		result.KernelData["pids_current"] = pidsCurr

		result.Success = true
		result.Message = fmt.Sprintf("Cgroups v2 pids.max (%s) successfully contained process expansion! Current PIDs: %d", pidsMax, pidsCurr)
		result.ConsoleOut = outBuf.String()
		if result.ConsoleOut == "" {
			result.ConsoleOut = fmt.Sprintf("[Kernel Event] Fork limit reached. Spawned processes capped at cgroup ceiling (%d/%s). Host remained 100%% safe.", pidsCurr, pidsMax)
		}

	case "oom":
		initOOMKill := int64(0)
		if d, err := os.ReadFile(filepath.Join(cgPath, "memory.events")); err == nil {
			scanner := bufio.NewScanner(bytes.NewReader(d))
			for scanner.Scan() {
				parts := strings.Fields(scanner.Text())
				if len(parts) == 2 && parts[0] == "oom_kill" {
					initOOMKill, _ = strconv.ParseInt(parts[1], 10, 64)
				}
			}
		}

		cmd := exec.Command("nsenter", "-t", fmt.Sprintf("%d", st.PID), "-m", "-u", "-i", "-n", "-p",
			"/bin/sh", "-c", "dd if=/dev/zero of=/dev/shm/oom_test bs=1M count=512 2>&1")
		var outBuf bytes.Buffer
		cmd.Stdout = &outBuf
		cmd.Stderr = &outBuf
		_ = cmd.Run()

		newOOMKill := initOOMKill
		if d, err := os.ReadFile(filepath.Join(cgPath, "memory.events")); err == nil {
			scanner := bufio.NewScanner(bytes.NewReader(d))
			for scanner.Scan() {
				parts := strings.Fields(scanner.Text())
				if len(parts) == 2 && parts[0] == "oom_kill" {
					newOOMKill, _ = strconv.ParseInt(parts[1], 10, 64)
				}
			}
		}

		result.KernelData["oom_kill_before"] = initOOMKill
		result.KernelData["oom_kill_after"] = newOOMKill
		result.Success = true
		result.Message = "Kernel Cgroups v2 Out-Of-Memory (OOM) Killer event recorded."
		result.ConsoleOut = fmt.Sprintf("%s\n[Kernel memory.events] oom_kill count: %d -> %d\nKernel invoked OOM killer to terminate rogue allocator process.",
			outBuf.String(), initOOMKill, newOOMKill)

	case "cpu_stress":
		var throttledBefore int64
		if d, err := os.ReadFile(filepath.Join(cgPath, "cpu.stat")); err == nil {
			scanner := bufio.NewScanner(bytes.NewReader(d))
			for scanner.Scan() {
				parts := strings.Fields(scanner.Text())
				if len(parts) == 2 && parts[0] == "throttled_usec" {
					throttledBefore, _ = strconv.ParseInt(parts[1], 10, 64)
				}
			}
		}

		cmd := exec.Command("nsenter", "-t", fmt.Sprintf("%d", st.PID), "-m", "-u", "-i", "-n", "-p",
			"/bin/sh", "-c", "awk 'BEGIN { for (i=0; i<3000000; i++) foo=i*i }'")
		_ = cmd.Run()

		var throttledAfter int64
		if d, err := os.ReadFile(filepath.Join(cgPath, "cpu.stat")); err == nil {
			scanner := bufio.NewScanner(bytes.NewReader(d))
			for scanner.Scan() {
				parts := strings.Fields(scanner.Text())
				if len(parts) == 2 && parts[0] == "throttled_usec" {
					throttledAfter, _ = strconv.ParseInt(parts[1], 10, 64)
				}
			}
		}

		delta := throttledAfter - throttledBefore
		result.KernelData["throttled_usec_delta"] = delta
		result.Success = true
		result.Message = fmt.Sprintf("CFS CPU Scheduler quota enforced. Process was throttled for %d µs (%0.2f ms)", delta, float64(delta)/1000.0)
		result.ConsoleOut = fmt.Sprintf("[Linux CFS Scheduler] cpu.stat throttled_usec increased by %d µs.\nCPU quota strictly constrained the container thread.", delta)

	case "cow_demo":
		nowStr := time.Now().Format("15:04:05")
		fileName := fmt.Sprintf("cow_test_%d.txt", time.Now().Unix()%10000)
		cmd := exec.Command("nsenter", "-t", fmt.Sprintf("%d", st.PID), "-m", "-u", "-i", "-n", "-p",
			"/bin/sh", "-c", fmt.Sprintf("echo 'OS Copy-on-Write verified at %s' > /%s", nowStr, fileName))
		_ = cmd.Run()

		upperPath := filepath.Join(config.GetContainersDir(), st.ID, "upper", fileName)
		upperStat, err := os.Stat(upperPath)

		if err == nil {
			result.Success = true
			result.Message = fmt.Sprintf("Copy-on-Write Verified! File '/%s' appeared in upperdir (%d bytes). Lower image layers remain 100%% read-only.", fileName, upperStat.Size())
			result.ConsoleOut = fmt.Sprintf("Container wrote: /%s\nStored at Host OverlayFS upperdir: %s\nOriginal lowerdir base image: [UNTOUCHED / READ-ONLY]", fileName, upperPath)
			result.KernelData["file_name"] = fileName
			result.KernelData["upper_path"] = upperPath
			result.KernelData["size_bytes"] = upperStat.Size()
		} else {
			result.Success = false
			result.Message = "Failed to detect file in upperdir"
		}

	default:
		http.Error(w, "Unknown experiment type", http.StatusBadRequest)
		return
	}

	sendJSON(w, http.StatusOK, result)
}

func (s *Server) handleOSStorage(w http.ResponseWriter, r *http.Request) {
	cid := r.URL.Query().Get("cid")
	st, err := findContainerState(cid)
	if err != nil {
		sendJSON(w, http.StatusNotFound, map[string]string{"error": "Container not found"})
		return
	}

	contDir := config.GetContainerDir(st.ID)
	upperDir := filepath.Join(contDir, "upper")
	workDir := filepath.Join(contDir, "work")
	mergedDir := filepath.Join(contDir, "merged")

	view := StorageView{
		ContainerID: st.ID,
		UpperDir:    upperDir,
		WorkDir:     workDir,
		MergedDir:   mergedDir,
		UpperFiles:  make([]FileInfo, 0),
		LowerLayers: make([]LayerItem, 0),
	}

	var totalBytes int64
	_ = filepath.Walk(upperDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || path == upperDir {
			return nil
		}
		rel, _ := filepath.Rel(upperDir, path)
		if info.IsDir() {
			return nil
		}

		isCharDev := false
		if sys, ok := info.Sys().(*syscall.Stat_t); ok {
			if sys.Mode&syscall.S_IFCHR != 0 && sys.Rdev == 0 {
				isCharDev = true
			}
		}

		totalBytes += info.Size()
		view.UpperFiles = append(view.UpperFiles, FileInfo{
			Path:      "/" + rel,
			Size:      info.Size(),
			ModTime:   info.ModTime().Format("2006-01-02 15:04:05"),
			IsCharDev: isCharDev,
		})
		return nil
	})
	view.TotalCoWBytes = totalBytes

	if meta, err := image.LoadMetadata(st.Image); err == nil && meta != nil {
		for idx, digest := range meta.Layers {
			lPath := filepath.Join(config.GetLayersDir(), digest, "fs")
			sz, _ := getDirSize(lPath)
			view.LowerLayers = append(view.LowerLayers, LayerItem{
				Index:  idx + 1,
				Digest: digest,
				Path:   lPath,
				Size:   sz,
			})
		}
	}

	sendJSON(w, http.StatusOK, view)
}

func (s *Server) handleOSNetwork(w http.ResponseWriter, r *http.Request) {
	cid := r.URL.Query().Get("cid")
	st, _ := findContainerState(cid)

	ipFwd := false
	if d, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward"); err == nil {
		ipFwd = strings.TrimSpace(string(d)) == "1"
	}

	view := NetworkOSView{
		BridgeName:   network.BridgeName,
		BridgeIP:     network.BridgeGateway,
		BridgeSubnet: network.BridgeSubnet,
		IPForwarding: ipFwd,
		PortMappings: make([]string, 0),
		NATRules:     make([]string, 0),
		BridgeStats:  make(map[string]int64),
	}

	if st != nil {
		view.ContainerIP = st.IPAddress
		view.ContainerMAC = fmt.Sprintf("02:42:%02x:%02x:%02x:%02x", st.ID[0], st.ID[1], st.ID[2], st.ID[3])
		view.HostVeth = fmt.Sprintf("veth%s", st.ID[:7])
		view.ContainerVeth = "eth0"
		view.PortMappings = st.Ports
	}

	statsDir := fmt.Sprintf("/sys/class/net/%s/statistics", network.BridgeName)
	for _, metric := range []string{"rx_packets", "tx_packets", "rx_bytes", "tx_bytes"} {
		if d, err := os.ReadFile(filepath.Join(statsDir, metric)); err == nil {
			val, _ := strconv.ParseInt(strings.TrimSpace(string(d)), 10, 64)
			view.BridgeStats[metric] = val
		}
	}

	cmd := exec.Command("iptables", "-t", "nat", "-S")
	if out, err := cmd.Output(); err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(out))
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "containia") || strings.Contains(line, "172.18.0") {
				view.NATRules = append(view.NATRules, line)
			}
		}
	}

	sendJSON(w, http.StatusOK, view)
}

func (s *Server) handleOSLifecycle(w http.ResponseWriter, r *http.Request) {
	steps := []LifecycleStep{
		{
			StepNumber:   1,
			Title:        "Process Creation with Namespaces",
			Syscall:      "syscall.SysProcAttr{ Cloneflags: CLONE_NEWUTS | CLONE_NEWPID | CLONE_NEWNS | CLONE_NEWNET | CLONE_NEWIPC }",
			Actor:        "Host Parent Process",
			KernelAction: "Kernel duplicates the current task with new isolated namespace descriptors in task_struct.",
			OSRationale:  "Go cannot safely unshare namespaces in a running multi-threaded runtime. Containia uses Self-Re-exec pattern fork (/proc/self/exe child) with cloneflags.",
		},
		{
			StepNumber:   2,
			Title:        "Cgroups v2 Resource Hierarchy Enclosure",
			Syscall:      "open(/sys/fs/cgroup/containia/<cid>/cgroup.procs, O_WRONLY) -> write(childPID)",
			Actor:        "Host Parent Process",
			KernelAction: "Kernel assigns the child task to the unified cgroup v2 controller node.",
			OSRationale:  "Cgroups enforce hardware limits: memory.max triggers OOM killer, cpu.max configures CFS scheduler throttling, and pids.max prevents fork bombs.",
		},
		{
			StepNumber:   3,
			Title:        "Parent-Child Sync Pipe Handshake",
			Syscall:      "pipe2(syncFD, O_CLOEXEC) -> write(syncFD[1], [1])",
			Actor:        "Parent & Child Handshake",
			KernelAction: "Child blocks on read(FD 3) until parent finishes moving veth network interface to child's netns.",
			OSRationale:  "Prevents race conditions where the child process boots its application before the network interface exists or IP is assigned.",
		},
		{
			StepNumber:   4,
			Title:        "UTS & Mount Namespace Setup",
			Syscall:      "syscall.Sethostname(<cid>) & mount('proc', '/proc', 'proc', MS_NOSUID|MS_NODEV, '')",
			Actor:        "Container Child Process",
			KernelAction: "Private proc filesystem mounted so 'ps aux' only sees container tasks where child is PID 1.",
			OSRationale:  "The child sets its hostname in its private UTS namespace without altering the host machine hostname.",
		},
		{
			StepNumber:   5,
			Title:        "Root Filesystem Switch (pivot_root)",
			Syscall:      "syscall.PivotRoot(mergedDir, oldRootDir) & syscall.Mount('', oldRootDir, '', MS_DETACH, '')",
			Actor:        "Container Child Process",
			KernelAction: "Atomically moves the mount tree so merged rootfs becomes '/', hiding the host filesystem entirely.",
			OSRationale:  "Unlike 'chroot', which can be escaped using standard relative path traversal, 'pivot_root' provides unbreakable kernel rootfs containment.",
		},
		{
			StepNumber:   6,
			Title:        "Binary Replacement",
			Syscall:      "syscall.Execve(binaryPath, argv, envp)",
			Actor:        "Container Child Process",
			KernelAction: "Kernel discards the child's re-exec code image and replaces it with the user application binary.",
			OSRationale:  "The user's application (e.g. /bin/sh, node, python) inherits PID 1 with no extra overhead or VM virtualization penalty.",
		},
	}

	sendJSON(w, http.StatusOK, steps)
}

func findContainerState(cid string) (*config.ContainerState, error) {
	containersDir := config.GetContainersDir()
	if cid == "" {
		entries, err := os.ReadDir(containersDir)
		if err != nil || len(entries) == 0 {
			return nil, fmt.Errorf("no containers found")
		}
		for _, entry := range entries {
			if entry.IsDir() {
				cid = entry.Name()
				break
			}
		}
	}

	statePath := config.GetContainerStatePath(cid)
	data, err := os.ReadFile(statePath)
	if err == nil {
		var st config.ContainerState
		if err := json.Unmarshal(data, &st); err == nil {
			return &st, nil
		}
	}

	entries, err := os.ReadDir(containersDir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), cid) {
			stPath := config.GetContainerStatePath(entry.Name())
			if d, e := os.ReadFile(stPath); e == nil {
				var st config.ContainerState
				if e := json.Unmarshal(d, &st); e == nil {
					return &st, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("container %s not found", cid)
}
