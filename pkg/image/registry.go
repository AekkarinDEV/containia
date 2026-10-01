package image

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"containia/pkg/config"
)

// OCI/Docker Manifest schemas
type ManifestList struct {
	SchemaVersion int                `json:"schemaVersion"`
	MediaType     string             `json:"mediaType"`
	Manifests     []PlatformManifest `json:"manifests"`
}

type PlatformManifest struct {
	MediaType string       `json:"mediaType"`
	Size      int64        `json:"size"`
	Digest    string       `json:"digest"`
	Platform  ArchPlatform `json:"platform"`
}

type ArchPlatform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Variant      string `json:"variant,omitempty"`
}

type SingleManifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Config        Descriptor      `json:"config"`
	Layers        []Descriptor    `json:"layers"`
}

type Descriptor struct {
	MediaType string `json:"mediaType"`
	Size      int64  `json:"size"`
	Digest    string `json:"digest"`
}

// RegistryClient handles OCI / Docker Registry v2 communication.
type RegistryClient struct {
	client     *http.Client
	registry   string
	repository string
	token      string
}

// NewRegistryClient creates a client targeting a given registry and repo.
func NewRegistryClient(registry, repository string) *RegistryClient {
	return &RegistryClient{
		client: &http.Client{
			Timeout: 10 * time.Minute,
		},
		registry:   registry,
		repository: repository,
	}
}

// ParseImageRef parses "postgres:18", "alpine", "ghcr.io/org/repo:tag" etc.
func ParseImageRef(imageRef string) (registry, repository, tag string) {
	tag = "latest"
	registry = "registry-1.docker.io"

	ref := imageRef
	// Extract tag if present
	if idx := strings.LastIndex(ref, ":"); idx != -1 && !strings.Contains(ref[idx:], "/") {
		tag = ref[idx+1:]
		ref = ref[:idx]
	}

	parts := strings.Split(ref, "/")
	if len(parts) == 1 {
		// e.g. "alpine" -> "library/alpine"
		repository = "library/" + parts[0]
	} else if len(parts) == 2 {
		// e.g. "library/alpine" or "user/app"
		if strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":") {
			registry = parts[0]
			repository = parts[1]
		} else {
			repository = parts[0] + "/" + parts[1]
		}
	} else {
		// 3 or more parts: e.g. "quay.io/org/repo"
		if strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":") {
			registry = parts[0]
			repository = strings.Join(parts[1:], "/")
		} else {
			repository = strings.Join(parts, "/")
		}
	}

	return registry, repository, tag
}

// Authenticate obtains a Bearer token if required by the registry.
func (c *RegistryClient) Authenticate() error {
	// Ping endpoint to inspect auth challenge
	pingURL := fmt.Sprintf("https://%s/v2/", c.registry)
	req, err := http.NewRequest(http.MethodGet, pingURL, nil)
	if err != nil {
		return err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil // No auth required
	}

	if resp.StatusCode == http.StatusUnauthorized {
		authHeader := resp.Header.Get("Www-Authenticate")
		if authHeader == "" {
			return fmt.Errorf("registry returned 401 without Www-Authenticate header")
		}

		realm, service := parseWwwAuthenticate(authHeader)
		if realm == "" {
			return fmt.Errorf("unable to parse realm from Www-Authenticate: %s", authHeader)
		}

		// Request anonymous token for pull scope
		tokenURL, err := url.Parse(realm)
		if err != nil {
			return err
		}

		q := tokenURL.Query()
		if service != "" {
			q.Set("service", service)
		}
		q.Set("scope", fmt.Sprintf("repository:%s:pull", c.repository))
		tokenURL.RawQuery = q.Encode()

		tokenResp, err := c.client.Get(tokenURL.String())
		if err != nil {
			return fmt.Errorf("failed to fetch auth token: %w", err)
		}
		defer tokenResp.Body.Close()

		if tokenResp.StatusCode != http.StatusOK {
			return fmt.Errorf("auth token request failed with HTTP %d", tokenResp.StatusCode)
		}

		var tokenData struct {
			Token       string `json:"token"`
			AccessToken string `json:"access_token"`
		}
		if err := json.NewDecoder(tokenResp.Body).Decode(&tokenData); err != nil {
			return err
		}

		if tokenData.Token != "" {
			c.token = tokenData.Token
		} else {
			c.token = tokenData.AccessToken
		}
	}

	return nil
}

func parseWwwAuthenticate(header string) (realm, service string) {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) < 2 {
		return "", ""
	}

	kvs := strings.Split(parts[1], ",")
	for _, kv := range kvs {
		kv = strings.TrimSpace(kv)
		pair := strings.SplitN(kv, "=", 2)
		if len(pair) == 2 {
			k := strings.TrimSpace(pair[0])
			v := strings.Trim(strings.TrimSpace(pair[1]), "\"")
			if k == "realm" {
				realm = v
			} else if k == "service" {
				service = v
			}
		}
	}
	return realm, service
}

func (c *RegistryClient) doWithRetry(req *http.Request) (*http.Response, error) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}

	// If token expired, refresh and retry once
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		if authErr := c.Authenticate(); authErr == nil {
			req.Header.Set("Authorization", "Bearer "+c.token)
			return c.client.Do(req)
		}
	}

	return resp, nil
}

// FetchManifest retrieves the image manifest, resolving multi-arch lists to linux/amd64.
func (c *RegistryClient) FetchManifest(reference string) (*SingleManifest, error) {
	manifestURL := fmt.Sprintf("https://%s/v2/%s/manifests/%s", c.registry, c.repository, reference)
	req, err := http.NewRequest(http.MethodGet, manifestURL, nil)
	if err != nil {
		return nil, err
	}

	// Request both manifest lists and single image manifests (OCI & Docker v2)
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
	}, ", "))

	resp, err := c.doWithRetry(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch manifest: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	contentType := resp.Header.Get("Content-Type")

	// Check if this is a Manifest List / OCI Index
	if strings.Contains(contentType, "manifest.list") || strings.Contains(contentType, "image.index") {
		var list ManifestList
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, fmt.Errorf("failed to parse manifest list: %w", err)
		}

		var targetDigest string
		for _, m := range list.Manifests {
			if m.Platform.OS == "linux" && m.Platform.Architecture == "amd64" {
				targetDigest = m.Digest
				break
			}
		}

		if targetDigest == "" {
			return nil, fmt.Errorf("no linux/amd64 platform found in manifest list")
		}

		// Recursively fetch the specific linux/amd64 manifest by digest
		return c.FetchManifest(targetDigest)
	}

	// Single manifest
	var manifest SingleManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse single manifest: %w", err)
	}

	return &manifest, nil
}

// FetchConfigBlob downloads and parses the image configuration JSON.
func (c *RegistryClient) FetchConfigBlob(digest string) (*config.ImageConfigFile, error) {
	blobURL := fmt.Sprintf("https://%s/v2/%s/blobs/%s", c.registry, c.repository, digest)
	req, err := http.NewRequest(http.MethodGet, blobURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.doWithRetry(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download config blob: HTTP %d", resp.StatusCode)
	}

	var cfg config.ImageConfigFile
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to decode image config: %w", err)
	}

	return &cfg, nil
}

// DownloadAndExtractLayer downloads a layer blob and unpacks it to /var/lib/containia/layers/<digest>/fs.
func (c *RegistryClient) DownloadAndExtractLayer(desc Descriptor, index, total int) error {
	layerDir := config.GetLayerDir(desc.Digest)
	fsDir := config.GetLayerFsDir(desc.Digest)
	markerFile := filepath.Join(layerDir, ".extracted")

	shortDigest := desc.Digest
	if len(shortDigest) > 19 {
		shortDigest = shortDigest[:19]
	}

	if _, err := os.Stat(markerFile); err == nil {
		fmt.Printf("Layer [%d/%d] %s: Already cached\n", index, total, shortDigest)
		return nil
	}

	fmt.Printf("Layer [%d/%d] %s: Downloading (%0.2f MB)...\n", index, total, shortDigest, float64(desc.Size)/(1024*1024))

	blobURL := fmt.Sprintf("https://%s/v2/%s/blobs/%s", c.registry, c.repository, desc.Digest)
	req, err := http.NewRequest(http.MethodGet, blobURL, nil)
	if err != nil {
		return err
	}

	resp, err := c.doWithRetry(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to fetch layer blob: HTTP %d", resp.StatusCode)
	}

	// Prepare layer directory
	if err := os.MkdirAll(fsDir, 0755); err != nil {
		return fmt.Errorf("failed to create layer directory: %w", err)
	}

	fmt.Printf("Layer [%d/%d] %s: Extracting...\n", index, total, shortDigest)

	var reader io.Reader = resp.Body
	// If layer is gzipped (standard for OCI and Docker)
	if strings.Contains(desc.MediaType, "gzip") || strings.HasSuffix(desc.MediaType, "+gzip") || strings.Contains(desc.Digest, "sha256") {
		gzr, err := gzip.NewReader(resp.Body)
		if err == nil {
			defer gzr.Close()
			reader = gzr
		}
	}

	if err := extractLayerTar(reader, fsDir); err != nil {
		_ = os.RemoveAll(layerDir)
		return fmt.Errorf("failed to extract layer: %w", err)
	}

	// Mark layer as successfully extracted
	_ = os.WriteFile(markerFile, []byte(desc.Digest), 0644)
	return nil
}

func extractLayerTar(stream io.Reader, targetDir string) error {
	tr := tar.NewReader(stream)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		targetPath := filepath.Join(targetDir, header.Name)

		// Security: prevent zip-slip / traversal attacks
		if !strings.HasPrefix(filepath.Clean(targetPath), filepath.Clean(targetDir)) {
			continue
		}

		// Handle OCI Whiteout files (.wh.<filename>)
		baseName := filepath.Base(header.Name)
		if strings.HasPrefix(baseName, ".wh.") {
			if baseName == ".wh..wh..opq" {
				// Opaque directory marker: hides contents of lower layers in this directory
				continue
			}
			// Normal whiteout: marks file as deleted
			deletedFileName := strings.TrimPrefix(baseName, ".wh.")
			realTarget := filepath.Join(filepath.Dir(targetPath), deletedFileName)
			_ = os.RemoveAll(realTarget)
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
