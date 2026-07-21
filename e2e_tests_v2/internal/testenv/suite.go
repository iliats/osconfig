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

	"cloud.google.com/go/osconfig/apiv1/osconfigpb"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/artifacts"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/config"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/gcp"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/poll"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/scenario"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/scheduler"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/testimages"
	"google.golang.org/protobuf/types/known/durationpb"
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

// BootstrapResult is the package lifecycle state reported by a startup script.
type BootstrapResult struct {
	Previous   string    `json:"previous"`
	Current    string    `json:"current"`
	ObservedAt time.Time `json:"observed_at"`
	Raw        string    `json:"raw"`
}

type execPolicySpec struct {
	Assignment *osconfigpb.OSPolicyAssignment
	PolicyID   string
	ResourceID string
	MarkerPath string
	WantOutput string
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
		manifest["expected_agent_version"] = s.Config.DEBExpectedVersion
	}
	if testCase.Bootstrap == scenario.InstallRPM || testCase.Bootstrap == scenario.UpgradeRPM {
		manifest["agent_package_sha256"] = s.Config.RPMPackageSHA256
		manifest["expected_agent_version"] = s.Config.RPMExpectedVersion
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

// WaitForBootstrap waits for a package install or upgrade script and returns
// the exact before/after versions it observed.
func (e *Environment) WaitForBootstrap(ctx context.Context, vm gcp.VM) (BootstrapResult, error) {
	if e.Case.Bootstrap == scenario.NoBootstrap {
		return BootstrapResult{}, fmt.Errorf("case %q has no bootstrap operation", e.Case.ID)
	}
	var result BootstrapResult
	err := poll.Until(ctx, e.suite.Config.PollInterval, "agent package bootstrap", func(ctx context.Context) (string, bool, error) {
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
		if !strings.HasPrefix(statusValue, "ready|") {
			return fmt.Sprintf("bootstrap status=%q", statusValue), false, nil
		}
		fields := parseStatusFields(strings.TrimPrefix(statusValue, "ready|"))
		if fields["previous"] == "" || fields["current"] == "" {
			return statusValue, false, fmt.Errorf("bootstrap ready status omitted previous or current version")
		}
		result = BootstrapResult{Previous: fields["previous"], Current: fields["current"], ObservedAt: time.Now().UTC(), Raw: statusValue}
		return statusValue, true, nil
	})
	if err != nil {
		return BootstrapResult{}, err
	}
	if err := e.record.WriteJSON("bootstrap.json", result); err != nil {
		return BootstrapResult{}, err
	}
	return result, nil
}

// WaitForInventory returns a full inventory generated after notBefore. The
// scenario body owns semantic assertions so the behavior remains visible.
func (e *Environment) WaitForInventory(ctx context.Context, vm gcp.VM, notBefore time.Time) (*osconfigpb.Inventory, error) {
	var result *osconfigpb.Inventory
	err := poll.Until(ctx, e.suite.Config.PollInterval, "fresh OS Config inventory", func(ctx context.Context) (string, bool, error) {
		inventory, err := e.suite.Clients.Inventory(ctx, vm)
		if err != nil {
			if gcp.IsTransientInventoryError(err) {
				return fmt.Sprintf("inventory unavailable: %v", err), false, nil
			}
			return "", false, err
		}
		updated := inventory.GetUpdateTime().AsTime()
		if !updated.After(notBefore) {
			return fmt.Sprintf("inventory update=%s is not after %s", updated.UTC(), notBefore.UTC()), false, nil
		}
		result = inventory
		return fmt.Sprintf("hostname=%q short_name=%q items=%d update=%s", inventory.GetOsInfo().GetHostname(), inventory.GetOsInfo().GetShortName(), len(inventory.GetItems()), updated.UTC()), true, nil
	})
	if err != nil {
		return nil, err
	}
	snapshot := map[string]any{
		"name":        result.GetName(),
		"hostname":    result.GetOsInfo().GetHostname(),
		"short_name":  result.GetOsInfo().GetShortName(),
		"os_version":  result.GetOsInfo().GetVersion(),
		"update_time": result.GetUpdateTime().AsTime().UTC(),
		"item_count":  len(result.GetItems()),
	}
	if err := e.record.WriteJSON("inventory.json", snapshot); err != nil {
		return nil, err
	}
	if err := e.record.WriteProtoJSON("inventory-full.json", result); err != nil {
		return nil, err
	}
	return result, nil
}

func parseStatusFields(value string) map[string]string {
	fields := make(map[string]string)
	for _, item := range strings.Split(value, "|") {
		key, fieldValue, ok := strings.Cut(item, "=")
		if ok {
			fields[key] = fieldValue
		}
	}
	return fields
}

// ApplyMinimalExecPolicy creates a uniquely targeted OS policy assignment and
// waits until the agent enforces a marker-file desired state and reports it as
// compliant. This mirrors the original exec-resource scenario on one resource.
func (e *Environment) ApplyMinimalExecPolicy(ctx context.Context, vm gcp.VM) error {
	assignmentID := resourceName("policy", e.Case.ID, e.Attempt)
	assignmentName := fmt.Sprintf("projects/%s/locations/%s/osPolicyAssignments/%s", vm.Project, vm.Zone, assignmentID)
	spec := buildMinimalExecPolicy(e.suite.Config.RunID, e.Attempt)

	e.t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), e.suite.Config.CleanupTimeout)
		defer cancel()
		if err := e.suite.Clients.DeleteOSPolicyAssignment(cleanupCtx, assignmentName); err != nil {
			_ = e.record.Record("cleanup-os-policy", "failed", map[string]any{"assignment": assignmentName, "error": err.Error()})
			e.t.Errorf("cleanup OS policy assignment %s: %v", assignmentName, err)
			return
		}
		_ = e.record.Record("cleanup-os-policy", "passed", map[string]any{"assignment": assignmentName})
	})

	createdAt := time.Now()
	if _, err := e.suite.Clients.CreateOSPolicyAssignment(ctx, vm.Project, vm.Zone, assignmentID, spec.Assignment); err != nil {
		return err
	}
	if err := e.record.WriteJSON("os-policy-assignment.json", map[string]any{
		"name": assignmentName, "policy_id": spec.PolicyID, "resource_id": spec.ResourceID, "marker_path": spec.MarkerPath,
	}); err != nil {
		return err
	}

	var reportSnapshot map[string]any
	err := poll.Until(ctx, e.suite.Config.PollInterval, "OS policy marker compliance", func(ctx context.Context) (string, bool, error) {
		report, err := e.suite.Clients.OSPolicyAssignmentReport(ctx, vm, assignmentID)
		if err != nil {
			if gcp.IsTransientOSConfigError(err) {
				return fmt.Sprintf("policy report unavailable: %v", err), false, nil
			}
			return "", false, err
		}
		if err := e.record.WriteProtoJSON("os-policy-report-last.json", report); err != nil {
			return "", false, err
		}
		if !report.GetUpdateTime().AsTime().After(createdAt) {
			return fmt.Sprintf("policy report update=%s predates assignment creation", report.GetUpdateTime().AsTime().UTC()), false, nil
		}
		for _, compliance := range report.GetOsPolicyCompliances() {
			if compliance.GetOsPolicyId() != spec.PolicyID {
				continue
			}
			if compliance.GetComplianceState() != osconfigpb.OSPolicyAssignmentReport_OSPolicyCompliance_COMPLIANT {
				return fmt.Sprintf("policy=%s state=%s reason=%q", spec.PolicyID, compliance.GetComplianceState(), compliance.GetComplianceStateReason()), false, nil
			}
			for _, resource := range compliance.GetOsPolicyResourceCompliances() {
				if resource.GetOsPolicyResourceId() != spec.ResourceID {
					continue
				}
				if resource.GetComplianceState() != osconfigpb.OSPolicyAssignmentReport_OSPolicyCompliance_OSPolicyResourceCompliance_COMPLIANT {
					return fmt.Sprintf("resource=%s state=%s reason=%q", spec.ResourceID, resource.GetComplianceState(), resource.GetComplianceStateReason()), false, nil
				}
				gotOutput := string(resource.GetExecResourceOutput().GetEnforcementOutput())
				if gotOutput != spec.WantOutput {
					return "", false, fmt.Errorf("OS policy enforcement output = %q, want %q", gotOutput, spec.WantOutput)
				}
				reportSnapshot = map[string]any{
					"name": report.GetName(), "instance": report.GetInstance(), "assignment": report.GetOsPolicyAssignment(),
					"update_time": report.GetUpdateTime().AsTime().UTC(), "last_run_id": report.GetLastRunId(),
					"policy_id": spec.PolicyID, "policy_state": compliance.GetComplianceState().String(),
					"resource_id": spec.ResourceID, "resource_state": resource.GetComplianceState().String(), "enforcement_output": gotOutput,
				}
				return fmt.Sprintf("policy=%s resource=%s state=COMPLIANT output=%q", spec.PolicyID, spec.ResourceID, gotOutput), true, nil
			}
			return "", false, fmt.Errorf("compliant policy report omitted resource %q", spec.ResourceID)
		}
		return fmt.Sprintf("report has %d policies; waiting for %s", len(report.GetOsPolicyCompliances()), spec.PolicyID), false, nil
	})
	if err != nil {
		return err
	}
	if reportSnapshot == nil {
		return fmt.Errorf("OS policy poll completed without a report snapshot")
	}
	return e.record.WriteJSON("os-policy-report.json", reportSnapshot)
}

func buildMinimalExecPolicy(runID, attempt string) execPolicySpec {
	policyID := "write-marker"
	resourceID := "marker-file"
	markerPath := fmt.Sprintf("/var/lib/osconfig-e2e/%s", attempt)
	wantOutput := fmt.Sprintf("policy-applied-%s", attempt)
	validateScript := fmt.Sprintf("if [ \"$(cat %s 2>/dev/null)\" = %s ]; then exit 100; fi; exit 101", shellQuote(markerPath), shellQuote(wantOutput))
	enforceScript := fmt.Sprintf("install -d -m 0755 /var/lib/osconfig-e2e; printf %%s %s > %s; exit 100", shellQuote(wantOutput), shellQuote(markerPath))
	assignment := &osconfigpb.OSPolicyAssignment{
		Description: "E2E v2 compatibility exec-resource check",
		InstanceFilter: &osconfigpb.OSPolicyAssignment_InstanceFilter{
			InclusionLabels: []*osconfigpb.OSPolicyAssignment_LabelSet{{Labels: map[string]string{
				"e2e-run":     labelValue(runID),
				"e2e-attempt": labelValue(attempt),
			}}},
		},
		Rollout: &osconfigpb.OSPolicyAssignment_Rollout{
			DisruptionBudget: &osconfigpb.FixedOrPercent{Mode: &osconfigpb.FixedOrPercent_Fixed{Fixed: 1}},
			MinWaitDuration:  durationpb.New(0),
		},
		OsPolicies: []*osconfigpb.OSPolicy{{
			Id:   policyID,
			Mode: osconfigpb.OSPolicy_ENFORCEMENT,
			ResourceGroups: []*osconfigpb.OSPolicy_ResourceGroup{{Resources: []*osconfigpb.OSPolicy_Resource{{
				Id: resourceID,
				ResourceType: &osconfigpb.OSPolicy_Resource_Exec{Exec: &osconfigpb.OSPolicy_Resource_ExecResource{
					Validate: &osconfigpb.OSPolicy_Resource_ExecResource_Exec{
						Source:      &osconfigpb.OSPolicy_Resource_ExecResource_Exec_Script{Script: validateScript},
						Interpreter: osconfigpb.OSPolicy_Resource_ExecResource_Exec_SHELL,
					},
					Enforce: &osconfigpb.OSPolicy_Resource_ExecResource_Exec{
						Source:         &osconfigpb.OSPolicy_Resource_ExecResource_Exec_Script{Script: enforceScript},
						Interpreter:    osconfigpb.OSPolicy_Resource_ExecResource_Exec_SHELL,
						OutputFilePath: markerPath,
					},
				}},
			}}}},
		}},
	}
	return execPolicySpec{Assignment: assignment, PolicyID: policyID, ResourceID: resourceID, MarkerPath: markerPath, WantOutput: wantOutput}
}

// RunPatchDryRun executes a patch job targeted by exact instance URI and
// requires one successful per-instance result, matching the original patch
// suite's success contract without mutating package state.
func (e *Environment) RunPatchDryRun(ctx context.Context, vm gcp.VM) error {
	instanceURI := fmt.Sprintf("zones/%s/instances/%s", vm.Zone, vm.Name)
	job, err := e.suite.Clients.ExecutePatchJob(ctx, patchDryRunRequest(vm))
	if err != nil {
		return fmt.Errorf("execute patch dry run for %s: %w", vm.Name, err)
	}
	_ = e.record.Record("patch-job", "started", map[string]any{"name": job.GetName(), "instance": instanceURI})

	var completed *osconfigpb.PatchJob
	var details []*osconfigpb.PatchJobInstanceDetails
	err = poll.Until(ctx, e.suite.Config.PollInterval, "targeted patch dry run", func(ctx context.Context) (string, bool, error) {
		current, err := e.suite.Clients.PatchJob(ctx, job.GetName())
		if err != nil {
			if gcp.IsTransientOSConfigError(err) {
				return fmt.Sprintf("patch job unavailable: %v", err), false, nil
			}
			return "", false, err
		}
		if err := e.record.WriteProtoJSON("patch-job-last.json", current); err != nil {
			return "", false, err
		}
		summary := current.GetInstanceDetailsSummary()
		observation := fmt.Sprintf("state=%s progress=%.1f succeeded=%d failed=%d no_agent=%d", current.GetState(), current.GetPercentComplete(), summary.GetSucceededInstanceCount()+summary.GetSucceededRebootRequiredInstanceCount(), summary.GetFailedInstanceCount(), summary.GetNoAgentDetectedInstanceCount())
		switch current.GetState() {
		case osconfigpb.PatchJob_COMPLETED_WITH_ERRORS, osconfigpb.PatchJob_CANCELED, osconfigpb.PatchJob_TIMED_OUT:
			failedDetails, detailErr := e.suite.Clients.PatchJobInstanceDetails(ctx, current.GetName())
			for i, detail := range failedDetails {
				_ = e.record.WriteProtoJSON(fmt.Sprintf("patch-instance-%d.json", i), detail)
			}
			return observation, false, fmt.Errorf("patch job %s ended in %s: %s; details=%v; detail_error=%v", current.GetName(), current.GetState(), current.GetErrorMessage(), failedDetails, detailErr)
		case osconfigpb.PatchJob_SUCCEEDED:
			jobDetails, err := e.suite.Clients.PatchJobInstanceDetails(ctx, current.GetName())
			if err != nil {
				if gcp.IsTransientOSConfigError(err) {
					return observation + fmt.Sprintf(" details unavailable: %v", err), false, nil
				}
				return observation, false, err
			}
			if len(jobDetails) == 0 {
				return observation + " waiting for per-instance result", false, nil
			}
			if len(jobDetails) != 1 {
				return observation, false, fmt.Errorf("patch job targeted %d instances, want exactly 1: %v", len(jobDetails), jobDetails)
			}
			detail := jobDetails[0]
			if err := e.record.WriteProtoJSON("patch-instance-0.json", detail); err != nil {
				return observation, false, err
			}
			if !strings.HasSuffix(detail.GetName(), "/instances/"+vm.Name) {
				return observation, false, fmt.Errorf("patch result instance = %q, want %q", detail.GetName(), vm.Name)
			}
			if detail.GetState() != osconfigpb.Instance_SUCCEEDED && detail.GetState() != osconfigpb.Instance_SUCCEEDED_REBOOT_REQUIRED {
				return observation, false, fmt.Errorf("patch result for %s = %s: %s", vm.Name, detail.GetState(), detail.GetFailureReason())
			}
			completed = current
			details = jobDetails
			return observation, true, nil
		default:
			return observation, false, nil
		}
	})
	if err != nil {
		_ = e.record.Record("patch-job", "failed", map[string]any{"name": job.GetName(), "error": err.Error()})
		return err
	}
	detail := details[0]
	summary := completed.GetInstanceDetailsSummary()
	result := map[string]any{
		"name": completed.GetName(), "state": completed.GetState().String(), "dry_run": completed.GetDryRun(),
		"percent_complete": completed.GetPercentComplete(), "succeeded": summary.GetSucceededInstanceCount(),
		"succeeded_reboot_required": summary.GetSucceededRebootRequiredInstanceCount(),
		"instance":                  detail.GetName(), "instance_state": detail.GetState().String(), "attempt_count": detail.GetAttemptCount(),
	}
	if err := e.record.WriteJSON("patch-job.json", result); err != nil {
		return err
	}
	_ = e.record.Record("patch-job", "passed", result)
	return nil
}

func patchDryRunRequest(vm gcp.VM) *osconfigpb.ExecutePatchJobRequest {
	return &osconfigpb.ExecutePatchJobRequest{
		Parent:      fmt.Sprintf("projects/%s", vm.Project),
		Description: fmt.Sprintf("E2E v2 compatibility dry run for %s", vm.Name),
		InstanceFilter: &osconfigpb.PatchInstanceFilter{
			Instances: []string{fmt.Sprintf("zones/%s/instances/%s", vm.Zone, vm.Name)},
		},
		Duration:    durationpb.New(20 * time.Minute),
		DryRun:      true,
		PatchConfig: &osconfigpb.PatchConfig{RebootConfig: osconfigpb.PatchConfig_NEVER},
	}
}

func bootstrapScript(kind scenario.Bootstrap, cfg config.Config) (string, error) {
	switch kind {
	case scenario.NoBootstrap:
		return "", nil
	case scenario.InstallDEB:
		if cfg.DEBPackageURL == "" || cfg.DEBPackageSHA256 == "" || cfg.DEBExpectedVersion == "" {
			return "", fmt.Errorf("deb_package_url, deb_package_sha256, and deb_expected_version are required for install-deb cases")
		}
		return linuxInstallScript(
			"previous='absent'; if dpkg-query -W google-osconfig-agent >/dev/null 2>&1; then previous=\"$(dpkg-query -W -f='${Version}' google-osconfig-agent)\"; apt-get remove -y google-osconfig-agent; fi",
			"apt-get install -y ./google-osconfig-agent.deb",
			"dpkg-query -W -f='${Version}' google-osconfig-agent",
			cfg.DEBExpectedVersion, cfg.DEBPackageURL, cfg.DEBPackageSHA256, "google-osconfig-agent.deb"), nil
	case scenario.InstallRPM:
		if cfg.RPMPackageURL == "" || cfg.RPMPackageSHA256 == "" || cfg.RPMExpectedVersion == "" {
			return "", fmt.Errorf("rpm_package_url, rpm_package_sha256, and rpm_expected_version are required for install-rpm cases")
		}
		return linuxInstallScript(
			"previous='absent'; if rpm -q google-osconfig-agent >/dev/null 2>&1; then previous=\"$(rpm -q --qf '%{VERSION}-%{RELEASE}' google-osconfig-agent)\"; dnf remove -y google-osconfig-agent; fi",
			"dnf install -y ./google-osconfig-agent.rpm",
			"rpm -q --qf '%{VERSION}-%{RELEASE}' google-osconfig-agent",
			cfg.RPMExpectedVersion, cfg.RPMPackageURL, cfg.RPMPackageSHA256, "google-osconfig-agent.rpm"), nil
	case scenario.UpgradeRPM:
		if cfg.RPMPackageURL == "" || cfg.RPMPackageSHA256 == "" || cfg.RPMUpgradeFrom == "" || cfg.RPMExpectedVersion == "" {
			return "", fmt.Errorf("rpm_package_url, rpm_package_sha256, rpm_expected_version, and rpm_upgrade_from_version are required for upgrade-rpm cases")
		}
		return linuxUpgradeScript(
			"rpm -q --qf '%{VERSION}-%{RELEASE}' google-osconfig-agent",
			"dnf install -y ./google-osconfig-agent.rpm",
			cfg.RPMUpgradeFrom, cfg.RPMExpectedVersion, cfg.RPMPackageURL, cfg.RPMPackageSHA256, "google-osconfig-agent.rpm"), nil
	default:
		return "", fmt.Errorf("unsupported bootstrap %q", kind)
	}
}

func linuxUpgradeScript(versionCommand, installCommand, expectedPrevious, expectedCurrent, packageURL, packageSHA256, filename string) string {
	return fmt.Sprintf(`#!/bin/bash
set -Eeuo pipefail
status_uri="http://metadata.google.internal/computeMetadata/v1/instance/guest-attributes/osconfig_e2e/bootstrap_status"
report() {
  curl -fsS -X PUT --data "$1" "$status_uri" -H "Metadata-Flavor: Google"
}
trap 'rc=$?; report "failed:line=${LINENO}:exit=${rc}" || true' ERR
expected_previous=%s
expected_current=%s
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
systemctl is-enabled --quiet google-osconfig-agent
systemctl is-active --quiet google-osconfig-agent
version="$(%s)"
if [[ "$version" != "$expected_current" ]]; then
  report "failed:unexpected-installed-version:actual=${version}:expected=${expected_current}"
	trap - ERR
	exit 1
fi
report "ready|previous=${previous}|current=${version}"
`, shellQuote(expectedPrevious), shellQuote(expectedCurrent), versionCommand, shellQuote(packageURL), shellQuote(filename), packageSHA256, filename, installCommand, versionCommand)
}

func linuxInstallScript(removeCommand, installCommand, versionCommand, expectedCurrent, packageURL, packageSHA256, filename string) string {
	return fmt.Sprintf(`#!/bin/bash
set -Eeuo pipefail
status_uri="http://metadata.google.internal/computeMetadata/v1/instance/guest-attributes/osconfig_e2e/bootstrap_status"
report() {
  curl -fsS -X PUT --data "$1" "$status_uri" -H "Metadata-Flavor: Google"
}
trap 'rc=$?; report "failed:line=${LINENO}:exit=${rc}" || true' ERR
expected_current=%s
%s
curl -fsSL %s -o %s
echo "%s  %s" | sha256sum --check --strict
%s
systemctl enable --now google-osconfig-agent
systemctl is-enabled --quiet google-osconfig-agent
systemctl is-active --quiet google-osconfig-agent
version="$(%s)"
if [[ "$version" != "$expected_current" ]]; then
  report "failed:unexpected-installed-version:actual=${version}:expected=${expected_current}"
  trap - ERR
  exit 1
fi
report "ready|previous=${previous}|current=${version}"
`, shellQuote(expectedCurrent), removeCommand, shellQuote(packageURL), shellQuote(filename), packageSHA256, filename, installCommand, versionCommand)
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
