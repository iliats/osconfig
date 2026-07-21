package testimages

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/scenario"
)

func TestForCaseEnforcesIsolationBoundary(t *testing.T) {
	manifest := &Manifest{Images: map[string]Entry{
		"candidate": {Image: "projects/images/global/images/candidate", Platform: "debian-12", Kind: Candidate, AgentDigest: "sha256:agent", BootstrapDigest: "sha256:bootstrap"},
		"public":    {Image: "projects/debian-cloud/global/images/debian-12", Platform: "debian-12", Kind: Public},
	}}

	tests := []struct {
		name     string
		category scenario.Category
		key      string
		wantErr  bool
	}{
		{name: "functional candidate", category: scenario.Functional, key: "candidate"},
		{name: "functional public rejected", category: scenario.Functional, key: "public", wantErr: true},
		{name: "compatibility public", category: scenario.Compatibility, key: "public"},
		{name: "install candidate rejected", category: scenario.InstallUpgrade, key: "candidate", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := manifest.ForCase(scenario.Case{ID: tc.name, Category: tc.category, Platform: "debian-12", ImageKey: tc.key})
			if (err != nil) != tc.wantErr {
				t.Fatalf("ForCase() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestLoadRejectsPublicImageDifferentFromSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "images.json")
	data := []byte(`{"images":{"public":{"image":"projects/p/global/images/image-a","source_image":"projects/p/global/images/image-b","platform":"debian-12","kind":"public"}}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() succeeded for a public image different from source_image")
	}
}

func TestLoadRejectsImageFamily(t *testing.T) {
	path := filepath.Join(t.TempDir(), "images.json")
	data := []byte(`{"images":{"public":{"image":"projects/p/global/images/family/debian-12","source_image":"projects/p/global/images/family/debian-12","platform":"debian-12","kind":"public"}}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() succeeded for an image family")
	}
}
