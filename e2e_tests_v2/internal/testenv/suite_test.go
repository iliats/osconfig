package testenv

import (
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/config"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/gcp"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/scenario"
)

func TestResourceNameIsValidAndBounded(t *testing.T) {
	got := resourceName("Run_2026/07/21", strings.Repeat("Inventory Reporting ", 10), "deadbeef")
	if len(got) > 63 {
		t.Fatalf("resourceName() length = %d: %q", len(got), got)
	}
	if unsafeResourceName.MatchString(got) || strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
		t.Fatalf("resourceName() is invalid: %q", got)
	}
}

func TestShellQuote(t *testing.T) {
	got := shellQuote("https://example.test/a'b.deb")
	want := `'https://example.test/a'"'"'b.deb'`
	if got != want {
		t.Fatalf("shellQuote() = %q, want %q", got, want)
	}
}

func TestInstallBootstrapRemovesExistingAgentAndReportsVersions(t *testing.T) {
	digest := strings.Repeat("a", 64)
	script, err := bootstrapScript(scenario.InstallDEB, config.Config{
		DEBPackageURL:      "https://example.test/agent.deb",
		DEBPackageSHA256:   digest,
		DEBExpectedVersion: "20260701.00-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"apt-get remove -y google-osconfig-agent",
		"sha256sum --check --strict",
		"systemctl is-enabled --quiet google-osconfig-agent",
		"systemctl is-active --quiet google-osconfig-agent",
		"unexpected-installed-version",
		"ready|previous=${previous}|current=${version}",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("bootstrap script does not contain %q", want)
		}
	}
}

func TestUpgradeBootstrapRequiresAndChecksStartingVersion(t *testing.T) {
	digest := strings.Repeat("b", 64)
	script, err := bootstrapScript(scenario.UpgradeRPM, config.Config{
		RPMPackageURL:      "https://example.test/agent.rpm",
		RPMPackageSHA256:   digest,
		RPMExpectedVersion: "20260701.00-1.el9",
		RPMUpgradeFrom:     "20260101.00-1.el9",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"unexpected-starting-version",
		"'20260101.00-1.el9'",
		"sha256sum --check --strict",
		"systemctl is-enabled --quiet google-osconfig-agent",
		"systemctl is-active --quiet google-osconfig-agent",
		"unexpected-installed-version",
		"ready|previous=${previous}|current=${version}",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("upgrade script does not contain %q", want)
		}
	}
}

func TestParseStatusFieldsPreservesVersionEpoch(t *testing.T) {
	got := parseStatusFields("previous=1:20260101.00-1|current=1:20260701.00-1")
	if got["previous"] != "1:20260101.00-1" || got["current"] != "1:20260701.00-1" {
		t.Fatalf("parseStatusFields() = %#v", got)
	}
}

func TestBuildMinimalExecPolicyUsesUniqueExactLabelsAndObservableOutput(t *testing.T) {
	spec := buildMinimalExecPolicy("run-1", "attempt-1")
	labels := spec.Assignment.GetInstanceFilter().GetInclusionLabels()[0].GetLabels()
	if labels["e2e-run"] != "run-1" || labels["e2e-attempt"] != "attempt-1" {
		t.Fatalf("assignment labels = %#v", labels)
	}
	resource := spec.Assignment.GetOsPolicies()[0].GetResourceGroups()[0].GetResources()[0].GetExec()
	if !strings.Contains(resource.GetValidate().GetScript(), "exit 101") || !strings.Contains(resource.GetEnforce().GetScript(), "exit 100") {
		t.Fatalf("unexpected exec resource: %v", resource)
	}
	if resource.GetEnforce().GetOutputFilePath() != spec.MarkerPath || spec.WantOutput == "" {
		t.Fatalf("policy output is not observable: spec=%+v resource=%v", spec, resource)
	}
}

func TestPatchDryRunRequestTargetsOneExactInstance(t *testing.T) {
	request := patchDryRunRequest(gcp.VM{Project: "project-1", Zone: "us-central1-a", Name: "vm-1"})
	if !request.GetDryRun() {
		t.Fatal("patch request is not a dry run")
	}
	want := "zones/us-central1-a/instances/vm-1"
	instances := request.GetInstanceFilter().GetInstances()
	if len(instances) != 1 || instances[0] != want {
		t.Fatalf("patch targets = %v, want [%s]", instances, want)
	}
	if len(request.GetInstanceFilter().GetInstanceNamePrefixes()) != 0 || len(request.GetInstanceFilter().GetGroupLabels()) != 0 {
		t.Fatalf("patch request contains broader filters: %v", request.GetInstanceFilter())
	}
}
