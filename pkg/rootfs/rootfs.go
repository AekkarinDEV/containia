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

func SetupOverlay(containerID, imageName string) (string, error) {
	cleanImage := strings.ReplaceAll(imageName, ":", "_")
	cleanImage = strings.ReplaceAll(cleanImage, "/", "_")

	var lowerDirOpt string

	metaPath := config.GetImageMetaPath(cleanImage)
	if data, err := os.ReadFile(metaPath); err == nil {
		var meta config.ImageMetadata
		if err := json.Unmarshal(data, &meta); err == nil && len(meta.Layers) > 0 {
			var layerPaths []string
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

	for _, dir := range []string{upperDir, workDir, mergedDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	mountOpts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lowerDirOpt, upperDir, workDir)
	if err := unix.Mount("overlay", mergedDir, "overlay", 0, mountOpts); err != nil {
		return "", fmt.Errorf("failed to mount overlayfs on %s: %w", mergedDir, err)
	}

	return mergedDir, nil
}

func UnmountOverlay(containerID string) error {
	mergedDir := config.GetContainerMergedDir(containerID)
	if _, err := os.Stat(mergedDir); os.IsNotExist(err) {
		return nil
	}

	_ = unix.Unmount(mergedDir, unix.MNT_DETACH)
	return nil
}

func PivotRoot(newRoot string) error {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("failed to make mount namespace private: %w", err)
	}

	if err := unix.Mount(newRoot, newRoot, "bind", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("failed to bind mount newRoot %s: %w", newRoot, err)
	}

	oldRoot := filepath.Join(newRoot, ".old_root")
	if err := os.MkdirAll(oldRoot, 0700); err != nil {
		return fmt.Errorf("failed to create old_root dir: %w", err)
	}

	if err := unix.PivotRoot(newRoot, oldRoot); err != nil {
		return fmt.Errorf("pivot_root failed: %w", err)
	}

	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("chdir / failed: %w", err)
	}

	oldRootPath := "/.old_root"
	if err := unix.Unmount(oldRootPath, unix.MNT_DETACH); err != nil {
		return fmt.Errorf("unmount old root failed: %w", err)
	}

	_ = os.Remove(oldRootPath)
	return nil
}

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

	_ = os.Symlink("/proc/self/fd", filepath.Join(devDir, "fd"))
	_ = os.Symlink("/proc/self/fd/0", filepath.Join(devDir, "stdin"))
	_ = os.Symlink("/proc/self/fd/1", filepath.Join(devDir, "stdout"))
	_ = os.Symlink("/proc/self/fd/2", filepath.Join(devDir, "stderr"))

	return nil
}

func MountEssentialFilesystems() error {
	if err := os.MkdirAll("/proc", 0755); err != nil {
		return err
	}
	if err := unix.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		return fmt.Errorf("failed to mount /proc: %w", err)
	}

	if err := os.MkdirAll("/sys", 0755); err != nil {
		return err
	}
	_ = unix.Mount("sysfs", "/sys", "sysfs", unix.MS_RDONLY, "")

	_ = os.MkdirAll("/dev/shm", 0777)
	_ = unix.Mount("tmpfs", "/dev/shm", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=1777")

	_ = os.MkdirAll("/dev/pts", 0755)
	_ = unix.Mount("devpts", "/dev/pts", "devpts", unix.MS_NOSUID|unix.MS_NOEXEC, "newinstance,ptmxmode=0666,mode=0620")

	return nil
}

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
