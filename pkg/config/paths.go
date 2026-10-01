package config

import (
	"path/filepath"
	"strings"
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

func GetImageMetaPath(imageName string) string {
	return filepath.Join(GetImagesDir(), imageName, "image.json")
}

func GetLayersDir() string {
	return filepath.Join(BaseDir, "layers")
}

func GetLayerDir(digest string) string {
	clean := strings.ReplaceAll(digest, ":", "_")
	clean = strings.ReplaceAll(clean, "/", "_")
	return filepath.Join(GetLayersDir(), clean)
}

func GetLayerFsDir(digest string) string {
	return filepath.Join(GetLayerDir(digest), "fs")
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
