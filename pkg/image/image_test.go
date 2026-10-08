package image

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"containia/pkg/config"
)

func TestListHidesInternalBaseImage(t *testing.T) {
	imagesDir := t.TempDir()
	for _, tc := range []struct {
		dir  string
		meta config.ImageMetadata
	}{
		{"oven_bun_latest", config.ImageMetadata{Name: "oven/bun:latest", Tag: "latest", Internal: true}},
		{"nextjs", config.ImageMetadata{Name: "nextjs", Tag: "latest"}},
	} {
		dir := filepath.Join(imagesDir, tc.dir)
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(tc.meta)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "image.json"), data, 0644); err != nil {
			t.Fatal(err)
		}
	}

	images, err := listImages(imagesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].Repository != "nextjs" || images[0].Tag != "latest" || images[0].Reference != "nextjs" {
		t.Fatalf("expected only nextjs:latest, got %+v", images)
	}
}

func TestListHandlesLegacyTaggedRepository(t *testing.T) {
	imagesDir := t.TempDir()
	dir := filepath.Join(imagesDir, "postgres_18")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(config.ImageMetadata{Name: "postgres:18", Tag: "18"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "image.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	images, err := listImages(imagesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].Repository != "postgres" || images[0].Tag != "18" || images[0].Reference != "postgres:18" {
		t.Fatalf("expected postgres:18, got %+v", images)
	}
}

func TestListCalculatesMissingSizeFromSharedAndCopyLayers(t *testing.T) {
	imagesDir := t.TempDir()
	layersDir := t.TempDir()
	for _, layer := range []struct {
		digest string
		path   string
		data   string
	}{
		{"sha256:base", "usr/local/bin/node", "runtime"},
		{"sha256:app", "app/package.json", "app data"},
	} {
		path := filepath.Join(layersDir, filepath.Base(config.GetLayerDir(layer.digest)), "fs", layer.path)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(layer.data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	imageDir := filepath.Join(imagesDir, "nextjs")
	if err := os.Mkdir(imageDir, 0755); err != nil {
		t.Fatal(err)
	}
	meta := config.ImageMetadata{
		Name: "nextjs", Tag: "latest",
		Layers: []string{"sha256:base", "sha256:app", "sha256:base"},
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imageDir, "image.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	images, err := listImagesWithLayers(imagesDir, layersDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].Size != 15 {
		t.Fatalf("expected image size to include base and app data once, got %+v", images)
	}
}

func TestLayersSizeReportsMissingLayer(t *testing.T) {
	if _, err := layersSize([]string{"sha256:missing"}, t.TempDir()); err == nil {
		t.Fatal("expected missing layer to produce an error instead of reporting zero bytes")
	}
}
