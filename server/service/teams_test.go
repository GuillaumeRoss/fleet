package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/fleetdm/fleet/v4/server/contexts/viewer"
	"github.com/fleetdm/fleet/v4/server/fleet"
	"github.com/fleetdm/fleet/v4/server/mock"
	"github.com/fleetdm/fleet/v4/server/ptr"
	"github.com/stretchr/testify/require"
)

func TestTeamAuth(t *testing.T) {
	ds := new(mock.Store)
	license := &fleet.LicenseInfo{Tier: fleet.TierPremium, Expiration: time.Now().Add(24 * time.Hour)}
	svc := newTestService(t, ds, nil, nil, &TestServerOpts{License: license, SkipCreateTestUsers: true})

	ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
		return &fleet.AppConfig{}, nil
	}
	ds.NewTeamFunc = func(ctx context.Context, team *fleet.Team) (*fleet.Team, error) {
		return &fleet.Team{}, nil
	}
	ds.NewActivityFunc = func(ctx context.Context, user *fleet.User, activityType string, details *map[string]interface{}) error {
		return nil
	}
	ds.TeamFunc = func(ctx context.Context, tid uint) (*fleet.Team, error) {
		return &fleet.Team{}, nil
	}
	ds.SaveTeamFunc = func(ctx context.Context, team *fleet.Team) (*fleet.Team, error) {
		return &fleet.Team{}, nil
	}
	ds.ListUsersFunc = func(ctx context.Context, opt fleet.UserListOptions) ([]*fleet.User, error) {
		return nil, nil
	}
	ds.ListTeamsFunc = func(ctx context.Context, filter fleet.TeamFilter, opt fleet.ListOptions) ([]*fleet.Team, error) {
		return nil, nil
	}
	ds.DeleteTeamFunc = func(ctx context.Context, tid uint) error {
		return nil
	}
	ds.TeamEnrollSecretsFunc = func(ctx context.Context, teamID uint) ([]*fleet.EnrollSecret, error) {
		return nil, nil
	}
	ds.ApplyEnrollSecretsFunc = func(ctx context.Context, teamID *uint, secrets []*fleet.EnrollSecret) error {
		return nil
	}
	ds.TeamByNameFunc = func(ctx context.Context, name string) (*fleet.Team, error) {
		switch name {
		case "team1":
			return &fleet.Team{ID: 1}, nil
		default:
			return &fleet.Team{ID: 2}, nil
		}
	}

	testCases := []struct {
		name                       string
		user                       *fleet.User
		shouldFailTeamWrite        bool
		shouldFailGlobalWrite      bool
		shouldFailRead             bool
		shouldFailTeamSecretsWrite bool
	}{
		{
			"global admin",
			&fleet.User{GlobalRole: ptr.String(fleet.RoleAdmin)},
			false,
			false,
			false,
			false,
		},
		{
			"global maintainer",
			&fleet.User{GlobalRole: ptr.String(fleet.RoleMaintainer)},
			true,
			true,
			false,
			false,
		},
		{
			"global observer",
			&fleet.User{GlobalRole: ptr.String(fleet.RoleObserver)},
			true,
			true,
			true,
			true,
		},
		{
			"team admin, belongs to team",
			&fleet.User{Teams: []fleet.UserTeam{{Team: fleet.Team{ID: 1}, Role: fleet.RoleAdmin}}},
			false,
			true,
			false,
			false,
		},
		{
			"team maintainer, belongs to team",
			&fleet.User{Teams: []fleet.UserTeam{{Team: fleet.Team{ID: 1}, Role: fleet.RoleMaintainer}}},
			true,
			true,
			false,
			false,
		},
		{
			"team observer, belongs to team",
			&fleet.User{Teams: []fleet.UserTeam{{Team: fleet.Team{ID: 1}, Role: fleet.RoleObserver}}},
			true,
			true,
			true,
			true,
		},
		{
			"team admin, DOES NOT belong to team",
			&fleet.User{Teams: []fleet.UserTeam{{Team: fleet.Team{ID: 2}, Role: fleet.RoleAdmin}}},
			true,
			true,
			true,
			true,
		},
		{
			"team maintainer, DOES NOT belong to team",
			&fleet.User{Teams: []fleet.UserTeam{{Team: fleet.Team{ID: 2}, Role: fleet.RoleMaintainer}}},
			true,
			true,
			true,
			true,
		},
		{
			"team observer, DOES NOT belong to team",
			&fleet.User{Teams: []fleet.UserTeam{{Team: fleet.Team{ID: 2}, Role: fleet.RoleObserver}}},
			true,
			true,
			true,
			true,
		},
	}
	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			ctx := viewer.NewContext(context.Background(), viewer.Viewer{User: tt.user})

			_, err := svc.NewTeam(ctx, fleet.TeamPayload{Name: ptr.String("name")})
			checkAuthErr(t, tt.shouldFailGlobalWrite, err)

			_, err = svc.ModifyTeam(ctx, 1, fleet.TeamPayload{Name: ptr.String("othername")})
			checkAuthErr(t, tt.shouldFailTeamWrite, err)

			_, err = svc.ModifyTeamAgentOptions(ctx, 1, nil, fleet.ApplySpecOptions{})
			checkAuthErr(t, tt.shouldFailTeamWrite, err)

			_, err = svc.AddTeamUsers(ctx, 1, []fleet.TeamUser{})
			checkAuthErr(t, tt.shouldFailTeamWrite, err)

			_, err = svc.DeleteTeamUsers(ctx, 1, []fleet.TeamUser{})
			checkAuthErr(t, tt.shouldFailTeamWrite, err)

			_, err = svc.ListTeamUsers(ctx, 1, fleet.ListOptions{})
			checkAuthErr(t, tt.shouldFailRead, err)

			_, err = svc.ListTeams(ctx, fleet.ListOptions{})
			checkAuthErr(t, false, err) // everybody can do this

			_, err = svc.GetTeam(ctx, 1)
			checkAuthErr(t, tt.shouldFailRead, err)

			err = svc.DeleteTeam(ctx, 1)
			checkAuthErr(t, tt.shouldFailTeamWrite, err)

			_, err = svc.TeamEnrollSecrets(ctx, 1)
			checkAuthErr(t, tt.shouldFailRead, err)

			_, err = svc.ModifyTeamEnrollSecrets(ctx, 1, []fleet.EnrollSecret{{Secret: "newteamsecret", CreatedAt: time.Now()}})
			checkAuthErr(t, tt.shouldFailTeamSecretsWrite, err)

			err = svc.ApplyTeamSpecs(ctx, []*fleet.TeamSpec{{Name: "team1"}}, fleet.ApplySpecOptions{})
			checkAuthErr(t, tt.shouldFailTeamWrite, err)
		})
	}
}

func TestApplyTeamSpecs(t *testing.T) {
	ds := new(mock.Store)
	license := &fleet.LicenseInfo{Tier: fleet.TierPremium, Expiration: time.Now().Add(24 * time.Hour)}
	svc := newTestService(t, ds, nil, nil, &TestServerOpts{License: license, SkipCreateTestUsers: true})
	user := &fleet.User{GlobalRole: ptr.String(fleet.RoleAdmin)}
	ctx := viewer.NewContext(context.Background(), viewer.Viewer{User: user})
	baseFeatures := fleet.Features{
		EnableHostUsers:         true,
		EnableSoftwareInventory: true,
		AdditionalQueries:       ptr.RawMessage(json.RawMessage(`{"foo": "bar"}`)),
	}

	mkspec := func(s string) *json.RawMessage {
		return ptr.RawMessage(json.RawMessage(s))
	}

	t.Run("Features for new teams", func(t *testing.T) {
		cases := []struct {
			name   string
			spec   *json.RawMessage
			global fleet.Features
			result fleet.Features
		}{
			{
				name:   "no spec features uses global config as defaults",
				spec:   nil,
				global: baseFeatures,
				result: baseFeatures,
			},
			{
				name:   "missing spec features uses new config default values",
				spec:   mkspec(`{"enable_software_inventory": false}`),
				global: baseFeatures,
				result: fleet.Features{
					EnableHostUsers:         true,
					EnableSoftwareInventory: false,
					AdditionalQueries:       nil,
				},
			},
			{
				name:   "defaults can be overwritten",
				spec:   mkspec(`{"enable_host_users": false}`),
				global: baseFeatures,
				result: fleet.Features{
					EnableHostUsers:         false,
					EnableSoftwareInventory: true,
					AdditionalQueries:       nil,
				},
			},
			{
				name: "all config can be changed",
				spec: mkspec(`{
          "enable_host_users": false,
          "enable_software_inventory": false,
          "additional_queries": {"example": "query"}
        }`),
				global: baseFeatures,
				result: fleet.Features{
					EnableHostUsers:         false,
					EnableSoftwareInventory: false,
					AdditionalQueries:       ptr.RawMessage([]byte(`{"example": "query"}`)),
				},
			},
		}

		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				ds.TeamByNameFunc = func(ctx context.Context, name string) (*fleet.Team, error) {
					return nil, sql.ErrNoRows
				}

				ds.AppConfigFunc = func(ctx context.Context) (*fleet.AppConfig, error) {
					return &fleet.AppConfig{Features: tt.global}, nil
				}

				ds.NewTeamFunc = func(ctx context.Context, team *fleet.Team) (*fleet.Team, error) {
					require.Equal(t, "team1", team.Name)
					require.Equal(t, tt.result, team.Config.Features)
					team.ID = 1
					return team, nil
				}

				ds.NewActivityFunc = func(ctx context.Context, user *fleet.User, activityType string, details *map[string]interface{}) error {
					require.Len(t, (*details)["teams"], 1)
					return nil
				}

				err := svc.ApplyTeamSpecs(ctx, []*fleet.TeamSpec{{Name: "team1", Features: tt.spec}}, fleet.ApplySpecOptions{})
				require.NoError(t, err)
			})
		}
	})

	t.Run("Features for existing teams", func(t *testing.T) {
		cases := []struct {
			name   string
			spec   *json.RawMessage
			old    fleet.Features
			result fleet.Features
		}{
			{
				name:   "no spec features uses old config",
				spec:   nil,
				old:    baseFeatures,
				result: baseFeatures,
			},
			{
				name: "missing spec features uses new config default values",
				spec: mkspec(`{"enable_software_inventory": false}`),
				old:  baseFeatures,
				result: fleet.Features{
					EnableHostUsers:         true,
					EnableSoftwareInventory: false,
					AdditionalQueries:       nil,
				},
			},
			{
				name: "config has defaults based on what are the global defaults",
				spec: mkspec(`{"additional_queries": {}}`),
				old:  baseFeatures,
				result: fleet.Features{
					EnableHostUsers:         true,
					EnableSoftwareInventory: true,
					AdditionalQueries:       nil,
				},
			},
			{
				name: "defaults can be overwritten",
				spec: mkspec(`{"enable_host_users": false}`),
				old:  baseFeatures,
				result: fleet.Features{
					EnableHostUsers:         false,
					EnableSoftwareInventory: false,
					AdditionalQueries:       nil,
				},
			},
			{
				name: "all config can be changed",
				spec: mkspec(`{
          "enable_host_users": false,
          "enable_software_inventory": true,
          "additional_queries": {"example": "query"}
        }`),
				old: baseFeatures,
				result: fleet.Features{
					EnableHostUsers:         false,
					EnableSoftwareInventory: true,
					AdditionalQueries:       ptr.RawMessage([]byte(`{"example": "query"}`)),
				},
			},
		}

		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				ds.TeamByNameFunc = func(ctx context.Context, name string) (*fleet.Team, error) {
					return &fleet.Team{Config: fleet.TeamConfig{Features: tt.old}}, nil
				}

				ds.SaveTeamFunc = func(ctx context.Context, team *fleet.Team) (*fleet.Team, error) {
					return &fleet.Team{}, nil
				}

				ds.NewActivityFunc = func(ctx context.Context, user *fleet.User, activityType string, details *map[string]interface{}) error {
					require.Len(t, (*details)["teams"], 1)
					return nil
				}

				err := svc.ApplyTeamSpecs(ctx, []*fleet.TeamSpec{{Name: "team1", Features: tt.spec}}, fleet.ApplySpecOptions{})
				require.NoError(t, err)
			})
		}
	})
}
