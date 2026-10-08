package builder

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"containia/pkg/config"
	"containia/pkg/image"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
)

func Build(contextDir, dockerfilePath, tag string) error {
	if dockerfilePath == "" {
		dockerfilePath = filepath.Join(contextDir, "Dockerfile")
	}

	content, err := os.ReadFile(dockerfilePath)
	if err != nil {
		return fmt.Errorf("failed to read Dockerfile at %s: %w", dockerfilePath, err)
	}

	lines := strings.Split(string(content), "\n")
	var meta config.ImageMetadata
	meta.CreatedAt = time.Now()
	meta.Name, meta.Tag = image.SplitNameTag(tag)
	meta.Config.ExposedPorts = make(map[string]interface{})

	step := 1
	var layers []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, " ", 2)
		verb := strings.ToUpper(parts[0])
		arg := ""
		if len(parts) > 1 {
			arg = strings.TrimSpace(parts[1])
		}

		fmt.Printf("Step %d: %s %s\n", step, verb, arg)
		step++

		switch verb {
		case "FROM":
			baseImage := arg
			if !image.Exists(baseImage) {
				fmt.Printf("Pulling base image '%s'...\n", baseImage)
				if err := image.Pull(baseImage); err != nil {
					return fmt.Errorf("failed to pull base image '%s': %w", baseImage, err)
				}
			}
			if err := image.SetInternal(baseImage, true); err != nil {
				return fmt.Errorf("failed to mark base image '%s' as internal: %w", baseImage, err)
			}
			baseMeta, err := image.LoadMetadata(baseImage)
			if err == nil && baseMeta != nil {
				layers = append(layers, baseMeta.Layers...)
				meta.Config = baseMeta.Config
				if meta.Config.ExposedPorts == nil {
					meta.Config.ExposedPorts = make(map[string]interface{})
				}
			}

		case "ENV":
			if strings.Contains(arg, "=") {
				envPairs := strings.Fields(arg)
				for _, pair := range envPairs {
					meta.Config.Env = append(meta.Config.Env, pair)
				}
			} else {
				envParts := strings.SplitN(arg, " ", 2)
				if len(envParts) == 2 {
					meta.Config.Env = append(meta.Config.Env, fmt.Sprintf("%s=%s", envParts[0], strings.TrimSpace(envParts[1])))
				}
			}

		case "WORKDIR":
			meta.Config.WorkingDir = arg

		case "EXPOSE":
			port := strings.TrimSpace(arg)
			if !strings.Contains(port, "/") {
				port = port + "/tcp"
			}
			meta.Config.ExposedPorts[port] = struct{}{}

		case "CMD":
			meta.Config.Cmd = parseArrayOrString(arg)

		case "ENTRYPOINT":
			meta.Config.Entrypoint = parseArrayOrString(arg)

		case "COPY":
			copyParts := strings.Fields(arg)
			if len(copyParts) >= 2 {
				src := copyParts[0]
				dest := copyParts[1]
				layerDigest, err := createCopyLayer(contextDir, src, dest)
				if err != nil {
					return fmt.Errorf("COPY failed: %w", err)
				}
				layers = append(layers, layerDigest)
			}
		}
	}

	meta.Layers = layers
	meta.Size, err = image.LayersSize(layers)
	if err != nil {
		return fmt.Errorf("failed to calculate image size: %w", err)
	}

	hash := sha256.New()
	hash.Write([]byte(strings.Join(meta.Layers, ":")))
	hash.Write([]byte(fmt.Sprintf("%v", meta.Config)))
	sum := hex.EncodeToString(hash.Sum(nil))
	meta.ID = sum[:12]
	meta.ConfigDigest = "sha256:" + sum

	cleanName := image.NormalizeImageName(tag)
	imageDir := filepath.Join(config.GetImagesDir(), cleanName)
	if err := os.MkdirAll(imageDir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}

	metaPath := config.GetImageMetaPath(cleanName)
	if err := os.WriteFile(metaPath, data, 0644); err != nil {
		return err
	}

	fmt.Printf("Successfully built image '%s' (Image ID: %s)\n", tag, meta.ID)
	return nil
}

func parseArrayOrString(val string) []string {
	var result []string
	if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
		if err := json.Unmarshal([]byte(val), &result); err == nil {
			return result
		}
	}
	return strings.Fields(val)
}

func createCopyLayer(contextDir, src, dest string) (string, error) {
	randBytes := make([]byte, 16)
	_, _ = rand.Read(randBytes)
	digest := "sha256:" + hex.EncodeToString(randBytes)

	layerFs := config.GetLayerFsDir(digest)
	targetDest := filepath.Join(layerFs, strings.TrimPrefix(dest, "/"))
	if err := os.MkdirAll(targetDest, 0755); err != nil {
		return "", err
	}

	srcPath := filepath.Join(contextDir, src)
	var matcher *patternmatcher.PatternMatcher
	ignorePath := filepath.Join(contextDir, ".dockerignore")
	if ignore, err := os.Open(ignorePath); err == nil {
		patterns, readErr := ignorefile.ReadAll(ignore)
		_ = ignore.Close()
		if readErr != nil {
			return "", readErr
		}
		matcher, err = patternmatcher.New(patterns)
		if err != nil {
			return "", fmt.Errorf("invalid .dockerignore: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := copyPathWithIgnore(srcPath, targetDest, contextDir, matcher); err != nil {
		return "", err
	}

	_ = os.WriteFile(filepath.Join(config.GetLayerDir(digest), ".extracted"), []byte(digest), 0644)
	return digest, nil
}

func copyPath(src, dst string) error {
	return copyPathWithIgnore(src, dst, filepath.Dir(src), nil)
}

func copyPathWithIgnore(src, dst, contextDir string, matcher *patternmatcher.PatternMatcher) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}

	if info.IsDir() {
		return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if matcher != nil {
				contextRel, err := filepath.Rel(contextDir, path)
				if err != nil {
					return err
				}
				ignored, err := matcher.MatchesOrParentMatches(contextRel)
				if err != nil {
					return err
				}
				if ignored {
					if info.IsDir() && !matcher.Exclusions() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			rel, _ := filepath.Rel(src, path)
			target := filepath.Join(dst, rel)
			if info.IsDir() {
				return os.MkdirAll(target, 0755)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return copySymlink(path, target)
			}
			return copyFile(path, target)
		})
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return copySymlink(src, dst)
	}
	return copyFile(src, dst)
}

func copySymlink(src, dst string) error {
	link, err := os.Readlink(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	return os.Symlink(link, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	mode := info.Mode()
	if strings.Contains(src, "bin") || mode&0111 != 0 {
		mode = mode | 0755
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}

	return os.Chmod(dst, mode)
}
