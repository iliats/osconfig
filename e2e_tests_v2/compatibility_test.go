//go:build e2e

package e2etests_test

import (
	"context"
	"fmt"
	"testing"

	"cloud.google.com/go/osconfig/apiv1/osconfigpb"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/gcp"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/inventoryassert"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/scenario"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/testenv"
)

// TestPublicImageCompatibility exercises the strong compatibility sequence
// from the design on two unmodified public-image package backends: install the
// candidate agent, report real inventory, enforce an OS policy exec resource,
// and complete a patch dry run targeted to the exact VM.
func TestPublicImageCompatibility(t *testing.T) {
	cases := []scenario.Case{
		{
			ID: "compatibility/strong/debian-12", Feature: "strong-image-compatibility",
			Category: scenario.Compatibility, Platform: "debian-12", ImageKey: "public-debian-12",
			ExpectedShortName: "debian", Bootstrap: scenario.InstallDEB,
			ExpectedPackages: []scenario.Package{
				{Manager: scenario.APT, Name: "bash"},
				{Manager: scenario.APT, Name: "google-osconfig-agent"},
			},
		},
		{
			ID: "compatibility/strong/el9", Feature: "strong-image-compatibility",
			Category: scenario.Compatibility, Platform: "el9", ImageKey: "public-el9",
			ExpectedShortName: "rhel", Bootstrap: scenario.InstallRPM,
			ExpectedPackages: []scenario.Package{
				{Manager: scenario.YUM, Name: "bash"},
				{Manager: scenario.YUM, Name: "google-osconfig-agent"},
			},
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		runScenario(t, testCase, func(t *testing.T, env *testenv.Environment) {
			var vm *gcp.VM
			env.Step("create VM from unmodified public image", func(ctx context.Context) error {
				var err error
				vm, err = env.CreateVM(ctx)
				return err
			})

			var bootstrap testenv.BootstrapResult
			env.Step("install and start exact candidate agent", func(ctx context.Context) error {
				var err error
				bootstrap, err = env.WaitForBootstrap(ctx, *vm)
				if err == nil && bootstrap.Current != expectedBootstrapVersion(testCase) {
					return fmt.Errorf("installed version=%q want=%q", bootstrap.Current, expectedBootstrapVersion(testCase))
				}
				return err
			})

			var inventory *osconfigpb.Inventory
			env.Step("verify fresh inventory and known packages", func(ctx context.Context) error {
				var err error
				inventory, err = env.WaitForInventory(ctx, *vm, bootstrap.ObservedAt)
				if err != nil {
					return err
				}
				return inventoryassert.Check(inventory, vm.Name, testCase.ExpectedShortName, testCase.ExpectedPackages)
			})
			env.Step("enforce minimal OS policy exec resource", func(ctx context.Context) error {
				return env.ApplyMinimalExecPolicy(ctx, *vm)
			})
			env.Step("execute targeted patch dry run", func(ctx context.Context) error {
				return env.RunPatchDryRun(ctx, *vm)
			})
		})
	}
}
