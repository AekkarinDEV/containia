package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"containia/pkg/cgroup"
	"containia/pkg/config"
	"containia/pkg/image"
	"containia/pkg/network"
	"containia/pkg/rootfs"
	"golang.org/x/sys/unix"
)

// Run launches a container based on the provided flags, image, and command.
func Run(flags config.RunFlags, imageName string, command []string) error {
	// 1. Ensure image is available locally; pull if missing
	if !image.Exists(imageName) {
		fmt.Printf("Image '%s' not found locally. Pulling...\n", imageName)
		if err := image.Pull(imageName); err != nil {
			return fmt.Errorf("failed to pull image: %w", err)
		}
	}

	// Load image metadata if available to supply defaults
	imgMeta, _ := image.LoadMetadata(imageName)
	if len(command) == 0 && imgMeta != nil {
		if len(imgMeta.Config.Entrypoint) > 0 {
			command = append(command, imgMeta.Config.Entrypoint...)
		}
		if len(imgMeta.Config.Cmd) > 0 {
			command = append(command, imgMeta.Config.Cmd...)
		}
	}
	if len(command) == 0 {
		command = []string{"/bin/sh"}
	}

	// Determine working directory
	workDir := flags.WorkingDir
	if workDir == "" && imgMeta != nil {
		workDir = imgMeta.Config.WorkingDir
	}

	// Merge environment variables: image defaults + user flags
	var mergedEnv []string
	if imgMeta != nil {
		mergedEnv = append(mergedEnv, imgMeta.Config.Env...)
	}
	mergedEnv = append(mergedEnv, flags.Env...)

	// 2. Generate container ID and name
	containerID := generateID()
	containerName := flags.Name
	if containerName == "" {
		containerName = "containia-" + containerID[:6]
	}

	containerDir := config.GetContainerDir(containerID)
	if err := os.MkdirAll(containerDir, 0755); err != nil {
		return fmt.Errorf("failed to create container dir: %w", err)
	}

	// 3. Setup OverlayFS
	mergedDir, err := rootfs.SetupOverlay(containerID, imageName)
	if err != nil {
		return fmt.Errorf("overlayfs setup failed: %w", err)
	}

	// 4. Setup Cgroups v2
	cg := cgroup.NewManager(containerID)
	if err := cg.Initialize(); err != nil {
		fmt.Printf("Warning: Cgroup init warning: %v\n", err)
	}

	memBytes, _ := cgroup.ParseMemoryString(flags.Memory)
	cpuQuota, _ := cgroup.ParseCPULimit(flags.CPUs)
	_ = cg.ApplyLimits(memBytes, cpuQuota, flags.PidsLimit)

	// Save preliminary state
	state := &config.ContainerState{
		ID:          containerID,
		Name:        containerName,
		Image:       imageName,
		Command:     command,
		Status:      config.StatusCreated,
		CreatedAt:   time.Now(),
		MemoryLimit: memBytes,
		CPUShares:   cpuQuota,
		PidsLimit:   flags.PidsLimit,
		RootfsPath:  mergedDir,
		WorkingDir:  workDir,
		Ports:       flags.Ports,
		Volumes:     flags.Volumes,
		Env:         mergedEnv,
	}
	saveState(containerID, state)

	// 5. Fork-Exec via Self-Re-exec pattern with Linux Namespaces
	selfPath, err := os.Executable()
	if err != nil {
		selfPath = "/proc/self/exe"
	}

	childArgs := append([]string{"child", containerID}, command...)
	cmd := exec.Command(selfPath, childArgs...)

	// Configure namespace isolation flags
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUTS |
			syscall.CLONE_NEWPID |
			syscall.CLONE_NEWNS |
			syscall.CLONE_NEWIPC |
			syscall.CLONE_NEWNET,
		Setsid: flags.Detach,
	}

	syncR, syncW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("failed to create sync pipe: %w", err)
	}
	defer syncR.Close()
	defer syncW.Close()

	cmd.ExtraFiles = []*os.File{syncR}

	var logFile *os.File
	if flags.Detach {
		// In detached mode, redirect outputs to container log file
		logPath := config.GetContainerLogPath(containerID)
		logFile, err = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("failed to open container log: %w", err)
		}
		defer logFile.Close()
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	} else {
		// Interactive / Foreground mode
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	// Set container environmental flags
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("CONTAINIA_ID=%s", containerID),
		fmt.Sprintf("CONTAINIA_IMAGE=%s", imageName),
	)
	for _, env := range flags.Env {
		cmd.Env = append(cmd.Env, env)
	}

	// 6. Start the container process
	if err := cmd.Start(); err != nil {
		_ = rootfs.UnmountOverlay(containerID)
		_ = cg.Destroy()
		return fmt.Errorf("failed to spawn container process: %w", err)
	}

	pid := cmd.Process.Pid
	state.PID = pid
	state.Status = config.StatusRunning

	// 7. Attach process to cgroups
	_ = cg.AddProcess(pid)

	// 8. Configure Virtual Networking
	if flags.Network != "none" {
		ipAddr, err := network.SetupContainerNetwork(containerID, pid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: network setup error: %v\n", err)
		} else {
			state.IPAddress = ipAddr
			if len(flags.Ports) > 0 {
				_ = network.SetupPortForwarding(ipAddr, flags.Ports)
			}
			_ = network.SyncAllContainerHosts()
		}
	}
	saveState(containerID, state)

	// 9. Notify child that namespaces, cgroups, and network configuration are ready
	_, _ = syncW.Write([]byte{1})
	_ = syncW.Close()

	if flags.Detach {
		fmt.Printf("%s\n", containerID)
		return nil
	}

	// 9. Wait for process completion in foreground mode
	waitErr := cmd.Wait()
	state.Status = config.StatusExited
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			state.ExitCode = exitErr.ExitCode()
		}
	}
	saveState(containerID, state)

	// Clean up resources
	if len(state.Ports) > 0 && state.IPAddress != "" {
		network.CleanupPortForwarding(state.IPAddress, state.Ports)
	}
	network.CleanupContainerNetwork(containerID)
	_ = cg.Destroy()

	if flags.Remove {
		_ = rootfs.UnmountOverlay(containerID)
		_ = os.RemoveAll(containerDir)
	}

	return waitErr
}

// Child executes inside the newly unshared Linux Namespaces.
// It performs pivot_root, mounts /proc, sets hostname, and replaces itself with the user binary.
func Child(containerID string, userCommand []string) error {
	// 0. Synchronize with host parent: wait until host has fully configured netns and cgroups
	syncPipe := os.NewFile(3, "sync_pipe")
	if syncPipe != nil {
		buf := make([]byte, 1)
		_, _ = syncPipe.Read(buf)
		_ = syncPipe.Close()
	}

	mergedDir := config.GetContainerMergedDir(containerID)

	// 1. Set container hostname (CLONE_NEWUTS)
	hostname := containerID
	if len(hostname) > 12 {
		hostname = hostname[:12]
	}
	if err := unix.Sethostname([]byte(hostname)); err != nil {
		return fmt.Errorf("failed to set hostname: %w", err)
	}

	// 2. Default standard environment variables
	os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	os.Setenv("HOME", "/root")
	os.Setenv("TERM", "xterm")

	// Ensure essential networking files exist in container rootfs before pivot_root
	etcDir := filepath.Join(mergedDir, "etc")
	_ = os.MkdirAll(etcDir, 0755)
	resolvConf := filepath.Join(etcDir, "resolv.conf")
	_ = os.WriteFile(resolvConf, []byte("nameserver 8.8.8.8\nnameserver 1.1.1.1\noptions timeout:2 attempts:3\n"), 0644)
	hostsPath := filepath.Join(etcDir, "hosts")
	if _, err := os.Stat(hostsPath); os.IsNotExist(err) {
		_ = os.WriteFile(hostsPath, []byte(fmt.Sprintf("127.0.0.1 localhost\n::1 localhost\n127.0.0.1 %s\n", hostname)), 0644)
	}

	// 3. Load container state for volume bind mounts & custom envs
	state, _ := loadState(containerID)
	if state != nil {
		if len(state.Volumes) > 0 {
			if err := rootfs.BindMountVolumes(mergedDir, state.Volumes); err != nil {
				return fmt.Errorf("failed to bind mount volumes: %w", err)
			}
		}
		for _, e := range state.Env {
			parts := strings.SplitN(e, "=", 2)
			if len(parts) == 2 {
				os.Setenv(parts[0], parts[1])
			}
		}
	}

	// 4. Setup /dev devices inside mergedDir before pivot_root
	if err := rootfs.SetupDevNodes(mergedDir); err != nil {
		return fmt.Errorf("failed to setup dev nodes: %w", err)
	}

	// 5. Perform pivot_root to isolate filesystem (CLONE_NEWNS)
	if err := rootfs.PivotRoot(mergedDir); err != nil {
		return fmt.Errorf("pivot_root failed: %w", err)
	}

	// 5. Mount essential virtual filesystems (/proc, /sys)
	if err := rootfs.MountEssentialFilesystems(); err != nil {
		return fmt.Errorf("failed to mount filesystems: %w", err)
	}

	// 6. Set working directory
	if state != nil && state.WorkingDir != "" {
		_ = os.Chdir(state.WorkingDir)
	}

	if len(userCommand) == 0 {
		return fmt.Errorf("no command specified for container")
	}

	// 7. Resolve command binary path inside container rootfs
	cmdPath := userCommand[0]
	if !strings.HasPrefix(cmdPath, "/") {
		pathEnv := os.Getenv("PATH")
		searchDirs := append(strings.Split(pathEnv, ":"), "/bin", "/usr/bin", "/sbin", "/usr/sbin")
		for _, dir := range searchDirs {
			if dir == "" {
				continue
			}
			candidate := filepath.Join(dir, cmdPath)
			if _, err := os.Stat(candidate); err == nil {
				cmdPath = candidate
				break
			}
		}
	}

	// 8. Replace child process image with requested user command (syscall.Exec)
	return unix.Exec(cmdPath, userCommand, os.Environ())
}

// PS lists containers matching docker ps output format.
func PS(showAll bool) error {
	containersDir := config.GetContainersDir()
	if _, err := os.Stat(containersDir); os.IsNotExist(err) {
		fmt.Println("CONTAINER ID   IMAGE     COMMAND                  CREATED          STATUS    PORTS     NAMES")
		return nil
	}

	entries, err := os.ReadDir(containersDir)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "CONTAINER ID\tIMAGE\tCOMMAND\tCREATED\tSTATUS\tIP ADDRESS\tNAMES")

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		cid := entry.Name()
		state, err := loadState(cid)
		if err != nil {
			continue
		}

		// Check if process is still running
		if state.Status == config.StatusRunning && state.PID > 0 {
			if err := syscall.Kill(state.PID, 0); err != nil {
				state.Status = config.StatusExited
				saveState(cid, state)
			}
		}

		if !showAll && state.Status != config.StatusRunning {
			continue
		}

		cmdStr := strings.Join(state.Command, " ")
		if len(cmdStr) > 20 {
			cmdStr = cmdStr[:17] + "..."
		}
		cmdStr = fmt.Sprintf("\"%s\"", cmdStr)

		shortID := state.ID
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}

		createdStr := formatDuration(time.Since(state.CreatedAt))

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			shortID,
			state.Image,
			cmdStr,
			createdStr,
			state.Status,
			state.IPAddress,
			state.Name,
		)
	}
	return w.Flush()
}

// Stop sends SIGTERM followed by SIGKILL to a running container.
func Stop(containerID string) error {
	state, err := findContainer(containerID)
	if err != nil {
		return err
	}

	if state.Status != config.StatusRunning || state.PID <= 0 {
		fmt.Printf("Container %s is not running\n", containerID)
		return nil
	}

	fmt.Printf("Stopping container %s (PID %d)...\n", state.ID[:12], state.PID)
	_ = syscall.Kill(state.PID, syscall.SIGTERM)

	// Wait up to 2 seconds for graceful shutdown
	done := make(chan bool, 1)
	go func() {
		for i := 0; i < 20; i++ {
			if err := syscall.Kill(state.PID, 0); err != nil {
				done <- true
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		done <- false
	}()

	graceful := <-done
	if !graceful {
		_ = syscall.Kill(state.PID, syscall.SIGKILL)
	}

	state.Status = config.StatusStopped
	saveState(state.ID, state)

	if len(state.Ports) > 0 && state.IPAddress != "" {
		network.CleanupPortForwarding(state.IPAddress, state.Ports)
	}
	network.CleanupContainerNetwork(state.ID)
	cg := cgroup.NewManager(state.ID)
	_ = cg.Destroy()

	fmt.Printf("%s\n", state.ID[:12])
	return nil
}

// RM removes a stopped container.
func RM(containerID string, force bool) error {
	state, err := findContainer(containerID)
	if err != nil {
		return err
	}

	if state.Status == config.StatusRunning && !force {
		return fmt.Errorf("container %s is running. Stop it first or use --force", state.ID[:12])
	}

	if state.Status == config.StatusRunning && force {
		_ = Stop(state.ID)
	}

	if len(state.Ports) > 0 && state.IPAddress != "" {
		network.CleanupPortForwarding(state.IPAddress, state.Ports)
	}
	_ = rootfs.UnmountOverlay(state.ID)
	_ = os.RemoveAll(config.GetContainerDir(state.ID))
	_ = cgroup.NewManager(state.ID).Destroy()

	fmt.Printf("%s\n", state.ID[:12])
	return nil
}

// Logs outputs container log contents.
func Logs(containerID string) error {
	state, err := findContainer(containerID)
	if err != nil {
		return err
	}

	logPath := config.GetContainerLogPath(state.ID)
	f, err := os.Open(logPath)
	if err != nil {
		return fmt.Errorf("no logs available for %s", containerID)
	}
	defer f.Close()

	_, err = io.Copy(os.Stdout, f)
	return err
}

// Exec executes a new command inside an existing container's namespaces using nsenter.
func Exec(containerID string, command []string) error {
	state, err := findContainer(containerID)
	if err != nil {
		return err
	}

	if state.Status != config.StatusRunning {
		return fmt.Errorf("cannot exec in non-running container %s", containerID)
	}

	pidStr := fmt.Sprintf("%d", state.PID)
	args := append([]string{"-t", pidStr, "-m", "-u", "-i", "-n", "-p"}, command...)

	cmd := exec.Command("nsenter", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), state.Env...)

	return cmd.Run()
}

func generateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func saveState(containerID string, state *config.ContainerState) {
	statePath := config.GetContainerStatePath(containerID)
	data, _ := json.MarshalIndent(state, "", "  ")
	_ = os.WriteFile(statePath, data, 0644)
}

func loadState(containerID string) (*config.ContainerState, error) {
	statePath := config.GetContainerStatePath(containerID)
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, err
	}
	var state config.ContainerState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func findContainer(query string) (*config.ContainerState, error) {
	containersDir := config.GetContainersDir()
	entries, err := os.ReadDir(containersDir)
	if err != nil {
		return nil, fmt.Errorf("no containers found")
	}

	for _, entry := range entries {
		cid := entry.Name()
		if strings.HasPrefix(cid, query) {
			return loadState(cid)
		}
		// Also check by container name
		st, err := loadState(cid)
		if err == nil && st.Name == query {
			return st, nil
		}
	}
	return nil, fmt.Errorf("container not found: %s", query)
}

func formatDuration(d time.Duration) string {
	if d.Seconds() < 60 {
		return fmt.Sprintf("%d seconds ago", int(d.Seconds()))
	}
	if d.Minutes() < 60 {
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	}
	if d.Hours() < 24 {
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}
