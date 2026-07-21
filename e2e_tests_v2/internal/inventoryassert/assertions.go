// Package inventoryassert contains semantic assertions for inventory scenarios.
package inventoryassert

import (
	"fmt"
	"sort"
	"strings"

	"cloud.google.com/go/osconfig/apiv1/osconfigpb"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/scenario"
)

// Check verifies the same high-value fields as the original inventory
// reporting suite: hostname, OS short name, and known installed package items.
func Check(inventory *osconfigpb.Inventory, hostname, shortName string, packages []scenario.Package) error {
	if inventory == nil {
		return fmt.Errorf("inventory is nil")
	}
	var problems []string
	if got := inventory.GetOsInfo().GetHostname(); got != hostname {
		problems = append(problems, fmt.Sprintf("hostname=%q want=%q", got, hostname))
	}
	if got := inventory.GetOsInfo().GetShortName(); got != shortName {
		problems = append(problems, fmt.Sprintf("short_name=%q want=%q", got, shortName))
	}
	if len(inventory.GetItems()) == 0 {
		problems = append(problems, "inventory contains no package items")
	}

	installed := make(map[scenario.PackageManager]map[string]bool)
	for _, item := range inventory.GetItems() {
		pkg := item.GetInstalledPackage()
		for manager, name := range map[scenario.PackageManager]string{
			scenario.APT:    pkg.GetAptPackage().GetPackageName(),
			scenario.YUM:    pkg.GetYumPackage().GetPackageName(),
			scenario.GooGet: pkg.GetGoogetPackage().GetPackageName(),
		} {
			if name == "" {
				continue
			}
			if installed[manager] == nil {
				installed[manager] = make(map[string]bool)
			}
			installed[manager][name] = true
		}
	}
	for _, expected := range packages {
		if !installed[expected.Manager][expected.Name] {
			problems = append(problems, fmt.Sprintf("installed %s package %q not found (observed: %s)", expected.Manager, expected.Name, observed(installed[expected.Manager])))
		}
	}
	if len(problems) != 0 {
		return fmt.Errorf("inventory assertion failed: %s", strings.Join(problems, "; "))
	}
	return nil
}

func observed(packages map[string]bool) string {
	names := make([]string, 0, len(packages))
	for name := range packages {
		names = append(names, name)
	}
	sort.Strings(names)
	const limit = 20
	if len(names) > limit {
		names = append(names[:limit], fmt.Sprintf("... and %d more", len(names)-limit))
	}
	return strings.Join(names, ", ")
}
