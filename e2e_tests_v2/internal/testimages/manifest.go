package testimages

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/scenario"
)

// Kind records whether an image is an immutable functional candidate or an
// unmodified public image.
type Kind string

const (
	Candidate Kind = "candidate"
	Public    Kind = "public"
)

// Entry is an immutable input recorded in every test artifact manifest.
type Entry struct {
	Image           string `json:"image"`
	SourceImage     string `json:"source_image"`
	Platform        string `json:"platform"`
	Kind            Kind   `json:"kind"`
	AgentDigest     string `json:"agent_digest,omitempty"`
	BootstrapDigest string `json:"bootstrap_digest,omitempty"`
}

// Manifest maps stable case keys to concrete image resources.
type Manifest struct {
	Images map[string]Entry `json:"images"`
}

// Load reads and validates an image manifest.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read image manifest %q: %w", path, err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse image manifest %q: %w", path, err)
	}
	if len(manifest.Images) == 0 {
		return nil, fmt.Errorf("image manifest %q contains no images", path)
	}
	for key, entry := range manifest.Images {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(entry.Image) == "" || strings.TrimSpace(entry.SourceImage) == "" || strings.TrimSpace(entry.Platform) == "" {
			return nil, fmt.Errorf("image entry %q must define image, source_image, and platform", key)
		}
		if strings.Contains(entry.Image, "/images/family/") || strings.Contains(entry.SourceImage, "/images/family/") {
			return nil, fmt.Errorf("image entry %q must use concrete image and source_image resources", key)
		}
		if entry.Kind != Candidate && entry.Kind != Public {
			return nil, fmt.Errorf("image entry %q has unsupported kind %q", key, entry.Kind)
		}
		if entry.Kind == Candidate && (entry.AgentDigest == "" || entry.BootstrapDigest == "") {
			return nil, fmt.Errorf("candidate image %q must define agent_digest and bootstrap_digest", key)
		}
		if entry.Kind == Public && entry.Image != entry.SourceImage {
			return nil, fmt.Errorf("public image %q must equal its source_image; got image %q and source %q", key, entry.Image, entry.SourceImage)
		}
	}
	return &manifest, nil
}

// ForCase returns an entry and enforces that only functional tests use
// candidates and that fresh-image categories use public images.
func (m *Manifest) ForCase(testCase scenario.Case) (Entry, error) {
	entry, ok := m.Images[testCase.ImageKey]
	if !ok {
		return Entry{}, fmt.Errorf("image key %q is not present in the manifest", testCase.ImageKey)
	}
	if entry.Platform != testCase.Platform {
		return Entry{}, fmt.Errorf("image key %q platform = %q, case platform = %q", testCase.ImageKey, entry.Platform, testCase.Platform)
	}
	wantKind := Public
	if testCase.Category == scenario.Functional {
		wantKind = Candidate
	}
	if entry.Kind != wantKind {
		return Entry{}, fmt.Errorf("%s case %q requires a %s image, got %s", testCase.Category, testCase.ID, wantKind, entry.Kind)
	}
	return entry, nil
}
