package inventoryassert

import (
	"strings"
	"testing"

	"cloud.google.com/go/osconfig/apiv1/osconfigpb"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/scenario"
)

func TestCheck(t *testing.T) {
	inventory := &osconfigpb.Inventory{
		OsInfo: &osconfigpb.Inventory_OsInfo{Hostname: "vm-1", ShortName: "debian"},
		Items: map[string]*osconfigpb.Inventory_Item{
			"bash": {
				Details: &osconfigpb.Inventory_Item_InstalledPackage{InstalledPackage: &osconfigpb.Inventory_SoftwarePackage{
					Details: &osconfigpb.Inventory_SoftwarePackage_AptPackage{AptPackage: &osconfigpb.Inventory_VersionedPackage{PackageName: "bash"}},
				}},
			},
		},
	}
	if err := Check(inventory, "vm-1", "debian", []scenario.Package{{Manager: scenario.APT, Name: "bash"}}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckReportsAllImportantMismatches(t *testing.T) {
	err := Check(&osconfigpb.Inventory{OsInfo: &osconfigpb.Inventory_OsInfo{Hostname: "wrong", ShortName: "rhel"}}, "vm-1", "debian", []scenario.Package{{Manager: scenario.APT, Name: "bash"}})
	if err == nil {
		t.Fatal("Check() succeeded")
	}
	for _, want := range []string{"hostname", "short_name", "no package items", `package "bash" not found`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Check() error %q does not contain %q", err, want)
		}
	}
}
