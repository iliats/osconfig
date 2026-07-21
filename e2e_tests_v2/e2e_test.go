//go:build e2e

package e2etests_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/config"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/gcp"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/scenario"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/testenv"
)

var e2eSuite *testenv.Suite

func TestMain(m *testing.M) {
	config, err := config.LoadFromEnvironment()
	if err != nil {
		fmt.Fprintf(os.Stderr, "E2E configuration error: %v\n", err)
		os.Exit(2)
	}
	suite, err := testenv.NewSuite(context.Background(), config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "E2E initialization error: %v\n", err)
		os.Exit(2)
	}
	e2eSuite = suite
	code := m.Run()
	if err := suite.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "close E2E clients: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func TestFunctional(t *testing.T) {
	runCases(t, []scenario.Case{
		{ID: "functional/inventory/debian-12", Feature: "inventory-reporting", Category: scenario.Functional, Platform: "debian-12", ImageKey: "functional-debian-12", ExpectedShortName: "debian"},
		{ID: "functional/inventory/el9", Feature: "inventory-reporting", Category: scenario.Functional, Platform: "el9", ImageKey: "functional-el9", ExpectedShortName: "rhel"},
	})
}

func TestCompatibility(t *testing.T) {
	runCases(t, []scenario.Case{
		{ID: "compatibility/inventory/debian-12", Feature: "image-compatibility", Category: scenario.Compatibility, Platform: "debian-12", ImageKey: "public-debian-12", ExpectedShortName: "debian"},
		{ID: "compatibility/inventory/windows-2019", Feature: "image-compatibility", Category: scenario.Compatibility, Platform: "windows-2019", ImageKey: "public-windows-2019", ExpectedShortName: "windows", MachineType: "e2-standard-4"},
	})
}

func TestInstallUpgrade(t *testing.T) {
	runCases(t, []scenario.Case{
		{ID: "install-upgrade/install/debian-12", Feature: "agent-installation", Category: scenario.InstallUpgrade, Platform: "debian-12", ImageKey: "public-debian-12", ExpectedShortName: "debian", Bootstrap: scenario.InstallDEB},
		{ID: "install-upgrade/upgrade/el9", Feature: "agent-upgrade", Category: scenario.InstallUpgrade, Platform: "el9", ImageKey: "public-el9", ExpectedShortName: "rhel", Bootstrap: scenario.UpgradeRPM},
	})
}

func runCases(t *testing.T, cases []scenario.Case) {
	t.Helper()
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.ID, func(t *testing.T) {
			if !e2eSuite.Config.Categories[testCase.Category] {
				t.Skipf("category %q disabled by run configuration", testCase.Category)
			}
			t.Parallel()
			env := e2eSuite.NewEnvironment(t, testCase)
			var vmName string
			var vmID uint64
			env.Step("create isolated VM", func(ctx context.Context) error {
				vm, err := env.CreateVM(ctx)
				if err != nil {
					return err
				}
				vmName = vm.Name
				vmID = vm.ID
				return nil
			})
			env.Step("wait for inventory", func(ctx context.Context) error {
				return env.WaitForInventory(ctx, gcp.VM{Project: env.Lease.Target.Project, Zone: env.Lease.Target.Zone, Name: vmName, ID: vmID})
			})
			t.Logf("scenario passed on instance %s", vmName)
		})
	}
}
