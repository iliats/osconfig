package testenv

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/artifacts"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/config"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/gcp"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/poll"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/scenario"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/scheduler"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/testimages"
)

var unsafeResourceName = regexp.MustCompile(`[^a-z0-9-]+`)

// Suite contains process-scoped, concurrency-safe dependencies.
type Suite struct {
	Config    config.Config
	Images    *testimages.Manifest
	Scheduler *scheduler.Scheduler
	Clients   *gcp.Clients
}

// NewSuite validates immutable inputs before any test starts.
func NewSuite(ctx context.Context, cfg config.Config) (*Suite, error) {
	images, err := testimages.Load(cfg.ImageManifest)
	if err != nil {
		return nil, err
	}
	scheduler, err := scheduler.New(cfg.Targets)
	if err != nil {
		return nil, err
	}
	clients, err := gcp.NewClients(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Suite{Config: cfg, Images: images, Scheduler: scheduler, Clients: clients}, nil
}

// Close releases process-scoped clients.
func (s *Suite) Close() error { return s.Clients.Close() }

// Environment owns one test attempt and all its resources.
type Environment struct {
	t       *testing.T
	suite   *Suite
	Case    scenario.Case
	Image   testimages.Entry
	Lease   *scheduler.Lease
	Attempt string
	Context context.Context
	record  *artifacts.Recorder
}

// NewEnvironment acquires bounded capacity and creates one attempt artifact
// namespace. Its context is used by all foreground operations.
func (s *Suite) NewEnvironment(t *testing.T, testCase scenario.Case) *Environment {
	t.Helper()
	image, err := s.Images.ForCase(testCase)
	if err != nil {
		t.Fatalf("resolve immutable image input: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.Config.TestTimeout)
	t.Cleanup(cancel)
	attempt := newAttemptID(s.Config.RunID, testCase.ID)
	recorder, err := artifacts.New(s.Config.ArtifactDir, testCase.ID, attempt)
	if err != nil {
		t.Fatalf("create artifacts: %v", err)
	}
	t.Cleanup(func() {
		if err := recorder.Close(); err != nil {
			t.Errorf("close artifact recorder: %v", err)
		}
	})
	_ = recorder.Record("acquire-capacity", "started", nil)
	lease, err := s.Scheduler.Acquire(ctx)
	if err != nil {
		_ = recorder.Record("acquire-capacity", "failed", map[string]any{"error": err.Error()})
		t.Fatalf("acquire capacity: %v", err)
	}
	t.Cleanup(lease.Release)
	_ = recorder.Record("acquire-capacity", "passed", map[string]any{"project": lease.Target.Project, "zone": lease.Target.Zone})

	env := &Environment{t: t, suite: s, Case: testCase, Image: image, Lease: lease, Attempt: attempt, Context: ctx, record: recorder}
	manifest := map[string]any{
		"run_id":          s.Config.RunID,
		"attempt_id":      attempt,
		"test_id":         testCase.ID,
		"feature":         testCase.Feature,
		"category":        testCase.Category,
		"platform":        testCase.Platform,
		"project":         lease.Target.Project,
		"zone":            lease.Target.Zone,
		"image":           image,
		"test_timeout":    s.Config.TestTimeout.String(),
		"cleanup_timeout": s.Config.CleanupTimeout.String(),
	}
	if testCase.Bootstrap == scenario.InstallDEB {
		manifest["agent_package_sha256"] = s.Config.DEBPackageSHA256
	}
	if testCase.Bootstrap == scenario.InstallRPM {
		manifest["agent_package_sha256"] = s.Config.RPMPackageSHA256
	}
	if err := recorder.WriteJSON("manifest.json", manifest); err != nil {
		t.Fatalf("write attempt manifest: %v", err)
	}
	_ = recorder.Record("attempt", "started", map[string]any{"artifact_dir": recorder.Dir})
	t.Logf("attempt %s artifacts: %s", attempt, recorder.Dir)
	return env
}

// Step records phase timing and attributes failures to one named operation.
func (e *Environment) Step(name string, operation func(context.Context) error) {
	e.t.Helper()
	started := time.Now()
	_ = e.record.Record(name, "started", nil)
	if err := operation(e.Context); err != nil {
		_ = e.record.Record(name, "failed", map[string]any{"elapsed": time.Since(started).String(), "error": err.Error()})
		e.t.Fatalf("phase %q failed after %s: %v", name, time.Since(started).Round(time.Millisecond), err)
	}
	_ = e.record.Record(name, "passed", map[string]any{"elapsed": time.Since(started).String()})
}

// CreateVM registers diagnostics and idempotent deletion before issuing the
// create call, so partially successful API calls still have cleanup coverage.
func (e *Environment) CreateVM(ctx context.Context) (*gcp.VM, error) {
	name := resourceName(e.suite.Config.RunID, e.Case.ID, e.Attempt)
	planned := gcp.VM{Project: e.Lease.Target.Project, Zone: e.Lease.Target.Zone, Name: name}
	bootstrap, err := bootstrapScript(e.Case.Bootstrap, e.suite.Config)
	if err != nil {
		return nil, err
	}

	e.t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), e.suite.Config.CleanupTimeout)
		defer cancel()
		if err := e.suite.Clients.DeleteVM(cleanupCtx, planned.Project, planned.Zone, planned.Name); err != nil {
			_ = e.record.Record("cleanup-instance", "failed", map[string]any{"error": err.Error(), "instance": planned})
			e.t.Errorf("cleanup instance %s: %v", planned.Name, err)
			return
		}
		_ = e.record.Record("cleanup-instance", "passed", map[string]any{"instance": planned})
	})
	e.t.Cleanup(func() {
		diagnosticsCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		serial, err := e.suite.Clients.SerialOutput(diagnosticsCtx, planned)
		if err != nil {
			_ = e.record.Record("collect-serial", "failed", map[string]any{"error": err.Error()})
			return
		}
		if err := e.record.WriteText("serial-port-1.log", serial); err != nil {
			e.t.Errorf("write serial output: %v", err)
		}
	})

	metadata := map[string]string{
		"enable-osconfig":         "true",
		"enable-guest-attributes": "true",
		"enable-os-config-debug":  "true",
		"osconfig-poll-interval":  "1",
	}
	if bootstrap != "" {
		metadata["startup-script"] = bootstrap
	}
	machineType := e.Case.MachineType
	if machineType == "" {
		machineType = e.suite.Config.MachineType
	}
	request := gcp.VMRequest{
		Project:          planned.Project,
		Zone:             planned.Zone,
		Name:             planned.Name,
		Image:            e.Image.Image,
		MachineType:      machineType,
		Network:          e.suite.Config.Network,
		ServiceAccount:   e.suite.Config.ServiceAccount,
		EnableExternalIP: e.suite.Config.EnableExternalIP,
		Metadata:         metadata,
		Labels: map[string]string{
			"e2e-run":      labelValue(e.suite.Config.RunID),
			"e2e-test":     labelValue(e.Case.ID),
			"e2e-attempt":  labelValue(e.Attempt),
			"e2e-category": labelValue(string(e.Case.Category)),
			"e2e-expires":  fmt.Sprintf("%d", time.Now().Add(24*time.Hour).Unix()),
		},
	}
	if err := e.record.WriteJSON("instance-planned.json", planned); err != nil {
		return nil, err
	}
	vm, err := e.suite.Clients.CreateVM(ctx, request)
	if err != nil {
		return nil, err
	}
	planned.ID = vm.ID
	if err := e.record.WriteJSON("instance.json", vm); err != nil {
		return nil, err
	}
	return vm, nil
}

// WaitForInventory waits through eventual consistency, fails early on an
// explicit bootstrap failure, and records the final high-signal state.
func (e *Environment) WaitForInventory(ctx context.Context, vm gcp.VM) error {
	var snapshot map[string]any
	var bootstrapStatus string
	err := poll.Until(ctx, e.suite.Config.PollInterval, "fresh OS Config inventory", func(ctx context.Context) (string, bool, error) {
		if e.Case.Bootstrap != scenario.NoBootstrap {
			statusValue, err := e.suite.Clients.GuestAttribute(ctx, vm, "osconfig_e2e", "bootstrap_status")
			if err != nil {
				if gcp.IsNotFound(err) || gcp.IsTransientComputeError(err) {
					return fmt.Sprintf("bootstrap status unavailable: %v", err), false, nil
				}
				return "", false, fmt.Errorf("read bootstrap status: %w", err)
			}
			if strings.HasPrefix(statusValue, "failed:") {
				return statusValue, false, fmt.Errorf("bootstrap reported %s", statusValue)
			}
			bootstrapStatus = statusValue
			if !strings.HasPrefix(statusValue, "ready:") {
				return fmt.Sprintf("bootstrap status=%q", statusValue), false, nil
			}
		}
		inventory, err := e.suite.Clients.Inventory(ctx, vm)
		if err != nil {
			if gcp.IsTransientInventoryError(err) {
				return fmt.Sprintf("inventory unavailable: %v", err), false, nil
			}
			return "", false, err
		}
		shortName := inventory.GetOsInfo().GetShortName()
		snapshot = map[string]any{
			"name":             inventory.GetName(),
			"hostname":         inventory.GetOsInfo().GetHostname(),
			"short_name":       shortName,
			"os_version":       inventory.GetOsInfo().GetVersion(),
			"update_time":      inventory.GetUpdateTime().AsTime().UTC(),
			"item_count":       len(inventory.GetItems()),
			"bootstrap_status": bootstrapStatus,
		}
		if shortName != e.Case.ExpectedShortName {
			return fmt.Sprintf("short_name=%q want=%q", shortName, e.Case.ExpectedShortName), false, nil
		}
		return fmt.Sprintf("short_name=%q items=%d", shortName, len(inventory.GetItems())), true, nil
	})
	if err != nil {
		return err
	}
	return e.record.WriteJSON("inventory.json", snapshot)
}

func bootstrapScript(kind scenario.Bootstrap, cfg config.Config) (string, error) {
	switch kind {
	case scenario.NoBootstrap:
		return "", nil
	case scenario.InstallDEB:
		if cfg.DEBPackageURL == "" || cfg.DEBPackageSHA256 == "" {
			return "", fmt.Errorf("deb_package_url and deb_package_sha256 are required for install-deb cases")
		}
		return linuxInstallScript(
			"previous='absent'; if dpkg-query -W google-osconfig-agent >/dev/null 2>&1; then previous=\"$(dpkg-query -W -f='${Version}' google-osconfig-agent)\"; apt-get remove -y google-osconfig-agent; fi",
			"apt-get install -y ./google-osconfig-agent.deb",
			"dpkg-query -W -f='${Version}' google-osconfig-agent",
			cfg.DEBPackageURL, cfg.DEBPackageSHA256, "google-osconfig-agent.deb"), nil
	case scenario.InstallRPM:
		if cfg.RPMPackageURL == "" || cfg.RPMPackageSHA256 == "" {
			return "", fmt.Errorf("rpm_package_url and rpm_package_sha256 are required for install-rpm cases")
		}
		return linuxInstallScript(
			"previous='absent'; if rpm -q google-osconfig-agent >/dev/null 2>&1; then previous=\"$(rpm -q --qf '%{VERSION}-%{RELEASE}' google-osconfig-agent)\"; dnf remove -y google-osconfig-agent; fi",
			"dnf install -y ./google-osconfig-agent.rpm",
			"rpm -q --qf '%{VERSION}-%{RELEASE}' google-osconfig-agent",
			cfg.RPMPackageURL, cfg.RPMPackageSHA256, "google-osconfig-agent.rpm"), nil
	case scenario.UpgradeRPM:
		if cfg.RPMPackageURL == "" || cfg.RPMPackageSHA256 == "" || cfg.RPMUpgradeFrom == "" {
			return "", fmt.Errorf("rpm_package_url, rpm_package_sha256, and rpm_upgrade_from_version are required for upgrade-rpm cases")
		}
		return linuxUpgradeScript(
			"rpm -q --qf '%{VERSION}-%{RELEASE}' google-osconfig-agent",
			"dnf install -y ./google-osconfig-agent.rpm",
			cfg.RPMUpgradeFrom, cfg.RPMPackageURL, cfg.RPMPackageSHA256, "google-osconfig-agent.rpm"), nil
	default:
		return "", fmt.Errorf("unsupported bootstrap %q", kind)
	}
}

func linuxUpgradeScript(versionCommand, installCommand, expectedPrevious, packageURL, packageSHA256, filename string) string {
	return fmt.Sprintf(`#!/bin/bash
set -Eeuo pipefail
status_uri="http://metadata.google.internal/computeMetadata/v1/instance/guest-attributes/osconfig_e2e/bootstrap_status"
report() {
  curl -fsS -X PUT --data "$1" "$status_uri" -H "Metadata-Flavor: Google"
}
trap 'rc=$?; report "failed:line=${LINENO}:exit=${rc}" || true' ERR
expected_previous=%s
previous="$(%s)"
if [[ "$previous" != "$expected_previous" ]]; then
  report "failed:unexpected-starting-version:actual=${previous}:expected=${expected_previous}"
  trap - ERR
  exit 1
fi
curl -fsSL %s -o %s
echo "%s  %s" | sha256sum --check --strict
%s
systemctl enable --now google-osconfig-agent
systemctl is-active --quiet google-osconfig-agent
version="$(%s)"
if [[ "$version" == "$previous" ]]; then
  report "failed:version-unchanged:${version}"
  trap - ERR
  exit 1
fi
report "ready:previous=${previous}:current=${version}"
`, shellQuote(expectedPrevious), versionCommand, shellQuote(packageURL), shellQuote(filename), packageSHA256, filename, installCommand, versionCommand)
}

func linuxInstallScript(removeCommand, installCommand, versionCommand, packageURL, packageSHA256, filename string) string {
	return fmt.Sprintf(`#!/bin/bash
set -Eeuo pipefail
status_uri="http://metadata.google.internal/computeMetadata/v1/instance/guest-attributes/osconfig_e2e/bootstrap_status"
report() {
  curl -fsS -X PUT --data "$1" "$status_uri" -H "Metadata-Flavor: Google"
}
trap 'rc=$?; report "failed:line=${LINENO}:exit=${rc}" || true' ERR
%s
curl -fsSL %s -o %s
echo "%s  %s" | sha256sum --check --strict
%s
systemctl enable --now google-osconfig-agent
systemctl is-active --quiet google-osconfig-agent
version="$(%s)"
report "ready:previous=${previous}:current=${version}"
`, removeCommand, shellQuote(packageURL), shellQuote(filename), packageSHA256, filename, installCommand, versionCommand)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func newAttemptID(runID, testID string) string {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", runID, testID, time.Now().UnixNano())))
		random = sum[:4]
	}
	return strings.ToLower(hex.EncodeToString(random))
}

func resourceName(runID, testID, attempt string) string {
	base := labelValue("oc-" + runID + "-" + testID)
	if len(base) > 53 {
		base = strings.Trim(base[:53], "-")
	}
	return strings.Trim(base+"-"+attempt, "-")
}

func labelValue(value string) string {
	value = strings.ToLower(value)
	value = unsafeResourceName.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		value = "unknown"
	}
	if len(value) > 63 {
		value = strings.Trim(value[:63], "-")
	}
	return value
}
