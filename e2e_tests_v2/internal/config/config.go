package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/scenario"
)

const envConfigPath = "E2E_CONFIG"

var sha256Pattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

// Target is an isolated project/zone capacity allocation available to one test
// process. Separate CI shards should receive disjoint targets or projects.
type Target struct {
	Project  string `json:"project"`
	Zone     string `json:"zone"`
	Capacity int    `json:"capacity"`
}

// Config contains run-level settings. Secrets are deliberately excluded; GCP
// clients use Application Default Credentials.
type Config struct {
	RunID              string
	Targets            []Target
	ImageManifest      string
	ArtifactDir        string
	Categories         map[scenario.Category]bool
	TestTimeout        time.Duration
	PollInterval       time.Duration
	CleanupTimeout     time.Duration
	MachineType        string
	Network            string
	ServiceAccount     string
	EnableExternalIP   bool
	DEBPackageURL      string
	DEBPackageSHA256   string
	DEBExpectedVersion string
	RPMPackageURL      string
	RPMPackageSHA256   string
	RPMExpectedVersion string
	RPMUpgradeFrom     string
	OSConfigEndpoint   string
}

type fileConfig struct {
	RunID              string   `json:"run_id"`
	Targets            []Target `json:"targets"`
	ImageManifest      string   `json:"image_manifest"`
	ArtifactDir        string   `json:"artifact_dir"`
	Categories         string   `json:"categories"`
	TestTimeout        string   `json:"test_timeout"`
	PollInterval       string   `json:"poll_interval"`
	CleanupTimeout     string   `json:"cleanup_timeout"`
	MachineType        string   `json:"machine_type"`
	Network            string   `json:"network"`
	ServiceAccount     string   `json:"service_account"`
	EnableExternalIP   *bool    `json:"enable_external_ip"`
	DEBPackageURL      string   `json:"deb_package_url"`
	DEBPackageSHA256   string   `json:"deb_package_sha256"`
	DEBExpectedVersion string   `json:"deb_expected_version"`
	RPMPackageURL      string   `json:"rpm_package_url"`
	RPMPackageSHA256   string   `json:"rpm_package_sha256"`
	RPMExpectedVersion string   `json:"rpm_expected_version"`
	RPMUpgradeFrom     string   `json:"rpm_upgrade_from_version"`
	OSConfigEndpoint   string   `json:"osconfig_endpoint"`
}

// LoadFromEnvironment loads the path in E2E_CONFIG.
func LoadFromEnvironment() (Config, error) {
	path := strings.TrimSpace(os.Getenv(envConfigPath))
	if path == "" {
		return Config{}, fmt.Errorf("%s must point to a JSON run configuration", envConfigPath)
	}
	return Load(path)
}

// Load reads and validates a JSON run configuration.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	var raw fileConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}

	baseDir := filepath.Dir(path)
	if raw.ImageManifest != "" && !filepath.IsAbs(raw.ImageManifest) {
		raw.ImageManifest = filepath.Join(baseDir, raw.ImageManifest)
	}
	if raw.ArtifactDir != "" && !filepath.IsAbs(raw.ArtifactDir) {
		raw.ArtifactDir = filepath.Join(baseDir, raw.ArtifactDir)
	}

	categories, err := scenario.ParseCategories(raw.Categories)
	if err != nil {
		return Config{}, err
	}
	testTimeout, err := durationOrDefault(raw.TestTimeout, 60*time.Minute)
	if err != nil {
		return Config{}, fmt.Errorf("test_timeout: %w", err)
	}
	pollInterval, err := durationOrDefault(raw.PollInterval, 10*time.Second)
	if err != nil {
		return Config{}, fmt.Errorf("poll_interval: %w", err)
	}
	cleanupTimeout, err := durationOrDefault(raw.CleanupTimeout, 5*time.Minute)
	if err != nil {
		return Config{}, fmt.Errorf("cleanup_timeout: %w", err)
	}

	config := Config{
		RunID:              strings.TrimSpace(raw.RunID),
		Targets:            raw.Targets,
		ImageManifest:      raw.ImageManifest,
		ArtifactDir:        raw.ArtifactDir,
		Categories:         categories,
		TestTimeout:        testTimeout,
		PollInterval:       pollInterval,
		CleanupTimeout:     cleanupTimeout,
		MachineType:        valueOrDefault(raw.MachineType, "e2-standard-2"),
		Network:            valueOrDefault(raw.Network, "global/networks/default"),
		ServiceAccount:     valueOrDefault(raw.ServiceAccount, "default"),
		EnableExternalIP:   raw.EnableExternalIP == nil || *raw.EnableExternalIP,
		DEBPackageURL:      raw.DEBPackageURL,
		DEBPackageSHA256:   raw.DEBPackageSHA256,
		DEBExpectedVersion: strings.TrimSpace(raw.DEBExpectedVersion),
		RPMPackageURL:      raw.RPMPackageURL,
		RPMPackageSHA256:   raw.RPMPackageSHA256,
		RPMExpectedVersion: strings.TrimSpace(raw.RPMExpectedVersion),
		RPMUpgradeFrom:     strings.TrimSpace(raw.RPMUpgradeFrom),
		OSConfigEndpoint:   raw.OSConfigEndpoint,
	}
	if config.RunID == "" {
		config.RunID = time.Now().UTC().Format("20060102t150405z")
	}
	if config.ArtifactDir == "" {
		config.ArtifactDir = filepath.Join(baseDir, "artifacts")
	}
	if err := validate(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func validate(config Config) error {
	if len(config.Targets) == 0 {
		return fmt.Errorf("at least one target is required")
	}
	for i, target := range config.Targets {
		if target.Project == "" || target.Zone == "" || target.Capacity <= 0 {
			return fmt.Errorf("target %d must define project, zone, and positive capacity", i)
		}
	}
	if strings.TrimSpace(config.ImageManifest) == "" {
		return fmt.Errorf("image_manifest is required")
	}
	if config.PollInterval <= 0 || config.TestTimeout <= 0 || config.CleanupTimeout <= 0 {
		return fmt.Errorf("timeouts and poll interval must be positive")
	}
	if err := validatePackage("deb", config.DEBPackageURL, config.DEBPackageSHA256); err != nil {
		return err
	}
	if err := validatePackage("rpm", config.RPMPackageURL, config.RPMPackageSHA256); err != nil {
		return err
	}
	return nil
}

func validatePackage(kind, rawURL, digest string) error {
	if rawURL == "" && digest == "" {
		return nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("%s_package_url must be an HTTPS URL", kind)
	}
	if !sha256Pattern.MatchString(digest) {
		return fmt.Errorf("%s_package_sha256 must contain 64 hexadecimal characters", kind)
	}
	return nil
}

func durationOrDefault(value string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	return time.ParseDuration(value)
}

func valueOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
