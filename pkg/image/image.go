package image

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"containia/pkg/config"
)

// Info holds display metadata for 'containia images'.
type Info struct {
	Repository string    `json:"repository"`
	Tag        string    `json:"tag"`
	ID         string    `json:"id"`
	Size       int64     `json:"size"`
	CreatedAt  time.Time `json:"created_at"`
}

// Well-known lightweight rootfs tarball URLs as fallback for Alpine.
var defaultImageURLs = map[string]string{
	"alpine":        "https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/x86_64/alpine-minirootfs-3.21.3-x86_64.tar.gz",
	"alpine:latest": "https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/x86_64/alpine-minirootfs-3.21.3-x86_64.tar.gz",
	"alpine:3.21":   "https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/x86_64/alpine-minirootfs-3.21.3-x86_64.tar.gz",
}

// NormalizeImageName converts an image reference to a safe directory name.
func NormalizeImageName(name string) string {
	name = strings.ReplaceAll(name, ":", "_")
	name = strings.ReplaceAll(name, "/", "_")
	return name
}

// Exists checks if the image exists locally (either as OCI image or legacy rootfs).
func Exists(imageName string) bool {
	cleanName := NormalizeImageName(imageName)

	// Check for OCI metadata file
	metaPath := config.GetImageMetaPath(cleanName)
	if _, err := os.Stat(metaPath); err == nil {
		return true
	}

	// Check for legacy rootfs
	rootfs := config.GetImageRootfsDir(cleanName)
	if info, err := os.Stat(rootfs); err == nil && info.IsDir() {
		return true
	}

	return false
}

// LoadMetadata loads the ImageMetadata descriptor if present.
func LoadMetadata(imageName string) (*config.ImageMetadata, error) {
	cleanName := NormalizeImageName(imageName)
	metaPath := config.GetImageMetaPath(cleanName)

	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, err
	}

	var meta config.ImageMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

// Pull downloads an image using Docker Registry v2 / OCI protocol, with CDN fallback for Alpine.
func Pull(imageRef string) error {
	cleanName := NormalizeImageName(imageRef)
	imageDir := filepath.Join(config.GetImagesDir(), cleanName)

	if Exists(imageRef) {
		fmt.Printf("Image '%s' already exists locally\n", imageRef)
		return nil
	}

	registry, repository, tag := ParseImageRef(imageRef)
	fmt.Printf("Pulling from %s/%s:%s ...\n", registry, repository, tag)

	client := NewRegistryClient(registry, repository)
	err := client.Authenticate()
	if err == nil {
		// Attempt OCI pull
		manifest, fetchErr := client.FetchManifest(tag)
		if fetchErr == nil && manifest != nil {
			var configDigest string
			var cfgFile *config.ImageConfigFile

			if manifest.Config.Digest != "" {
				configDigest = manifest.Config.Digest
				cfgFile, _ = client.FetchConfigBlob(configDigest)
			}

			// Download and extract each layer
			totalLayers := len(manifest.Layers)
			var layerDigests []string
			var totalSize int64

			for i, layer := range manifest.Layers {
				totalSize += layer.Size
				layerDigests = append(layerDigests, layer.Digest)
				if err := client.DownloadAndExtractLayer(layer, i+1, totalLayers); err != nil {
					return fmt.Errorf("failed downloading layer %s: %w", layer.Digest, err)
				}
			}

			// Prepare image destination directory
			if err := os.MkdirAll(imageDir, 0755); err != nil {
				return fmt.Errorf("failed to create image dir: %w", err)
			}

			// Construct image metadata
			imgID := configDigest
			if strings.HasPrefix(imgID, "sha256:") {
				imgID = strings.TrimPrefix(imgID, "sha256:")
			}
			if len(imgID) > 12 {
				imgID = imgID[:12]
			}
			if imgID == "" {
				imgID = cleanName
			}

			cfgDef := config.ImageConfigDef{}
			if cfgFile != nil {
				cfgDef = cfgFile.Config
			}

			meta := config.ImageMetadata{
				Name:         imageRef,
				Tag:          tag,
				ID:           imgID,
				ConfigDigest: configDigest,
				Layers:       layerDigests,
				Config:       cfgDef,
				Size:         totalSize,
				CreatedAt:    time.Now(),
			}

			metaData, err := json.MarshalIndent(meta, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to encode image metadata: %w", err)
			}

			metaPath := config.GetImageMetaPath(cleanName)
			if err := os.WriteFile(metaPath, metaData, 0644); err != nil {
				return fmt.Errorf("failed to write image metadata: %w", err)
			}

			fmt.Printf("Successfully pulled image '%s' (ID: %s, %d layers)\n", imageRef, imgID, totalLayers)
			return nil
		} else {
			fmt.Printf("Registry manifest fetch failed (%v). Checking fallbacks...\n", fetchErr)
		}
	} else {
		fmt.Printf("Registry auth failed (%v). Checking fallbacks...\n", err)
	}

	// Fallback to Alpine CDN if image is alpine
	if strings.Contains(imageRef, "alpine") {
		return pullAlpineTarball(imageRef, cleanName)
	}

	return fmt.Errorf("unable to pull image '%s': registry pull failed and no fallback available", imageRef)
}

func pullAlpineTarball(imageRef, cleanName string) error {
	destDir := config.GetImageRootfsDir(cleanName)
	url := defaultImageURLs["alpine"]

	fmt.Printf("Falling back to Alpine CDN: %s ...\n", url)
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download fallback image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download fallback image: HTTP %d", resp.StatusCode)
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination rootfs directory: %w", err)
	}

	fmt.Println("Extracting fallback rootfs...")
	if err := extractTarGz(resp.Body, destDir); err != nil {
		os.RemoveAll(destDir)
		return fmt.Errorf("failed to extract tarball: %w", err)
	}

	// Ensure DNS works
	resolvConf := filepath.Join(destDir, "etc", "resolv.conf")
	if _, err := os.Stat(resolvConf); os.IsNotExist(err) {
		_ = os.WriteFile(resolvConf, []byte("nameserver 8.8.8.8\nnameserver 1.1.1.1\n"), 0644)
	}

	// Create metadata for fallback image
	meta := config.ImageMetadata{
		Name:      imageRef,
		Tag:       "latest",
		ID:        cleanName[:min(len(cleanName), 12)],
		Config:    config.ImageConfigDef{Cmd: []string{"/bin/sh"}},
		Size:      3 * 1024 * 1024,
		CreatedAt: time.Now(),
	}
	data, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(config.GetImageMetaPath(cleanName), data, 0644)

	fmt.Printf("Successfully pulled fallback image '%s' to %s\n", imageRef, destDir)
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

		cleanName := entry.Name()
		meta, err := LoadMetadata(cleanName)
		if err == nil && meta != nil {
			id := meta.ID
			if len(id) > 12 {
				id = id[:12]
			}
			tag := meta.Tag
			if tag == "" {
				tag = "latest"
			}
			results = append(results, Info{
				Repository: meta.Name,
				Tag:        tag,
				ID:         id,
				Size:       meta.Size,
				CreatedAt:  meta.CreatedAt,
			})
			continue
		}

		// Legacy image directory
		imgPath := filepath.Join(imagesDir, cleanName)
		dirSize, _ := getDirSize(imgPath)
		info, _ := entry.Info()

		results = append(results, Info{
			Repository: cleanName,
			Tag:        "latest",
			ID:         cleanName[:min(len(cleanName), 12)],
			Size:       dirSize,
			CreatedAt:  info.ModTime(),
		})
	}
	return results, nil
}

// Remove deletes an image and cleans up unused layers (garbage collection).
func Remove(imageName string) error {
	cleanName := NormalizeImageName(imageName)
	imgDir := filepath.Join(config.GetImagesDir(), cleanName)

	if _, err := os.Stat(imgDir); os.IsNotExist(err) {
		return fmt.Errorf("no such image: %s", imageName)
	}

	// Delete image metadata & directory
	if err := os.RemoveAll(imgDir); err != nil {
		return fmt.Errorf("failed to remove image directory: %w", err)
	}

	// Perform Layer Garbage Collection: find all in-use layers
	usedLayers := make(map[string]bool)
	allImages, _ := List()
	for _, img := range allImages {
		m, err := LoadMetadata(img.Repository)
		if err == nil && m != nil {
			for _, l := range m.Layers {
				cleanLayer := strings.ReplaceAll(l, ":", "_")
				usedLayers[cleanLayer] = true
			}
		}
	}

	// Delete unreferenced layers
	layersDir := config.GetLayersDir()
	if layerEntries, err := os.ReadDir(layersDir); err == nil {
		for _, entry := range layerEntries {
			if !entry.IsDir() {
				continue
			}
			if !usedLayers[entry.Name()] {
				_ = os.RemoveAll(filepath.Join(layersDir, entry.Name()))
			}
		}
	}

	fmt.Printf("Untagged: %s\n", imageName)
	return nil
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

		if !strings.HasPrefix(filepath.Clean(targetPath), filepath.Clean(targetDir)) {
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
