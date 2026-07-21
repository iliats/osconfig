package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaultsAndRelativePaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`{
  "run_id": "run-1",
  "targets": [{"project":"project-1","zone":"us-central1-a","capacity":2}],
  "image_manifest": "images.json",
  "categories": "functional,compatibility"
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ImageManifest != filepath.Join(dir, "images.json") {
		t.Fatalf("ImageManifest = %q", got.ImageManifest)
	}
	if got.TestTimeout != 60*time.Minute || got.CleanupTimeout != 5*time.Minute {
		t.Fatalf("unexpected defaults: timeout=%v cleanup=%v", got.TestTimeout, got.CleanupTimeout)
	}
	if !got.Categories["functional"] || !got.Categories["compatibility"] || got.Categories["install-upgrade"] {
		t.Fatalf("unexpected categories: %#v", got.Categories)
	}
}
