package dashboard

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"containia/pkg/builder"
	"containia/pkg/config"
	"containia/pkg/image"
	"containia/pkg/network"
	"containia/pkg/runtime"
)

//go:embed assets/*
var assetsFS embed.FS

// Server represents the dashboard HTTP server.
type Server struct {
	Port int
}

// NewServer creates a new dashboard server.
func NewServer(port int) *Server {
	if port <= 0 {
		port = 8080
	}
	return &Server{Port: port}
}

// ContainerView represents a container enriched with live cgroup statistics.
type ContainerView struct {
	config.ContainerState
	Stats CgroupLiveStats `json:"stats"`
}

type CgroupLiveStats struct {
	MemoryCurrent int64 `json:"memory_current"`
	MemoryMax     int64 `json:"memory_max"`
	PidsCurrent   int64 `json:"pids_current"`
	PidsMax       int64 `json:"pids_max"`
}

// Start launches the HTTP server and blocks.
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// 1. Static asset handler
	assetsSub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return fmt.Errorf("failed to load embedded assets: %w", err)
	}

	fileServer := http.FileServer(http.FS(assetsSub))
	mux.Handle("/assets/", http.StripPrefix("/assets/", fileServer))

	// Serve index.html at root
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data, err := assetsSub.Open("index.html")
		if err != nil {
			http.Error(w, "index.html not found", http.StatusInternalServerError)
			return
		}
		defer data.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.Copy(w, data)
	})

	// 2. REST API endpoints
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/containers", s.handleContainers)
	mux.HandleFunc("/api/containers/run", s.handleContainerRun)
	mux.HandleFunc("/api/containers/stop", s.handleContainerStop)
	mux.HandleFunc("/api/containers/rm", s.handleContainerRM)
	mux.HandleFunc("/api/containers/logs", s.handleContainerLogs)
	mux.HandleFunc("/api/images", s.handleImages)
	mux.HandleFunc("/api/images/pull", s.handleImagePull)
	mux.HandleFunc("/api/layers", s.handleLayers)
	mux.HandleFunc("/api/build", s.handleBuild)

	addr := fmt.Sprintf("0.0.0.0:%d", s.Port)
	fmt.Printf("\n🚀 Containia Monitor GUI started at http://localhost:%d\n", s.Port)
	fmt.Printf("   Listening on %s (Press Ctrl+C to stop)\n\n", addr)

	return http.ListenAndServe(addr, mux)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]interface{}{
		"version":        "0.2.0-beta",
		"bridge_name":    network.BridgeName,
		"bridge_ip":      network.BridgeGateway,
		"bridge_subnet":  network.BridgeSubnet,
		"cgroup_dir":     config.CgroupDir,
		"containers_dir": config.GetContainersDir(),
		"images_dir":     config.GetImagesDir(),
	}
	sendJSON(w, http.StatusOK, status)
}

func (s *Server) handleContainers(w http.ResponseWriter, r *http.Request) {
	containersDir := config.GetContainersDir()
	entries, err := os.ReadDir(containersDir)
	if err != nil {
		sendJSON(w, http.StatusOK, []ContainerView{})
		return
	}

	var results []ContainerView
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		cid := entry.Name()
		statePath := config.GetContainerStatePath(cid)
		data, err := os.ReadFile(statePath)
		if err != nil {
			continue
		}

		var st config.ContainerState
		if err := json.Unmarshal(data, &st); err != nil {
			continue
		}

		// Verify process state
		if st.Status == config.StatusRunning && st.PID > 0 {
			if err := syscall.Kill(st.PID, 0); err != nil {
				st.Status = config.StatusExited
			}
		}

		// Read live cgroup metrics
		stats := readCgroupStats(cid)

		results = append(results, ContainerView{
			ContainerState: st,
			Stats:          stats,
		})
	}

	sendJSON(w, http.StatusOK, results)
}

func (s *Server) handleContainerRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Image   string   `json:"image"`
		Name    string   `json:"name"`
		Command []string `json:"command"`
		Memory  string   `json:"memory"`
		CPUs    string   `json:"cpus"`
		Env     []string `json:"env"`
		Volumes []string `json:"volumes"`
		Detach  bool     `json:"detach"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	flags := config.RunFlags{
		Name:        req.Name,
		Memory:      req.Memory,
		CPUs:        req.CPUs,
		PidsLimit:   100,
		Interactive: false,
		Tty:         false,
		Detach:      true,
		Remove:      false,
		Network:     "bridge",
		Env:         req.Env,
		Volumes:     req.Volumes,
	}

	go func() {
		_ = runtime.Run(flags, req.Image, req.Command)
	}()

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "launching",
		"message": "Container launch initiated in detached mode",
	})
}

func (s *Server) handleContainerStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := runtime.Stop(req.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sendJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (s *Server) handleContainerRM(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID    string `json:"id"`
		Force bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := runtime.RM(req.ID, req.Force); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sendJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (s *Server) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	cid := r.URL.Query().Get("id")
	if cid == "" {
		http.Error(w, "Missing 'id' parameter", http.StatusBadRequest)
		return
	}

	logPath := config.GetContainerLogPath(cid)
	content, err := os.ReadFile(logPath)
	if err != nil {
		// Also search prefix
		containersDir := config.GetContainersDir()
		if entries, e := os.ReadDir(containersDir); e == nil {
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), cid) {
					content, _ = os.ReadFile(config.GetContainerLogPath(entry.Name()))
					break
				}
			}
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		var req struct {
			Image string `json:"image"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := image.Remove(req.Image); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sendJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		return
	}

	list, err := image.List()
	if err != nil {
		sendJSON(w, http.StatusOK, []interface{}{})
		return
	}

	type ImageView struct {
		image.Info
		Layers []string `json:"layers"`
	}

	var results []ImageView
	for _, img := range list {
		meta, _ := image.LoadMetadata(img.Repository)
		var layers []string
		if meta != nil {
			layers = meta.Layers
		}
		results = append(results, ImageView{
			Info:   img,
			Layers: layers,
		})
	}

	sendJSON(w, http.StatusOK, results)
}

func (s *Server) handleImagePull(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Image string `json:"image"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := image.Pull(req.Image); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	sendJSON(w, http.StatusOK, map[string]string{"status": "success", "image": req.Image})
}

func (s *Server) handleLayers(w http.ResponseWriter, r *http.Request) {
	layersDir := config.GetLayersDir()
	entries, err := os.ReadDir(layersDir)
	if err != nil {
		sendJSON(w, http.StatusOK, []interface{}{})
		return
	}

	type LayerInfo struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	}

	var layers []LayerInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		lPath := filepath.Join(layersDir, entry.Name())
		dirSize, _ := getDirSize(lPath)
		layers = append(layers, LayerInfo{
			Digest: entry.Name(),
			Size:   dirSize,
		})
	}

	sendJSON(w, http.StatusOK, layers)
}

func (s *Server) handleBuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Tag               string `json:"tag"`
		ContextDir        string `json:"context_dir"`
		DockerfileContent string `json:"dockerfile_content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Tag == "" {
		req.Tag = "custom-image:latest"
	}
	if req.ContextDir == "" {
		req.ContextDir = "."
	}

	// If custom dockerfile content provided, write to a temp file
	dockerfilePath := filepath.Join(req.ContextDir, "Dockerfile")
	if req.DockerfileContent != "" {
		tmpDir, err := os.MkdirTemp("", "containia-build-*")
		if err == nil {
			defer os.RemoveAll(tmpDir)
			tmpDocker := filepath.Join(tmpDir, "Dockerfile")
			_ = os.WriteFile(tmpDocker, []byte(req.DockerfileContent), 0644)
			dockerfilePath = tmpDocker
		}
	}

	err := builder.Build(req.ContextDir, dockerfilePath, req.Tag)
	if err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{
			"error": err.Error(),
		})
		return
	}

	sendJSON(w, http.StatusOK, map[string]string{
		"status": "success",
		"tag":    req.Tag,
		"logs":   "Successfully processed all build steps and layers.",
	})
}

func readCgroupStats(cid string) CgroupLiveStats {
	var stats CgroupLiveStats
	cgPath := config.GetContainerCgroupPath(cid)

	// memory.current
	if data, err := os.ReadFile(filepath.Join(cgPath, "memory.current")); err == nil {
		stats.MemoryCurrent, _ = strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	}
	// memory.max
	if data, err := os.ReadFile(filepath.Join(cgPath, "memory.max")); err == nil {
		stats.MemoryMax, _ = strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	}
	// pids.current
	if data, err := os.ReadFile(filepath.Join(cgPath, "pids.current")); err == nil {
		stats.PidsCurrent, _ = strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	}
	// pids.max
	if data, err := os.ReadFile(filepath.Join(cgPath, "pids.max")); err == nil {
		stats.PidsMax, _ = strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	}

	return stats
}

func sendJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
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
