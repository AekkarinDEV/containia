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

func Run(flags config.RunFlags, imageName string, command []string) error {
	if !image.Exists(imageName) {
		fmt.Printf("Image '%s' not found locally. Pulling...\n", imageName)
		if err := image.Pull(imageName); err != nil {
			return fmt.Errorf("failed to pull image: %w", err)
		}
	}

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

	workDir := flags.WorkingDir
	if workDir == "" && imgMeta != nil {
		workDir = imgMeta.Config.WorkingDir
	}

	var mergedEnv []string
	if imgMeta != nil {
		mergedEnv = append(mergedEnv, imgMeta.Config.Env...)
	}
	mergedEnv = append(mergedEnv, flags.Env...)

	containerID := generateID()
	containerName := flags.Name
	if containerName == "" {
		containerName = "containia-" + containerID[:6]
	}

	containerDir := config.GetContainerDir(containerID)
	if err := os.MkdirAll(containerDir, 0755); err != nil {
		return fmt.Errorf("failed to create container dir: %w", err)
	}

	mergedDir, err := rootfs.SetupOverlay(containerID, imageName)
	if err != nil {
		return fmt.Errorf("overlayfs setup failed: %w", err)
	}

	cg := cgroup.NewManager(containerID)
	if err := cg.Initialize(); err != nil {
		fmt.Printf("Warning: Cgroup init warning: %v\n", err)
	}

	memBytes, _ := cgroup.ParseMemoryString(flags.Memory)
	cpuQuota, _ := cgroup.ParseCPULimit(flags.CPUs)
	_ = cg.ApplyLimits(memBytes, cpuQuota, flags.PidsLimit)

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

	selfPath, err := os.Executable()
	if err != nil {
		selfPath = "/proc/self/exe"
	}

	childArgs := append([]string{"child", containerID}, command...)
	cmd := exec.Command(selfPath, childArgs...)

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

	startupR, startupW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("failed to create startup pipe: %w", err)
	}
	defer startupR.Close()
	defer startupW.Close()
	cmd.ExtraFiles = []*os.File{syncR, startupW}

	var logFile *os.File
	if flags.Detach {
		logPath := config.GetContainerLogPath(containerID)
		logFile, err = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("failed to open container log: %w", err)
		}
		defer logFile.Close()
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	} else {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	cmd.Env = append(containerEnvironment(mergedEnv),
		fmt.Sprintf("CONTAINIA_ID=%s", containerID),
		fmt.Sprintf("CONTAINIA_IMAGE=%s", imageName),
	)

	if err := cmd.Start(); err != nil {
		_ = rootfs.UnmountOverlay(containerID)
		_ = cg.Destroy()
		return fmt.Errorf("failed to spawn container process: %w", err)
	}
	_ = startupW.Close()

	pid := cmd.Process.Pid
	state.PID = pid
	state.Status = config.StatusRunning

	_ = cg.AddProcess(pid)

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

	_, _ = syncW.Write([]byte{1})
	_ = syncW.Close()
	_ = startupR.SetReadDeadline(time.Now().Add(30 * time.Second))
	startupMessage, startupErr := io.ReadAll(startupR)
	if startupErr != nil || len(startupMessage) > 0 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		state.Status = config.StatusExited
		state.ExitCode = 1
		saveState(containerID, state)
		network.CleanupPortForwarding(state.IPAddress, state.Ports)
		network.CleanupContainerNetwork(containerID)
		_ = rootfs.UnmountOverlay(containerID)
		_ = cg.Destroy()
		if startupErr != nil {
			return fmt.Errorf("container startup failed: %w", startupErr)
		}
		return fmt.Errorf("container startup failed: %s", strings.TrimSpace(string(startupMessage)))
	}

	if flags.Detach {
		go func() {
			waitErr := cmd.Wait()
			current, err := loadState(containerID)
			if err == nil && current.Status == config.StatusRunning {
				current.Status = config.StatusExited
				if exitErr, ok := waitErr.(*exec.ExitError); ok {
					current.ExitCode = exitErr.ExitCode()
				}
				saveState(containerID, current)
			}
			if state.IPAddress != "" {
				network.CleanupPortForwarding(state.IPAddress, state.Ports)
				network.CleanupContainerNetwork(containerID)
			}
			_ = cg.Destroy()
		}()
		fmt.Printf("%s\n", containerID)
		return nil
	}

	waitErr := cmd.Wait()
	state.Status = config.StatusExited
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			state.ExitCode = exitErr.ExitCode()
		}
	}
	saveState(containerID, state)

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

func Child(containerID string, userCommand []string) (childErr error) {
	// Successful exec closes this descriptor; setup and exec errors are returned to the launcher.
	startupPipe := os.NewFile(4, "startup_pipe")
	unix.CloseOnExec(4)
	defer func() {
		if childErr != nil {
			_, _ = startupPipe.Write([]byte(childErr.Error()))
		}
		_ = startupPipe.Close()
	}()
	syncPipe := os.NewFile(3, "sync_pipe")
	if syncPipe != nil {
		buf := make([]byte, 1)
		_, _ = syncPipe.Read(buf)
		_ = syncPipe.Close()
	}

	mergedDir := config.GetContainerMergedDir(containerID)

	hostname := containerID
	if len(hostname) > 12 {
		hostname = hostname[:12]
	}
	if err := unix.Sethostname([]byte(hostname)); err != nil {
		return fmt.Errorf("failed to set hostname: %w", err)
	}

	// The launcher may have inherited host locales or other host settings.
	// Container processes use only defaults, image settings, and explicit overrides.
	os.Clearenv()
	for _, entry := range containerEnvironment(nil) {
		key, value, _ := strings.Cut(entry, "=")
		os.Setenv(key, value)
	}
	os.Setenv("CONTAINIA_ID", containerID)

	etcDir := filepath.Join(mergedDir, "etc")
	_ = os.MkdirAll(etcDir, 0755)
	resolvConf := filepath.Join(etcDir, "resolv.conf")
	_ = os.WriteFile(resolvConf, []byte("nameserver 8.8.8.8\nnameserver 1.1.1.1\noptions timeout:2 attempts:3\n"), 0644)
	hostsPath := filepath.Join(etcDir, "hosts")
	if _, err := os.Stat(hostsPath); os.IsNotExist(err) {
		_ = os.WriteFile(hostsPath, []byte(fmt.Sprintf("127.0.0.1 localhost\n::1 localhost\n127.0.0.1 %s\n", hostname)), 0644)
	}

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

	if err := rootfs.SetupDevNodes(mergedDir); err != nil {
		return fmt.Errorf("failed to setup dev nodes: %w", err)
	}

	if err := rootfs.MountEssentialFilesystems(mergedDir); err != nil {
		return fmt.Errorf("failed to mount filesystems: %w", err)
	}

	if err := rootfs.PivotRoot(mergedDir); err != nil {
		return fmt.Errorf("pivot_root failed: %w", err)
	}

	if state != nil && state.WorkingDir != "" {
		if err := os.Chdir(state.WorkingDir); err != nil {
			return fmt.Errorf("failed to enter working directory %s: %w", state.WorkingDir, err)
		}
	}

	if len(userCommand) == 0 {
		return fmt.Errorf("no command specified for container")
	}

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

	return unix.Exec(cmdPath, userCommand, os.Environ())
}

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
	cmd.Env = containerEnvironment(state.Env)

	return cmd.Run()
}

func containerEnvironment(overrides []string) []string {
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"TERM=xterm",
	}
	positions := map[string]int{"PATH": 0, "HOME": 1, "TERM": 2}
	for _, entry := range overrides {
		key, _, valid := strings.Cut(entry, "=")
		if !valid || key == "" {
			continue
		}
		if index, exists := positions[key]; exists {
			env[index] = entry
		} else {
			positions[key] = len(env)
			env = append(env, entry)
		}
	}
	return env
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
