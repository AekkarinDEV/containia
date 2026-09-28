package image

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"containia/pkg/config"
)

// Info holds metadata about a downloaded rootfs image.
type Info struct {
	Name      string    `json:"name"`
	Tag       string    `json:"tag"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

// Well-known lightweight rootfs tarball URLs for mini-containers.
var defaultImageURLs = map[string]string{
	"alpine":        "https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/x86_64/alpine-minirootfs-3.21.3-x86_64.tar.gz",
	"alpine:latest": "https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/x86_64/alpine-minirootfs-3.21.3-x86_64.tar.gz",
	"alpine:3.21":   "https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/x86_64/alpine-minirootfs-3.21.3-x86_64.tar.gz",
}

// Exists checks if the given image has already been downloaded and unpacked.
func Exists(imageName string) bool {
	cleanName := normalizeName(imageName)
	rootfs := config.GetImageRootfsDir(cleanName)
	info, err := os.Stat(rootfs)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// Pull downloads and unpacks the requested rootfs image into /var/lib/containia/images/<name>/rootfs.
func Pull(imageName string) error {
	cleanName := normalizeName(imageName)
	destDir := config.GetImageRootfsDir(cleanName)

	if Exists(imageName) {
		fmt.Printf("Image '%s' already exists locally at %s\n", imageName, destDir)
		return nil
	}

	url, ok := defaultImageURLs[imageName]
	if !ok {
		// Default to alpine if image contains alpine, otherwise report unknown
		if strings.HasPrefix(imageName, "alpine") {
			url = defaultImageURLs["alpine"]
		} else {
			return fmt.Errorf("unknown image '%s'. Supported out-of-the-box: alpine, alpine:latest", imageName)
		}
	}

	fmt.Printf("Pulling rootfs image '%s' from %s ...\n", imageName, url)

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download image: HTTP %d", resp.StatusCode)
	}

	// Prepare destination directory
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination rootfs directory: %w", err)
	}

	fmt.Println("Extracting rootfs layers...")
	if err := extractTarGz(resp.Body, destDir); err != nil {
		os.RemoveAll(destDir)
		return fmt.Errorf("failed to extract tarball: %w", err)
	}

	// Ensure DNS works in container by generating /etc/resolv.conf if absent
	resolvConf := filepath.Join(destDir, "etc", "resolv.conf")
	if _, err := os.Stat(resolvConf); os.IsNotExist(err) {
		_ = os.WriteFile(resolvConf, []byte("nameserver 8.8.8.8\nnameserver 1.1.1.1\n"), 0644)
	}

	fmt.Printf("Successfully pulled image '%s' to %s\n", imageName, destDir)
	return nil
}

// List returns all downloaded rootfs images in /var/lib/containia/images.
func List() ([]Info, error) {
	imagesDir := config.GetImagesDir()
	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(imagesDir)
	if err != nil {
		return nil, err
	}

	var results []Info
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		imgPath := filepath.Join(imagesDir, entry.Name())
		dirSize, _ := getDirSize(imgPath)
		info, _ := entry.Info()

		results = append(results, Info{
			Name:      entry.Name(),
			Tag:       "latest",
			Size:      dirSize,
			CreatedAt: info.ModTime(),
		})
	}
	return results, nil
}

func normalizeName(name string) string {
	name = strings.ReplaceAll(name, ":", "_")
	name = strings.ReplaceAll(name, "/", "
	_")
	return name
}

func extractTarGz(gzipStream io.Reader, targetDir string) error {
	gzr, err := gzip.NewReader(gzipStream)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		targetPath := filepath.Join(targetDir, header.Name)

		// Security: prevent zip-slip / directory traversal
		if !strings.HasPrefix(filepath.Clean(targetPath), filepath.Clean(targetDir)) {
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return err
			}
			outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return err
			}
			outFile.Close()
		case tar.TypeSymlink:
			_ = os.Remove(targetPath)
			_ = os.Symlink(header.Linkname, targetPath)
		case tar.TypeLink:
			_ = os.Remove(targetPath)
			_ = os.Link(filepath.Join(targetDir, header.Linkname), targetPath)
		}
	}
	return nil
}

func getDirSize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}
