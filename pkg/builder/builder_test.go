package builder

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/patternmatcher"
)

func TestCopyPreservesNodeExecutablesAndExcludesBuildCache(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	for _, file := range []string{"package.json", "node_modules/tool/cli.js", ".next/dev/logs/cache", ".next/keep"} {
		path := filepath.Join(src, file)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(src, "node_modules/.bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../tool/cli.js", filepath.Join(src, "node_modules/.bin/tool")); err != nil {
		t.Fatal(err)
	}
	matcher, err := patternmatcher.New([]string{".next", "!.next/keep"})
	if err != nil {
		t.Fatal(err)
	}
	if err := copyPathWithIgnore(src, dst, src, matcher); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"package.json", "node_modules/tool/cli.js", ".next/keep"} {
		if _, err := os.Stat(filepath.Join(dst, path)); err != nil {
			t.Fatalf("expected copied file %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, ".next/dev/logs/cache")); !os.IsNotExist(err) {
		t.Fatalf("expected stale cache to be excluded, got %v", err)
	}
	link, err := os.Readlink(filepath.Join(dst, "node_modules/.bin/tool"))
	if err != nil || link != "../tool/cli.js" {
		t.Fatalf("expected executable symlink to be preserved, got %q, %v", link, err)
	}
}
