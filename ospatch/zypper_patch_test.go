package ospatch

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/osconfig/packages"
	utilmocks "github.com/GoogleCloudPlatform/osconfig/util/mocks"
	utiltest "github.com/GoogleCloudPlatform/osconfig/util/utiltest"
	"github.com/golang/mock/gomock"
)

func TestRunFilter(t *testing.T) {
	patches, pkgUpdates, pkgToPatchesMap := prepareTestCase()
	type input struct {
		patches           []*packages.ZypperPatch
		pkgUpdates        []*packages.PkgInfo
		pkgToPatchesMap   map[string][]string
		exclusiveIncludes []string
		excludes          []*Exclude
		withUpdate        bool
	}
	type expect struct {
		patches    []string
		pkgUpdates []string
		err        error
	}

	var patch3String = "patch-3"

	tests := []struct {
		name   string
		input  input
		expect expect
	}{
		{name: "runfilterwithexclusivepatches",
			input:  input{patches: patches, pkgUpdates: pkgUpdates, pkgToPatchesMap: pkgToPatchesMap, exclusiveIncludes: []string{"patch-3"}, excludes: []*Exclude{}, withUpdate: false},
			expect: expect{patches: []string{"patch-3"}, pkgUpdates: []string{}, err: nil},
		},
		{name: "runFilterwithUpdatewithexcludes",
			// withupdate, exclude a patch that has
			input:  input{patches: patches, pkgUpdates: pkgUpdates, pkgToPatchesMap: pkgToPatchesMap, exclusiveIncludes: []string{}, excludes: []*Exclude{CreateStringExclude(&patch3String)}, withUpdate: true},
			expect: expect{patches: []string{"patch-1", "patch-2"}, pkgUpdates: []string{"pkg6"}, err: nil},
		},
		{name: "runFilterwithoutUpdatewithexcludes",
			input:  input{patches: patches, pkgUpdates: pkgUpdates, pkgToPatchesMap: pkgToPatchesMap, exclusiveIncludes: []string{}, excludes: []*Exclude{CreateStringExclude(&patch3String)}, withUpdate: false},
			expect: expect{patches: []string{"patch-1", "patch-2"}, pkgUpdates: []string{}, err: nil},
		},
		{name: "runFilterwithUpdatewithoutexcludes",
			input:  input{patches: patches, pkgUpdates: pkgUpdates, pkgToPatchesMap: pkgToPatchesMap, exclusiveIncludes: []string{}, excludes: []*Exclude{}, withUpdate: true},
			expect: expect{patches: []string{"patch-1", "patch-2", "patch-3"}, pkgUpdates: []string{"pkg6"}, err: nil},
		},
		{name: "runFilterwithoutUpdatewithoutexcludes",
			input:  input{patches: patches, pkgUpdates: pkgUpdates, pkgToPatchesMap: pkgToPatchesMap, exclusiveIncludes: []string{}, excludes: []*Exclude{}, withUpdate: false},
			expect: expect{patches: []string{"patch-1", "patch-2", "patch-3"}, pkgUpdates: []string{}, err: nil},
		},
	}

	for _, tc := range tests {
		fPatches, fpkgs, err := runFilter(tc.input.patches, tc.input.exclusiveIncludes, tc.input.excludes, tc.input.pkgUpdates, tc.input.pkgToPatchesMap, tc.input.withUpdate)
		if err != nil {
			t.Errorf("[%s] unexpected error: got(%+v)", tc.name, err)
			continue
		}
		if len(fPatches) != len(tc.expect.patches) {
			t.Errorf("[%s] unexpected number of patches: expected(%d), got(%d)", tc.name, len(tc.expect.patches), len(fPatches))
		}
		for _, p := range fPatches {
			if !isIn(p.Name, tc.expect.patches) {
				t.Errorf("[%s] unexpected patch name: (%s)! is not in %+v", tc.name, p.Name, tc.expect.patches)
			}
		}
		if len(fpkgs) != len(tc.expect.pkgUpdates) {
			t.Errorf("[%s] unexpected number of packages: expected(%d), got(%d)", tc.name, len(tc.expect.pkgUpdates), len(fpkgs))
		}
		for _, p := range fpkgs {
			if !isIn(p.Name, tc.expect.pkgUpdates) {
				t.Errorf("[%s] unexpected package name: (%s)! is not in %+v", tc.name, p.Name, tc.expect.pkgUpdates)
			}
		}
	}
}

func isIn(needle string, haystack []string) bool {
	for _, hay := range haystack {
		if strings.Compare(hay, needle) == 0 {
			return true
		}
	}
	return false
}

func prepareTestCase() ([]*packages.ZypperPatch, []*packages.PkgInfo, map[string][]string) {
	var patches []*packages.ZypperPatch
	var pkgUpdates []*packages.PkgInfo
	var pkgToPatchesMap map[string][]string
	patches = append(patches, &packages.ZypperPatch{
		Name:     "patch-1",
		Category: "recommended",
		Severity: "important",
		Summary:  "patch-1",
	})
	patches = append(patches, &packages.ZypperPatch{
		Name:     "patch-2",
		Category: "security",
		Severity: "critical",
		Summary:  "patch-2",
	})
	patches = append(patches, &packages.ZypperPatch{
		Name:     "patch-3",
		Category: "optional",
		Severity: "low",
		Summary:  "patch-3",
	})

	pkgUpdates = append(pkgUpdates, &packages.PkgInfo{
		Name:    "pkg1",
		Arch:    "noarch",
		Version: "1.1.1",
	})
	pkgUpdates = append(pkgUpdates, &packages.PkgInfo{
		Name:    "pkg2",
		Arch:    "noarch",
		Version: "1.1.1",
	})
	pkgUpdates = append(pkgUpdates, &packages.PkgInfo{
		Name:    "pkg3",
		Arch:    "noarch",
		Version: "1.1.1",
	})
	pkgUpdates = append(pkgUpdates, &packages.PkgInfo{
		Name:    "pkg4",
		Arch:    "noarch",
		Version: "1.1.1",
	})
	pkgUpdates = append(pkgUpdates, &packages.PkgInfo{
		Name:    "pkg5",
		Arch:    "noarch",
		Version: "1.1.1",
	})
	// individual package update that is not a part
	// of a patch. this package only shows up
	// if user specifies --with-update
	pkgUpdates = append(pkgUpdates, &packages.PkgInfo{
		Name:    "pkg6",
		Arch:    "noarch",
		Version: "1.1.1",
	})

	pkgToPatchesMap = make(map[string][]string)
	pkgToPatchesMap["pkg1"] = []string{"patch-1"}
	pkgToPatchesMap["pkg2"] = []string{"patch-1"}
	pkgToPatchesMap["pkg3"] = []string{"patch-2"}
	pkgToPatchesMap["pkg4"] = []string{"patch-2"}
	pkgToPatchesMap["pkg5"] = []string{"patch-3"}

	return patches, pkgUpdates, pkgToPatchesMap
}

func TestRunZypperPatch(t *testing.T) {
	const zypperBin = "/usr/bin/zypper"

	listPatchesBaseArgs := []string{"--gpg-auto-import-keys", "-q", "list-patches"}
	listPatchesAllArgs := append(listPatchesBaseArgs, "--all")
	listUpdatesArgs := []string{"--gpg-auto-import-keys", "-q", "list-updates"}
	installArgs := []string{"--gpg-auto-import-keys", "--non-interactive", "install", "--auto-agree-with-licenses"}

	// One "needed" patch in list-patches format.
	onePatchOutput := []byte(`SLE-Module | patch-1 | security | important | --- | needed | Security patch`)
	// Two "needed" patches.
	twoPatchesOutput := []byte("SLE-Module | patch-1 | security | important | --- | needed | Security patch\n" +
		"SLE-Module | patch-2 | recommended | moderate | --- | needed | Recommended patch")
	// One available package update in list-updates format.
	oneUpdateOutput := []byte(`v | SLES12-SP3-Updates  | pkg1 | 1.0.0 | 2.0.0 | x86_64`)

	someErr := errors.New("some error")

	tests := []struct {
		name      string
		opts      []ZypperPatchOption
		setupMock func(ctx context.Context, mock *utilmocks.MockCommandRunner)
		wantErr   error
	}{
		{
			name: "ZypperPatchesError",
			opts: nil,
			setupMock: func(ctx context.Context, mock *utilmocks.MockCommandRunner) {
				mock.EXPECT().
					Run(ctx, utilmocks.EqCmd(exec.Command(zypperBin, listPatchesAllArgs...))).
					Return(nil, nil, someErr).Times(1)
			},
			wantErr: someErr,
		},
		{
			name: "NoPatches_NoUpdatesRequired",
			opts: nil,
			setupMock: func(ctx context.Context, mock *utilmocks.MockCommandRunner) {
				mock.EXPECT().
					Run(ctx, utilmocks.EqCmd(exec.Command(zypperBin, listPatchesAllArgs...))).
					Return([]byte(""), nil, nil).Times(1)
			},
			wantErr: nil,
		},
		{
			name: "DryrunWithPatches_SkipsInstall",
			opts: []ZypperPatchOption{ZypperUpdateDryrun(true)},
			setupMock: func(ctx context.Context, mock *utilmocks.MockCommandRunner) {
				mock.EXPECT().
					Run(ctx, utilmocks.EqCmd(exec.Command(zypperBin, listPatchesAllArgs...))).
					Return(onePatchOutput, nil, nil).Times(1)
				// No install call expected.
			},
			wantErr: nil,
		},
		{
			name: "InstallsPatches",
			opts: nil,
			setupMock: func(ctx context.Context, mock *utilmocks.MockCommandRunner) {
				listCall := mock.EXPECT().
					Run(ctx, utilmocks.EqCmd(exec.Command(zypperBin, listPatchesAllArgs...))).
					Return(onePatchOutput, nil, nil).Times(1)
				mock.EXPECT().
					Run(ctx, utilmocks.EqCmd(exec.Command(zypperBin, append(installArgs, "patch:patch-1")...))).
					After(listCall).Return(nil, nil, nil).Times(1)
			},
			wantErr: nil,
		},
		{
			name: "InstallError_PropagatesError",
			opts: nil,
			setupMock: func(ctx context.Context, mock *utilmocks.MockCommandRunner) {
				listCall := mock.EXPECT().
					Run(ctx, utilmocks.EqCmd(exec.Command(zypperBin, listPatchesAllArgs...))).
					Return(onePatchOutput, nil, nil).Times(1)
				mock.EXPECT().
					Run(ctx, utilmocks.EqCmd(exec.Command(zypperBin, append(installArgs, "patch:patch-1")...))).
					After(listCall).Return(nil, nil, someErr).Times(1)
			},
			wantErr: someErr,
		},
		{
			name: "WithUpdate_ZypperUpdatesError",
			opts: []ZypperPatchOption{ZypperUpdateWithUpdate(true)},
			setupMock: func(ctx context.Context, mock *utilmocks.MockCommandRunner) {
				// Empty patches list so ZypperPackagesInPatch short-circuits.
				listCall := mock.EXPECT().
					Run(ctx, utilmocks.EqCmd(exec.Command(zypperBin, listPatchesAllArgs...))).
					Return([]byte(""), nil, nil).Times(1)
				mock.EXPECT().
					Run(ctx, utilmocks.EqCmd(exec.Command(zypperBin, listUpdatesArgs...))).
					After(listCall).Return(nil, nil, someErr).Times(1)
			},
			wantErr: someErr,
		},
		{
			name: "WithUpdate_InstallsNonPatchPackages",
			opts: []ZypperPatchOption{ZypperUpdateWithUpdate(true)},
			setupMock: func(ctx context.Context, mock *utilmocks.MockCommandRunner) {
				// Empty patches list so ZypperPackagesInPatch short-circuits (returns empty map, nil).
				listPatchesCall := mock.EXPECT().
					Run(ctx, utilmocks.EqCmd(exec.Command(zypperBin, listPatchesAllArgs...))).
					Return([]byte(""), nil, nil).Times(1)
				listUpdatesCall := mock.EXPECT().
					Run(ctx, utilmocks.EqCmd(exec.Command(zypperBin, listUpdatesArgs...))).
					After(listPatchesCall).Return(oneUpdateOutput, nil, nil).Times(1)
				mock.EXPECT().
					Run(ctx, utilmocks.EqCmd(exec.Command(zypperBin, append(installArgs, "package:pkg1")...))).
					After(listUpdatesCall).Return(nil, nil, nil).Times(1)
			},
			wantErr: nil,
		},
		{
			name: "CategoryOption_PassesCorrectArgs",
			opts: []ZypperPatchOption{ZypperPatchCategories([]string{"security"})},
			setupMock: func(ctx context.Context, mock *utilmocks.MockCommandRunner) {
				// With a category filter, --all is NOT appended.
				args := append(listPatchesBaseArgs, "--category=security")
				mock.EXPECT().
					Run(ctx, utilmocks.EqCmd(exec.Command(zypperBin, args...))).
					Return([]byte(""), nil, nil).Times(1)
			},
			wantErr: nil,
		},
		{
			name: "SeverityOption_PassesCorrectArgs",
			opts: []ZypperPatchOption{ZypperPatchSeverities([]string{"critical"})},
			setupMock: func(ctx context.Context, mock *utilmocks.MockCommandRunner) {
				// With a severity filter, --all is NOT appended.
				args := append(listPatchesBaseArgs, "--severity=critical")
				mock.EXPECT().
					Run(ctx, utilmocks.EqCmd(exec.Command(zypperBin, args...))).
					Return([]byte(""), nil, nil).Times(1)
			},
			wantErr: nil,
		},
		{
			name: "WithOptionalOption_PassesCorrectArgs",
			opts: []ZypperPatchOption{ZypperUpdateWithOptional(true)},
			setupMock: func(ctx context.Context, mock *utilmocks.MockCommandRunner) {
				args := append(listPatchesBaseArgs, "--with-optional", "--all")
				mock.EXPECT().
					Run(ctx, utilmocks.EqCmd(exec.Command(zypperBin, args...))).
					Return([]byte(""), nil, nil).Times(1)
			},
			wantErr: nil,
		},
		{
			name: "ExclusivePatches_OnlyInstallsSpecifiedPatch",
			opts: []ZypperPatchOption{ZypperUpdateWithExclusivePatches([]string{"patch-1"})},
			setupMock: func(ctx context.Context, mock *utilmocks.MockCommandRunner) {
				listCall := mock.EXPECT().
					Run(ctx, utilmocks.EqCmd(exec.Command(zypperBin, listPatchesAllArgs...))).
					Return(twoPatchesOutput, nil, nil).Times(1)
				// Only patch-1 should be installed, not patch-2.
				mock.EXPECT().
					Run(ctx, utilmocks.EqCmd(exec.Command(zypperBin, append(installArgs, "patch:patch-1")...))).
					After(listCall).Return(nil, nil, nil).Times(1)
			},
			wantErr: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mockCtrl := gomock.NewController(t)
			defer mockCtrl.Finish()

			mockCommandRunner := utilmocks.NewMockCommandRunner(mockCtrl)
			packages.SetCommandRunner(mockCommandRunner)
			tc.setupMock(ctx, mockCommandRunner)

			err := RunZypperPatch(ctx, tc.opts...)
			utiltest.AssertErrorMatch(t, err, tc.wantErr)
		})
	}
}
