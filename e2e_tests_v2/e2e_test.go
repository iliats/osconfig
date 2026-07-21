//go:build e2e

package e2etests_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/config"
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

func runScenario(t *testing.T, testCase scenario.Case, body func(*testing.T, *testenv.Environment)) {
	t.Helper()
	t.Run(testCase.ID, func(t *testing.T) {
		if !e2eSuite.Config.Categories[testCase.Category] {
			t.Skipf("category %q disabled by run configuration", testCase.Category)
		}
		t.Parallel()
		body(t, e2eSuite.NewEnvironment(t, testCase))
	})
}

func expectedBootstrapVersion(testCase scenario.Case) string {
	if testCase.Bootstrap == scenario.InstallDEB {
		return e2eSuite.Config.DEBExpectedVersion
	}
	return e2eSuite.Config.RPMExpectedVersion
}
