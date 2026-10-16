//nolint:testpackage // the tests read the manager's unexported state
package upgrade

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/bosh"
)

// requestKey marks the context of a test request, so a test can tell that
// a director call ran under it.
type requestKey struct{}

var errReleaseListInTest = errors.New("the director is unreachable")

// catalogDirector is a releaseDirector that fails the release listing
// when listErr is set, and records whether each listing ran under the
// request's context.
type catalogDirector struct {
	*releaseDirector

	listErr error

	listMu   sync.Mutex
	listings []bool
}

func newCatalogDirector() *catalogDirector {
	return &catalogDirector{releaseDirector: newReleaseDirector()}
}

func (d *catalogDirector) GetReleases(ctx context.Context) ([]bosh.Release, error) {
	d.listMu.Lock()
	d.listings = append(d.listings, ctx.Value(requestKey{}) != nil)
	d.listMu.Unlock()

	if d.listErr != nil {
		return nil, d.listErr
	}

	return d.releaseDirector.GetReleases(ctx)
}

// listed returns, for each release listing, whether it ran under the
// request's context.
func (d *catalogDirector) listed() []bool {
	d.listMu.Lock()
	defer d.listMu.Unlock()

	return append([]bool(nil), d.listings...)
}

// createWith starts a manager over the director and asks it for an upgrade
// of the first instance with the targets given, under a request context.
func createWith(t *testing.T, director bosh.Director, stemcell StemcellTarget, releases []ReleaseTarget) (*Manager, *UpgradeTask, error) {
	t.Helper()

	manager := loadManager(t, director, newWaitVault())

	ctx := context.WithValue(context.Background(), requestKey{}, true)

	task, err := manager.CreateTask(ctx, CreateTaskRequest{
		InstanceIDs:    []string{waitInstanceID},
		TargetStemcell: stemcell,
		TargetReleases: releases,
	})

	return manager, task, err
}

// expectRejected fails the test unless the request failed with want and
// left no upgrade and no deploy behind.
func expectRejected(t *testing.T, manager *Manager, director *catalogDirector, err, want error) {
	t.Helper()

	if !errors.Is(err, want) {
		t.Fatalf("expected %v, got %v", want, err)
	}

	if tasks := manager.ListTasks(); len(tasks) != 0 {
		t.Fatalf("expected no upgrade to be created, got %+v", tasks)
	}

	if deploys := director.allDeploys(); len(deploys) != 0 {
		t.Fatalf("expected no deploy, got %v", deploys)
	}
}

//nolint:paralleltest // the managers run on shared test directors
func TestCreateTaskRejectsInvalidTargets(t *testing.T) {
	stemcell := StemcellTarget{OS: targetStemcellOS, Version: targetStemcellVersion}
	forge := ReleaseTarget{Name: redisForge, Version: redisForgeTarget}

	for name, test := range map[string]struct {
		stemcell StemcellTarget
		releases []ReleaseTarget
		want     error
	}{
		"no target":                        {want: ErrNoUpgradeTarget},
		"a stemcell with no version":       {stemcell: StemcellTarget{OS: targetStemcellOS}, want: ErrInvalidStemcell},
		"a stemcell with no OS":            {stemcell: StemcellTarget{Version: targetStemcellVersion}, want: ErrInvalidStemcell},
		"releases and a half-set stemcell": {stemcell: StemcellTarget{OS: targetStemcellOS}, releases: []ReleaseTarget{forge}, want: ErrInvalidStemcell},
		"a release with no version":        {releases: []ReleaseTarget{{Name: redisForge}}, want: ErrInvalidRelease},
		"a release with no name":           {releases: []ReleaseTarget{{Version: redisForgeTarget}}, want: ErrInvalidRelease},
		"a release named twice":            {releases: []ReleaseTarget{forge, {Name: redisForge, Version: "9.9.8"}}, want: ErrInvalidRelease},
		"a version the director lacks":     {releases: []ReleaseTarget{{Name: redisForge, Version: "9.9.10"}}, want: ErrReleaseNotOnDirector},
		"a release the director lacks":     {stemcell: stemcell, releases: []ReleaseTarget{{Name: "routing", Version: "0.1.0"}}, want: ErrReleaseNotOnDirector},
	} {
		t.Run(name, func(t *testing.T) {
			director := newCatalogDirector()

			manager, _, err := createWith(t, director, test.stemcell, test.releases)

			expectRejected(t, manager, director, err, test.want)
		})
	}
}

//nolint:paralleltest // the manager runs on a shared test director
func TestCreateTaskNamesEveryReleaseTheDirectorLacks(t *testing.T) {
	director := newCatalogDirector()

	manager, _, err := createWith(t, director, StemcellTarget{}, []ReleaseTarget{
		{Name: redisForge, Version: "9.9.10"},
		{Name: bpmRelease, Version: deployedBPMVersion},
		{Name: "routing", Version: "0.1.0"},
	})

	expectRejected(t, manager, director, err, ErrReleaseNotOnDirector)

	for _, part := range []string{"redis-forge/9.9.10", "routing/0.1.0", "never uploads"} {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("expected the error to name %q, got %q", part, err)
		}
	}

	if strings.Contains(err.Error(), "bpm/") {
		t.Fatalf("expected the error to leave out bpm, which the director holds, got %q", err)
	}
}

//nolint:paralleltest // the manager runs on a shared test director
func TestCreateTaskFailsWhenTheDirectorCannotListReleases(t *testing.T) {
	director := newCatalogDirector()
	director.listErr = errReleaseListInTest

	manager, _, err := createWith(t, director, StemcellTarget{}, []ReleaseTarget{{Name: redisForge, Version: redisForgeTarget}})

	expectRejected(t, manager, director, err, errReleaseListInTest)
}

//nolint:paralleltest // the manager runs with no director
func TestCreateTaskWithReleasesNeedsADirector(t *testing.T) {
	manager := loadManager(t, nil, newWaitVault())

	_, err := manager.CreateTask(context.Background(), CreateTaskRequest{
		InstanceIDs:    []string{waitInstanceID},
		TargetReleases: []ReleaseTarget{{Name: redisForge, Version: redisForgeTarget}},
	})
	if !errors.Is(err, ErrNoDirector) {
		t.Fatalf("expected %v, got %v", ErrNoDirector, err)
	}

	if tasks := manager.ListTasks(); len(tasks) != 0 {
		t.Fatalf("expected no upgrade to be created, got %+v", tasks)
	}
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestCreateTaskAcceptsEachTargetCombination(t *testing.T) {
	stemcell := StemcellTarget{OS: targetStemcellOS, Version: targetStemcellVersion}
	forge := []ReleaseTarget{{Name: redisForge, Version: redisForgeTarget}}

	// An upgrade with release targets lists the director's releases under
	// the request when it is created, and again under the manager when its
	// processing starts.
	checked := []bool{true, false}

	for name, test := range map[string]struct {
		stemcell StemcellTarget
		releases []ReleaseTarget
		listings []bool
		check    func(t *testing.T, manifest string)
	}{
		"a stemcell alone": {stemcell: stemcell, check: func(t *testing.T, manifest string) {
			t.Helper()

			if version := releaseVersion(t, manifest, redisForge); version != "latest" {
				t.Fatalf("expected %s to stay at latest, got %q", redisForge, version)
			}
		}},
		"releases alone": {releases: forge, listings: checked, check: func(t *testing.T, manifest string) {
			t.Helper()
			expectReleaseBumped(t, manifest, deployedStemcellVersion)
		}},
		"a stemcell and releases": {stemcell: stemcell, releases: forge, listings: checked, check: func(t *testing.T, manifest string) {
			t.Helper()
			expectReleaseBumped(t, manifest, targetStemcellVersion)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			director := newCatalogDirector()

			manager, task, err := createWith(t, director, test.stemcell, test.releases)
			if err != nil {
				t.Fatalf("creating the upgrade: %v", err)
			}

			if task.TargetStemcell != test.stemcell || len(task.TargetReleases) != len(test.releases) {
				t.Fatalf("expected the upgrade to keep its targets, got %+v", task)
			}

			done := expectCompleted(t, manager, task.ID)
			if done.CompletedCount != 1 {
				t.Fatalf("expected the instance to succeed, got %+v", done.Instances)
			}

			if listings := director.listed(); !slices.Equal(listings, test.listings) {
				t.Fatalf("expected release listings %v, where true is under the request's context, got %v", test.listings, listings)
			}

			test.check(t, director.deployed(t, firstDeployment))
		})
	}
}

func TestDescribeTargetsNamesOnlyTheTargetsSet(t *testing.T) {
	t.Parallel()

	stemcell := StemcellTarget{OS: targetStemcellOS, Version: targetStemcellVersion}
	forge := []ReleaseTarget{{Name: redisForge, Version: redisForgeTarget}}

	for want, request := range map[string]CreateTaskRequest{
		"stemcell " + targetStemcellOS + "/" + targetStemcellVersion:                                 {TargetStemcell: stemcell},
		"release redis-forge/9.9.9":                                                                  {TargetReleases: forge},
		"stemcell " + targetStemcellOS + "/" + targetStemcellVersion + ", release redis-forge/9.9.9": {TargetStemcell: stemcell, TargetReleases: forge},
		"no target": {},
	} {
		if got := request.DescribeTargets(); got != want {
			t.Fatalf("expected %q, got %q", want, got)
		}
	}
}
