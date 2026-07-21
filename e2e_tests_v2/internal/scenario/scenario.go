package scenario

import (
	"fmt"
	"sort"
	"strings"
)

// Category identifies the intent of an E2E scenario independently from the
// product feature that it exercises.
type Category string

const (
	Functional     Category = "functional"
	Compatibility  Category = "compatibility"
	InstallUpgrade Category = "install-upgrade"
)

var allCategories = []Category{Functional, Compatibility, InstallUpgrade}

// ParseCategories converts a comma-separated category list into a set. An
// empty value enables all categories.
func ParseCategories(value string) (map[Category]bool, error) {
	enabled := make(map[Category]bool, len(allCategories))
	if strings.TrimSpace(value) == "" {
		for _, category := range allCategories {
			enabled[category] = true
		}
		return enabled, nil
	}

	valid := make(map[Category]bool, len(allCategories))
	for _, category := range allCategories {
		valid[category] = true
	}
	for _, item := range strings.Split(value, ",") {
		category := Category(strings.TrimSpace(item))
		if !valid[category] {
			return nil, fmt.Errorf("unknown E2E category %q", item)
		}
		enabled[category] = true
	}
	return enabled, nil
}

// Names returns enabled category names in stable order.
func Names(enabled map[Category]bool) []string {
	var names []string
	for category, ok := range enabled {
		if ok {
			names = append(names, string(category))
		}
	}
	sort.Strings(names)
	return names
}

// Case is a typed test declaration. Execution plumbing belongs in testenv;
// cases contain only identity, inputs, and expected behavior.
type Case struct {
	ID                string
	Feature           string
	Category          Category
	Platform          string
	ImageKey          string
	ExpectedShortName string
	ExpectedPackages  []Package
	MachineType       string
	Bootstrap         Bootstrap
}

// Package identifies an installed package record expected in OS inventory.
type Package struct {
	Manager PackageManager
	Name    string
}

// PackageManager selects the OS Config inventory package representation.
type PackageManager string

const (
	APT    PackageManager = "apt"
	YUM    PackageManager = "yum"
	GooGet PackageManager = "googet"
)

// Bootstrap describes setup that must happen on a fresh public image.
type Bootstrap string

const (
	NoBootstrap Bootstrap = ""
	InstallDEB  Bootstrap = "install-deb"
	InstallRPM  Bootstrap = "install-rpm"
	UpgradeRPM  Bootstrap = "upgrade-rpm"
)
