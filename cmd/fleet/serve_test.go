package main

import (
	"context"
	"errors"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fleetdm/fleet/v4/server/config"
	"github.com/fleetdm/fleet/v4/server/fleet"
	"github.com/fleetdm/fleet/v4/server/mock"
	"github.com/fleetdm/fleet/v4/server/service"
	kitlog "github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// safeStore is a wrapper around mock.Store to allow for concurrent calling to
// AppConfig, in the past we have seen this test fail with a data race warning.
//
// TODO: if we see other tests failing for similar reasons, we should build a
// more robust pattern instead of doing this everywhere
type safeStore struct {
	mock.Store
	mu sync.Mutex
}

func (s *safeStore) AppConfig(ctx context.Context) (*fleet.AppConfig, error) {
	s.mu.Lock()
	s.AppConfigFuncInvoked = true
	s.mu.Unlock()
	return s.AppConfigFunc(ctx)
}

func TestMaybeSendStatistics(t *testing.T) {
	ds := new(mock.Store)

	fleetConfig := config.FleetConfig{Osquery: config.OsqueryConfig{DetailUpdateInterval: 1 * time.Hour}}

	requestBody := ""

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBodyBytes, err := ioutil.ReadAll(r.Body)
		require.NoError(t, err)
		requestBody = string(requestBodyBytes)
	}))
	defer ts.Close()

	ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
		return &fleet.AppConfig{ServerSettings: fleet.ServerSettings{EnableAnalytics: true}}, nil
	}

	ds.ShouldSendStatisticsFunc = func(ctx context.Context, frequency time.Duration, config config.FleetConfig, license *fleet.LicenseInfo) (fleet.StatisticsPayload, bool, error) {
		return fleet.StatisticsPayload{
			AnonymousIdentifier:                  "ident",
			FleetVersion:                         "1.2.3",
			LicenseTier:                          "premium",
			NumHostsEnrolled:                     999,
			NumUsers:                             99,
			NumTeams:                             9,
			NumPolicies:                          0,
			NumLabels:                            3,
			SoftwareInventoryEnabled:             true,
			VulnDetectionEnabled:                 true,
			SystemUsersEnabled:                   true,
			HostsStatusWebHookEnabled:            true,
			NumWeeklyActiveUsers:                 111,
			NumWeeklyPolicyViolationDaysActual:   0,
			NumWeeklyPolicyViolationDaysPossible: 0,
			HostsEnrolledByOperatingSystem: map[string][]fleet.HostsCountByOSVersion{
				"linux": {
					fleet.HostsCountByOSVersion{Version: "1.2.3", NumEnrolled: 22},
				},
			},
			StoredErrors: []byte(`[]`),
			Organization: "Fleet",
		}, true, nil
	}
	recorded := false
	ds.RecordStatisticsSentFunc = func(ctx context.Context) error {
		recorded = true
		return nil
	}
	cleanedup := false
	ds.CleanupStatisticsFunc = func(ctx context.Context) error {
		cleanedup = true
		return nil
	}

	err := trySendStatistics(context.Background(), ds, fleet.StatisticsFrequency, ts.URL, fleetConfig, &fleet.LicenseInfo{Tier: "premium"})
	require.NoError(t, err)
	assert.True(t, recorded)
	require.True(t, cleanedup)
	assert.Equal(t, `{"anonymousIdentifier":"ident","fleetVersion":"1.2.3","licenseTier":"premium","organization":"Fleet","numHostsEnrolled":999,"numUsers":99,"numTeams":9,"numPolicies":0,"numLabels":3,"softwareInventoryEnabled":true,"vulnDetectionEnabled":true,"systemUsersEnabled":true,"hostsStatusWebHookEnabled":true,"numWeeklyActiveUsers":111,"numWeeklyPolicyViolationDaysActual":0,"numWeeklyPolicyViolationDaysPossible":0,"hostsEnrolledByOperatingSystem":{"linux":[{"version":"1.2.3","numEnrolled":22}]},"storedErrors":[],"numHostsNotResponding":0}`, requestBody)
}

func TestMaybeSendStatisticsSkipsSendingIfNotNeeded(t *testing.T) {
	ds := new(mock.Store)

	fleetConfig := config.FleetConfig{Osquery: config.OsqueryConfig{DetailUpdateInterval: 1 * time.Hour}}

	called := false

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer ts.Close()

	ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
		return &fleet.AppConfig{ServerSettings: fleet.ServerSettings{EnableAnalytics: true}}, nil
	}

	ds.ShouldSendStatisticsFunc = func(ctx context.Context, frequency time.Duration, cfg config.FleetConfig, license *fleet.LicenseInfo) (fleet.StatisticsPayload, bool, error) {
		return fleet.StatisticsPayload{}, false, nil
	}
	recorded := false
	ds.RecordStatisticsSentFunc = func(ctx context.Context) error {
		recorded = true
		return nil
	}
	cleanedup := false
	ds.CleanupStatisticsFunc = func(ctx context.Context) error {
		cleanedup = true
		return nil
	}

	err := trySendStatistics(context.Background(), ds, fleet.StatisticsFrequency, ts.URL, fleetConfig, &fleet.LicenseInfo{Tier: "premium"})
	require.NoError(t, err)
	assert.False(t, recorded)
	assert.False(t, cleanedup)
	assert.False(t, called)
}

func TestMaybeSendStatisticsSkipsIfNotConfigured(t *testing.T) {
	ds := new(mock.Store)

	fleetConfig := config.FleetConfig{Osquery: config.OsqueryConfig{DetailUpdateInterval: 1 * time.Hour}}

	called := false

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer ts.Close()

	ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
		return &fleet.AppConfig{}, nil
	}

	err := trySendStatistics(context.Background(), ds, fleet.StatisticsFrequency, ts.URL, fleetConfig, &fleet.LicenseInfo{Tier: "premium"})
	require.NoError(t, err)
	assert.False(t, called)
}

func TestAutomationsSchedule(t *testing.T) {
	ds := new(safeStore)

	endpointCalled := int32(0)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&endpointCalled, 1)
	}))
	defer ts.Close()

	ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
		return &fleet.AppConfig{
			WebhookSettings: fleet.WebhookSettings{
				HostStatusWebhook: fleet.HostStatusWebhookSettings{
					Enable:         true,
					DestinationURL: ts.URL,
					HostPercentage: 43,
					DaysCount:      2,
				},
				Interval: fleet.Duration{Duration: 2 * time.Second},
			},
		}, nil
	}
	ds.LockFunc = func(ctx context.Context, name string, owner string, expiration time.Duration) (bool, error) {
		return true, nil
	}
	ds.UnlockFunc = func(ctx context.Context, name string, owner string) error {
		return nil
	}

	calledOnce := make(chan struct{})
	calledTwice := make(chan struct{})
	ds.TotalAndUnseenHostsSinceFunc = func(ctx context.Context, daysCount int) (int, int, error) {
		defer func() {
			select {
			case <-calledOnce:
				select {
				case <-calledTwice:
				default:
					close(calledTwice)
				}
			default:
				close(calledOnce)
			}
		}()
		return 10, 6, nil
	}

	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	failingPoliciesSet := service.NewMemFailingPolicySet()
	startAutomationsSchedule(ctx, "test_instance", ds, kitlog.NewNopLogger(), 5*time.Minute, failingPoliciesSet)

	<-calledOnce
	time.Sleep(1 * time.Second)
	assert.Equal(t, int32(1), atomic.LoadInt32(&endpointCalled))
	<-calledTwice
	time.Sleep(1 * time.Second)
	assert.GreaterOrEqual(t, int32(2), atomic.LoadInt32(&endpointCalled))
}

func TestCronVulnerabilitiesCreatesDatabasesPath(t *testing.T) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	ds := new(mock.Store)
	ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
		return &fleet.AppConfig{
			Features: fleet.Features{EnableSoftwareInventory: true},
		}, nil
	}
	ds.LockFunc = func(ctx context.Context, name string, owner string, expiration time.Duration) (bool, error) {
		return true, nil
	}
	ds.UnlockFunc = func(ctx context.Context, name string, owner string) error {
		return nil
	}
	ds.InsertCVEMetaFunc = func(ctx context.Context, x []fleet.CVEMeta) error {
		return nil
	}
	ds.AllSoftwareWithoutCPEIteratorFunc = func(ctx context.Context, excludedPlatforms []string) (fleet.SoftwareIterator, error) {
		// we should not get this far before we see the directory being created
		return nil, errors.New("shouldn't happen")
	}
	ds.OSVersionsFunc = func(ctx context.Context, teamID *uint, platform *string, name *string, version *string) (*fleet.OSVersions, error) {
		return &fleet.OSVersions{}, nil
	}
	ds.SyncHostsSoftwareFunc = func(ctx context.Context, updatedAt time.Time) error {
		return nil
	}

	vulnPath := filepath.Join(t.TempDir(), "something")
	require.NoDirExists(t, vulnPath)

	config := config.VulnerabilitiesConfig{
		DatabasesPath:         vulnPath,
		Periodicity:           10 * time.Second,
		CurrentInstanceChecks: "auto",
	}
	// Use schedule to test that the schedule does indeed call cronVulnerabilities.
	startVulnerabilitiesSchedule(ctx, "test_instance", ds, kitlog.NewNopLogger(), &config, &fleet.LicenseInfo{Tier: "premium"})

	require.Eventually(t, func() bool {
		info, err := os.Lstat(vulnPath)
		if err != nil {
			return false
		}
		if !info.IsDir() {
			return false
		}
		return true
	}, 5*time.Minute, 30*time.Second)
}

func TestScanVulnerabilitiesMkdirFailsIfVulnPathIsFile(t *testing.T) {
	logger := kitlog.NewNopLogger()
	logger = level.NewFilter(logger, level.AllowDebug())

	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	appConfig := &fleet.AppConfig{
		Features: fleet.Features{EnableSoftwareInventory: true},
	}
	ds := new(mock.Store)

	// creating a file with the same path should result in an error when creating the directory
	fileVulnPath := filepath.Join(t.TempDir(), "somefile")
	_, err := os.Create(fileVulnPath)
	require.NoError(t, err)

	config := config.VulnerabilitiesConfig{
		DatabasesPath:         fileVulnPath,
		Periodicity:           10 * time.Second,
		CurrentInstanceChecks: "auto",
	}

	err = scanVulnerabilities(ctx, ds, logger, &config, appConfig, fileVulnPath, &fleet.LicenseInfo{Tier: "premium"})
	require.ErrorContains(t, err, "create vulnerabilities databases directory: mkdir")
}

func TestCronVulnerabilitiesSkipMkdirIfDisabled(t *testing.T) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	ds := new(mock.Store)
	ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
		// features.enable_software_inventory is false
		return &fleet.AppConfig{}, nil
	}
	ds.LockFunc = func(ctx context.Context, name string, owner string, expiration time.Duration) (bool, error) {
		return true, nil
	}
	ds.UnlockFunc = func(ctx context.Context, name string, owner string) error {
		return nil
	}
	ds.SyncHostsSoftwareFunc = func(ctx context.Context, updatedAt time.Time) error {
		return nil
	}

	vulnPath := filepath.Join(t.TempDir(), "something")
	require.NoDirExists(t, vulnPath)

	config := config.VulnerabilitiesConfig{
		DatabasesPath:         vulnPath,
		Periodicity:           10 * time.Second,
		CurrentInstanceChecks: "1",
	}

	// Use schedule to test that the schedule does indeed call cronVulnerabilities.
	startVulnerabilitiesSchedule(ctx, "test_instance", ds, kitlog.NewNopLogger(), &config, &fleet.LicenseInfo{Tier: "premium"})

	// Every cron tick is 10 seconds ... here we just wait for a loop interation and assert the vuln
	// dir. was not created.
	require.Eventually(t, func() bool {
		_, err := os.Stat(vulnPath)
		return os.IsNotExist(err)
	}, 24*time.Second, 12*time.Second)
}

// TestCronAutomationsLockDuration tests that the Lock method is being called
// for the current automation crons and that their duration is equal to the current
// schedule interval.
func TestAutomationsScheduleLockDuration(t *testing.T) {
	ds := new(safeStore)
	expectedInterval := 1 * time.Second

	intitalConfigLoaded := make(chan struct{}, 1)
	ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
		ac := fleet.AppConfig{
			WebhookSettings: fleet.WebhookSettings{
				Interval: fleet.Duration{Duration: 1 * time.Hour},
			},
		}
		select {
		case <-intitalConfigLoaded:
			ac.WebhookSettings.Interval = fleet.Duration{Duration: expectedInterval}
		default:
			// initial config
			close(intitalConfigLoaded)
		}
		return &ac, nil
	}
	hostStatus := make(chan struct{})
	hostStatusClosed := false
	failingPolicies := make(chan struct{})
	failingPoliciesClosed := false
	unknownName := false
	ds.LockFunc = func(ctx context.Context, name string, owner string, expiration time.Duration) (bool, error) {
		if expiration != expectedInterval {
			return false, nil
		}
		switch name {
		case "automations":
			if !hostStatusClosed {
				close(hostStatus)
				hostStatusClosed = true
			}
			if !failingPoliciesClosed {
				close(failingPolicies)
				failingPoliciesClosed = true
			}
		default:
			unknownName = true
		}
		return true, nil
	}
	ds.UnlockFunc = func(context.Context, string, string) error {
		return nil
	}

	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	startAutomationsSchedule(ctx, "test_instance", ds, kitlog.NewNopLogger(), 1*time.Second, service.NewMemFailingPolicySet())

	select {
	case <-failingPolicies:
	case <-time.After(5 * time.Second):
		t.Error("failing policies timeout")
	}
	select {
	case <-hostStatus:
	case <-time.After(5 * time.Second):
		t.Error("host status timeout")
	}
	require.False(t, unknownName)
}

func TestAutomationsScheduleIntervalChange(t *testing.T) {
	ds := new(safeStore)

	interval := struct {
		sync.Mutex
		value time.Duration
	}{
		value: 5 * time.Hour,
	}
	configLoaded := make(chan struct{}, 1)

	ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
		select {
		case configLoaded <- struct{}{}:
		default:
			// OK
		}

		interval.Lock()
		defer interval.Unlock()

		return &fleet.AppConfig{
			WebhookSettings: fleet.WebhookSettings{
				Interval: fleet.Duration{Duration: interval.value},
			},
		}, nil
	}

	lockCalled := make(chan struct{}, 1)
	ds.LockFunc = func(ctx context.Context, name string, owner string, expiration time.Duration) (bool, error) {
		select {
		case lockCalled <- struct{}{}:
		default:
			// OK
		}
		return true, nil
	}
	ds.UnlockFunc = func(context.Context, string, string) error {
		return nil
	}

	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	startAutomationsSchedule(ctx, "test_instance", ds, kitlog.NewNopLogger(), 200*time.Millisecond, service.NewMemFailingPolicySet())

	// wait for config to be called once by startAutomationsSchedule and again by configReloadFunc
	for c := 0; c < 2; c++ {
		select {
		case <-configLoaded:
		case <-time.After(5 * time.Second):
			t.Fatal("timeout: initial config load")
		}
	}

	interval.Lock()
	interval.value = 1 * time.Second
	interval.Unlock()

	select {
	case <-lockCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: interval change did not trigger lock call")
	}
}

func TestBasicAuthHandler(t *testing.T) {
	for _, tc := range []struct {
		name           string
		username       string
		password       string
		passes         bool
		noBasicAuthSet bool
	}{
		{
			name:     "good-credentials",
			username: "foo",
			password: "bar",
			passes:   true,
		},
		{
			name:     "empty-credentials",
			username: "",
			password: "",
			passes:   false,
		},
		{
			name:           "no-basic-auth-set",
			username:       "",
			password:       "",
			noBasicAuthSet: true,
			passes:         false,
		},
		{
			name:     "wrong-username",
			username: "foo1",
			password: "bar",
			passes:   false,
		},
		{
			name:     "wrong-password",
			username: "foo",
			password: "bar1",
			passes:   false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pass := false
			h := basicAuthHandler("foo", "bar", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				pass = true
				w.WriteHeader(http.StatusOK)
			}))

			r, err := http.NewRequest("GET", "", nil)
			require.NoError(t, err)

			if !tc.noBasicAuthSet {
				r.SetBasicAuth(tc.username, tc.password)
			}

			var w httptest.ResponseRecorder
			h.ServeHTTP(&w, r)

			if pass != tc.passes {
				t.Fatal("unexpected pass")
			}

			expStatusCode := http.StatusUnauthorized
			if pass {
				expStatusCode = http.StatusOK
			}
			require.Equal(t, w.Result().StatusCode, expStatusCode)
		})
	}
}

func TestDebugMux(t *testing.T) {
	h1 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h2 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) })

	cases := []struct {
		desc string
		mux  debugMux
		tok  string
		want int
	}{
		{
			"only fleet auth handler, no token",
			debugMux{fleetAuthenticatedHandler: h1},
			"",
			200,
		},
		{
			"only fleet auth handler, with token",
			debugMux{fleetAuthenticatedHandler: h1},
			"token",
			200,
		},
		{
			"both handlers, no token",
			debugMux{fleetAuthenticatedHandler: h1, tokenAuthenticatedHandler: h2},
			"",
			200,
		},
		{
			"both handlers, with token",
			debugMux{fleetAuthenticatedHandler: h1, tokenAuthenticatedHandler: h2},
			"token",
			400,
		},
	}

	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			path := "/debug/pprof"
			if c.tok != "" {
				path += "?token=" + c.tok
			}
			req := httptest.NewRequest("GET", path, nil)
			res := httptest.NewRecorder()
			c.mux.ServeHTTP(res, req)
			require.Equal(t, c.want, res.Code)
		})
	}
}
