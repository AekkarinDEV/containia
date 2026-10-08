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

type Info struct {
	Reference  string    `json:"reference"`
	Repository string    `json:"repository"`
	Tag        string    `json:"tag"`
	ID         string    `json:"id"`
	Size       int64     `json:"size"`
	CreatedAt  time.Time `json:"created_at"`
}

var defaultImageURLs = map[string]string{
	"alpine":        "https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/x86_64/alpine-minirootfs-3.21.3-x86_64.tar.gz",
	"alpine:latest": "https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/x86_64/alpine-minirootfs-3.21.3-x86_64.tar.gz",
	"alpine:3.21":   "https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/x86_64/alpine-minirootfs-3.21.3-x86_64.tar.gz",
}

func NormalizeImageName(name string) string {
	name = strings.ReplaceAll(name, ":", "_")
	name = strings.ReplaceAll(name, "/", "_")
	return name
}

// SplitNameTag separates an image reference without mistaking a registry port for a tag.
func SplitNameTag(ref string) (name, tag string) {
	tag = "latest"
	if idx := strings.LastIndex(ref, ":"); idx > strings.LastIndex(ref, "/") {
		return ref[:idx], ref[idx+1:]
	}
	return ref, tag
}

func Exists(imageName string) bool {
	cleanName := NormalizeImageName(imageName)

	metaPath := config.GetImageMetaPath(cleanName)
	if _, err := os.Stat(metaPath); err == nil {
		return true
	}

	rootfs := config.GetImageRootfsDir(cleanName)
	if info, err := os.Stat(rootfs); err == nil && info.IsDir() {
		return true
	}

	return false
}

func LoadMetadata(imageName string) (*config.ImageMetadata, error) {
	cleanName := NormalizeImageName(imageName)
	return loadMetadata(config.GetImageMetaPath(cleanName))
}

func loadMetadata(metaPath string) (*config.ImageMetadata, error) {
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

func SetInternal(imageName string, internal bool) error {
	meta, err := LoadMetadata(imageName)
	if err != nil {
		return err
	}
	if meta.Internal == internal {
		return nil
	}
	meta.Internal = internal
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(config.GetImageMetaPath(NormalizeImageName(imageName)), data, 0644)
}

func Pull(imageRef string) error {
	cleanName := NormalizeImageName(imageRef)
	imageDir := filepath.Join(config.GetImagesDir(), cleanName)

	if Exists(imageRef) {
		if err := SetInternal(imageRef, false); err != nil {
			return fmt.Errorf("failed to expose image '%s': %w", imageRef, err)
		}
		fmt.Printf("Image '%s' already exists locally\n", imageRef)
		return nil
	}

	registry, repository, tag := ParseImageRef(imageRef)
	fmt.Printf("Pulling from %s/%s:%s ...\n", registry, repository, tag)

	client := NewRegistryClient(registry, repository)
	err := client.Authenticate()
	if err == nil {
		manifest, fetchErr := client.FetchManifest(tag)
		if fetchErr == nil && manifest != nil {
			var configDigest string
			var cfgFile *config.ImageConfigFile

			if manifest.Config.Digest != "" {
				configDigest = manifest.Config.Digest
				cfgFile, _ = client.FetchConfigBlob(configDigest)
			}

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

			if err := os.MkdirAll(imageDir, 0755); err != nil {
				return fmt.Errorf("failed to create image dir: %w", err)
			}

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

			name, _ := SplitNameTag(imageRef)
			meta := config.ImageMetadata{
				Name:         name,
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

	resolvConf := filepath.Join(destDir, "etc", "resolv.conf")
	if _, err := os.Stat(resolvConf); os.IsNotExist(err) {
		_ = os.WriteFile(resolvConf, []byte("nameserver 8.8.8.8\nnameserver 1.1.1.1\n"), 0644)
	}

	name, tag := SplitNameTag(imageRef)
	meta := config.ImageMetadata{
		Name:      name,
		Tag:       tag,
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

func List() ([]Info, error) {
	return listImages(config.GetImagesDir())
}

func listImages(imagesDir string) ([]Info, error) {
	return listImagesWithLayers(imagesDir, config.GetLayersDir())
}

func listImagesWithLayers(imagesDir, layersDir string) ([]Info, error) {
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
		meta, err := loadMetadata(filepath.Join(imagesDir, cleanName, "image.json"))
		if err == nil && meta != nil {
			if meta.Internal {
				continue
			}
			id := meta.ID
			if len(id) > 12 {
				id = id[:12]
			}
			tag := meta.Tag
			if tag == "" {
				tag = "latest"
			}
			repository := meta.Name
			// Older pulls stored the tag in both Name and Tag.
			if strings.HasSuffix(repository, ":"+tag) {
				repository = strings.TrimSuffix(repository, ":"+tag)
			}
			reference := repository
			if NormalizeImageName(reference) != cleanName {
				reference += ":" + tag
			}
			size := meta.Size
			if size == 0 && len(meta.Layers) > 0 {
				size, err = layersSize(meta.Layers, layersDir)
				if err != nil {
					return nil, fmt.Errorf("failed to calculate size of image '%s': %w", repository, err)
				}
			}
			results = append(results, Info{
				Reference:  reference,
				Repository: repository,
				Tag:        tag,
				ID:         id,
				Size:       size,
				CreatedAt:  meta.CreatedAt,
			})
			continue
		}

		imgPath := filepath.Join(imagesDir, cleanName)
		dirSize, _ := getDirSize(imgPath)
		info, _ := entry.Info()

		results = append(results, Info{
			Reference:  cleanName,
			Repository: cleanName,
			Tag:        "latest",
			ID:         cleanName[:min(len(cleanName), 12)],
			Size:       dirSize,
			CreatedAt:  info.ModTime(),
		})
	}
	return results, nil
}

func Remove(imageName string) error {
	cleanName := NormalizeImageName(imageName)
	imgDir := filepath.Join(config.GetImagesDir(), cleanName)

	if _, err := os.Stat(imgDir); os.IsNotExist(err) {
		return fmt.Errorf("no such image: %s", imageName)
	}
	containers, err := os.ReadDir(config.GetContainersDir())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range containers {
		if !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(config.GetContainerStatePath(entry.Name()))
		if err != nil {
			return fmt.Errorf("failed to check image usage: %w", err)
		}
		var state config.ContainerState
		if err := json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("failed to check image usage: %w", err)
		}
		if NormalizeImageName(state.Image) == cleanName {
			return fmt.Errorf("image %s is used by container %s (%s, %s); remove the container first (in Web UI: Containers > Remove)", imageName, state.Name, state.ID, state.Status)
		}
	}

	if err := os.RemoveAll(imgDir); err != nil {
		return fmt.Errorf("failed to remove image directory: %w", err)
	}

	usedLayers := make(map[string]bool)
	remainingImages, err := os.ReadDir(config.GetImagesDir())
	if err != nil {
		return err
	}
	for _, entry := range remainingImages {
		if !entry.IsDir() {
			continue
		}
		m, err := LoadMetadata(entry.Name())
		if err == nil && m != nil {
			for _, l := range m.Layers {
				cleanLayer := strings.ReplaceAll(l, ":", "_")
				usedLayers[cleanLayer] = true
			}
		}
	}

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

// LayersSize measures the filesystem data in the image's shared and copied layers.
func LayersSize(layers []string) (int64, error) {
	return layersSize(layers, config.GetLayersDir())
}

func layersSize(layers []string, layersDir string) (int64, error) {
	var size int64
	seen := make(map[string]bool)
	for _, digest := range layers {
		if seen[digest] {
			continue
		}
		seen[digest] = true
		layerFs := filepath.Join(layersDir, filepath.Base(config.GetLayerDir(digest)), "fs")
		layerSize, err := getDirSize(layerFs)
		if err != nil {
			return 0, fmt.Errorf("layer %s: %w", digest, err)
		}
		size += layerSize
	}
	return size, nil
}

func getDirSize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
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
