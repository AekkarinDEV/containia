package rootfs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"containia/pkg/config"
	"golang.org/x/sys/unix"
)

// SetupOverlay prepares and mounts the OverlayFS for a container.
// It combines the read-only base image layers with a container-specific writable layer.
func SetupOverlay(containerID, imageName string) (string, error) {
	cleanImage := strings.ReplaceAll(imageName, ":", "_")
	cleanImage = strings.ReplaceAll(cleanImage, "/", "_")

	var lowerDirOpt string

	// 1. Check if multi-layer OCI image metadata exists
	metaPath := config.GetImageMetaPath(cleanImage)
	if data, err := os.ReadFile(metaPath); err == nil {
		var meta config.ImageMetadata
		if err := json.Unmarshal(data, &meta); err == nil && len(meta.Layers) > 0 {
			var layerPaths []string
			// In OverlayFS, left is highest (top), right is lowest (bottom).
			// OCI manifest layers are ordered bottom-to-top.
			// Reverse order: layer[N-1] down to layer[0]
			for i := len(meta.Layers) - 1; i >= 0; i-- {
				lfs := config.GetLayerFsDir(meta.Layers[i])
				if _, err := os.Stat(lfs); err == nil {
					layerPaths = append(layerPaths, lfs)
				}
			}
			if len(layerPaths) > 0 {
				lowerDirOpt = strings.Join(layerPaths, ":")
			}
		}
	}

	// 2. Fallback to legacy single rootfs directory
	if lowerDirOpt == "" {
		legacyLower := config.GetImageRootfsDir(cleanImage)
		if _, err := os.Stat(legacyLower); os.IsNotExist(err) {
			return "", fmt.Errorf("base image rootfs not found for %s. Run 'containia pull %s' first", imageName, imageName)
		}
		lowerDirOpt = legacyLower
	}

	upperDir := config.GetContainerUpperDir(containerID)
	workDir := config.GetContainerWorkDir(containerID)
	mergedDir := config.GetContainerMergedDir(containerID)

	// Ensure directories exist
	for _, dir := range []string{upperDir, workDir, mergedDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Mount OverlayFS
	mountOpts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lowerDirOpt, upperDir, workDir)
	if err := unix.Mount("overlay", mergedDir, "overlay", 0, mountOpts); err != nil {
		return "", fmt.Errorf("failed to mount overlayfs on %s: %w", mergedDir, err)
	}

	return mergedDir, nil
}

// UnmountOverlay unmounts the OverlayFS merged directory.
func UnmountOverlay(containerID string) error {
	mergedDir := config.GetContainerMergedDir(containerID)
	if _, err := os.Stat(mergedDir); os.IsNotExist(err) {
		return nil
	}

	// Try detaching mount point
	_ = unix.Unmount(mergedDir, unix.MNT_DETACH)
	return nil
}

// PivotRoot isolates the container's root filesystem using pivot_root syscall.
// This is the industry-standard mechanism used by runc and Docker (replacing insecure chroot).
func PivotRoot(newRoot string) error {
	// 1. Prevent mount changes from propagating to the host mount namespace
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("failed to make mount namespace private: %w", err)
	}

	// 2. Ensure newRoot is a mountpoint by bind-mounting it to itself
	if err := unix.Mount(newRoot, newRoot, "bind", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("failed to bind mount newRoot %s: %w", newRoot, err)
	}

	// 3. Create a temporary directory inside newRoot to hold the old root
	oldRoot := filepath.Join(newRoot, ".old_root")
	if err := os.MkdirAll(oldRoot, 0700); err != nil {
		return fmt.Errorf("failed to create old_root dir: %w", err)
	}

	// 4. Invoke pivot_root syscall: switches root to newRoot and puts old root in oldRoot
	if err := unix.PivotRoot(newRoot, oldRoot); err != nil {
		return fmt.Errorf("pivot_root failed: %w", err)
	}

	// 5. Change current directory to the new root
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("chdir / failed: %w", err)
	}

	// 6. Unmount old root with MNT_DETACH
	oldRootPath := "/.old_root"
	if err := unix.Unmount(oldRootPath, unix.MNT_DETACH); err != nil {
		return fmt.Errorf("unmount old root failed: %w", err)
	}

	// 7. Remove the old root mount point
	_ = os.Remove(oldRootPath)

	return nil
}

// SetupDevNodes binds essential character devices into the container merged /dev directory before pivot_root.
func SetupDevNodes(mergedDir string) error {
	devDir := filepath.Join(mergedDir, "dev")
	if err := os.MkdirAll(devDir, 0755); err != nil {
		return err
	}

	nodes := []string{"null", "zero", "full", "random", "urandom", "tty"}
	for _, n := range nodes {
		hostNode := "/dev/" + n
		targetNode := filepath.Join(devDir, n)

		if _, err := os.Stat(hostNode); err == nil {
			if _, err := os.Stat(targetNode); os.IsNotExist(err) {
				f, err := os.OpenFile(targetNode, os.O_CREATE|os.O_WRONLY, 0666)
				if err == nil {
					f.Close()
				}
			}
			_ = unix.Mount(hostNode, targetNode, "bind", unix.MS_BIND, "")
		}
	}

	// Symlinks
	_ = os.Symlink("/proc/self/fd", filepath.Join(devDir, "fd"))
	_ = os.Symlink("/proc/self/fd/0", filepath.Join(devDir, "stdin"))
	_ = os.Symlink("/proc/self/fd/1", filepath.Join(devDir, "stdout"))
	_ = os.Symlink("/proc/self/fd/2", filepath.Join(devDir, "stderr"))

	return nil
}

// MountEssentialFilesystems mounts /proc, /sys, /dev/shm, and /dev/pts inside the container.
func MountEssentialFilesystems() error {
	// 1. Mount /proc for process isolation
	if err := os.MkdirAll("/proc", 0755); err != nil {
		return err
	}
	if err := unix.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		return fmt.Errorf("failed to mount /proc: %w", err)
	}

	// 2. Mount /sys
	if err := os.MkdirAll("/sys", 0755); err != nil {
		return err
	}
	_ = unix.Mount("sysfs", "/sys", "sysfs", unix.MS_RDONLY, "")

	// 3. Mount /dev/shm (POSIX shared memory)
	_ = os.MkdirAll("/dev/shm", 0777)
	_ = unix.Mount("tmpfs", "/dev/shm", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=1777")

	// 4. Mount /dev/pts
	_ = os.MkdirAll("/dev/pts", 0755)
	_ = unix.Mount("devpts", "/dev/pts", "devpts", unix.MS_NOSUID|unix.MS_NOEXEC, "newinstance,ptmxmode=0666,mode=0620")

	return nil
}

// BindMountVolumes mounts host directories into the container merged directory before pivot_root.
// Format expected: "host_path:container_path"
func BindMountVolumes(mergedDir string, volumes []string) error {
	for _, vol := range volumes {
		parts := strings.Split(vol, ":")
		if len(parts) != 2 {
			continue
		}
		hostPath := parts[0]
		containerRel := strings.TrimPrefix(parts[1], "/")
		targetPath := filepath.Join(mergedDir, containerRel)

		fi, err := os.Stat(hostPath)
		if err != nil {
			return fmt.Errorf("volume source %s not found: %w", hostPath, err)
		}

		if fi.IsDir() {
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return fmt.Errorf("failed to create mount point dir %s: %w", targetPath, err)
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return fmt.Errorf("failed to create parent dir for %s: %w", targetPath, err)
			}
			f, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				return fmt.Errorf("failed to create mount point file %s: %w", targetPath, err)
			}
			f.Close()
		}

		if err := unix.Mount(hostPath, targetPath, "bind", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			return fmt.Errorf("failed to bind mount %s to %s: %w", hostPath, targetPath, err)
		}
	}
	return nil
}

