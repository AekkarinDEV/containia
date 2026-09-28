package config

import (
	"path/filepath"
)

const (
	BaseDir   = "/var/lib/containia"
	CgroupDir = "/sys/fs/cgroup/containia"
)

func GetImagesDir() string {
	return filepath.Join(BaseDir, "images")
}

func GetImageRootfsDir(imageName string) string {
	return filepath.Join(GetImagesDir(), imageName, "rootfs")
}

func GetContainersDir() string {
	return filepath.Join(BaseDir, "containers")
}

func GetContainerDir(containerID string) string {
	return filepath.Join(GetContainersDir(), containerID)
}

func GetContainerUpperDir(containerID string) string {
	return filepath.Join(GetContainerDir(containerID), "upper")
}

func GetContainerWorkDir(containerID string) string {
	return filepath.Join(GetContainerDir(containerID), "work")
}

func GetContainerMergedDir(containerID string) string {
	return filepath.Join(GetContainerDir(containerID), "merged")
}

func GetContainerStatePath(containerID string) string {
	return filepath.Join(GetContainerDir(containerID), "state.json")
}

func GetContainerLogPath(containerID string) string {
	return filepath.Join(GetContainerDir(containerID), "output.log")
}

func GetContainerCgroupPath(containerID string) string {
	return filepath.Join(CgroupDir, containerID)
}
