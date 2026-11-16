package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func profileInt(v int) *int             { return &v }
func profileBool(v bool) *bool          { return &v }
func profileList(v ...string) *[]string { return &v }

func profileTestManager(t *testing.T, selector Selector) (*Manager, *config.Config, string) {
	t.Helper()
	m := NewManager(nil, selector, nil)
	m.RegisterExecutor(schedulerProviderTestExecutor{provider: "claude"})
	model := "profile-model-" + t.Name()
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("account%d.json", i)
		a := &Auth{ID: id, FileName: id, Provider: "claude", Status: StatusActive, Attributes: map[string]string{"priority": fmt.Sprint(50 - i*10)}}
		if _, err := m.Register(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeyProfiles: map[string]string{"secondary-key": "secondary"}}, Routing: config.RoutingConfig{Profiles: map[string]config.RoutingProfile{
		"default":   {},
		"secondary": {AllowedAuths: profileList("account1.json", "account4.json"), Auths: map[string]config.RoutingAuthOverride{"account1.json": {Priority: profileInt(10)}, "account4.json": {Priority: profileInt(90)}}},
	}}}
	m.SetConfig(cfg)
	return m, cfg, model
}

func TestRoutingProfilesPriorityIsolationAndFailover(t *testing.T) {
	for _, kind := range []string{"round-robin", "fill-first", "weighted", "intelligent-fill"} {
		t.Run(kind, func(t *testing.T) {
			var selector Selector = &RoundRobinSelector{}
			switch kind {
			case "fill-first":
				selector = &FillFirstSelector{}
			case "weighted":
				selector = &WeightedRoundRobinSelector{}
			case "intelligent-fill":
				selector = NewIntelligentFillSelector(nil)
			}
			m, _, model := profileTestManager(t, selector)
			secondary := usage.WithAPIKey(context.Background(), "secondary-key")
			for _, mixed := range []bool{false, true} {
				pick := func(ctx context.Context, tried map[string]struct{}, opts cliproxyexecutor.Options) (*Auth, error) {
					if mixed {
						a, _, _, err := m.pickNextMixed(ctx, []string{"claude"}, model, opts, tried)
						return a, err
					}
					a, _, err := m.pickNext(ctx, "claude", model, opts, tried)
					return a, err
				}
				a, err := pick(secondary, nil, cliproxyexecutor.Options{})
				if err != nil || a.ID != "account4.json" {
					t.Fatalf("secondary: %v %v", a, err)
				}
				a, err = pick(context.Background(), nil, cliproxyexecutor.Options{})
				if err != nil || a.ID != "account1.json" {
					t.Fatalf("master: %v %v", a, err)
				}
				a, err = pick(secondary, map[string]struct{}{"account4.json": {}}, cliproxyexecutor.Options{})
				if err != nil || a.ID != "account1.json" {
					t.Fatalf("failover: %v %v", a, err)
				}
				a, err = pick(secondary, map[string]struct{}{"account4.json": {}, "account1.json": {}}, cliproxyexecutor.Options{})
				if err == nil || a != nil {
					t.Fatal("failover escaped allowlist")
				}
				a, err = pick(secondary, nil, cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: "account2.json"}})
				if err == nil || a != nil {
					t.Fatal("pinned auth escaped allowlist")
				}
			}
			stored, _ := m.GetByID("account4.json")
			if stored.Attributes["priority"] != "10" {
				t.Fatal("override mutated shared credential")
			}
		})
	}
}

func TestRoutingProfilesConcurrentKeys(t *testing.T) {
	m, _, model := profileTestManager(t, &FillFirstSelector{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := context.Background()
			want := "account1.json"
			if i%2 == 0 {
				ctx = usage.WithAPIKey(ctx, "secondary-key")
				want = "account4.json"
			}
			a, _, err := m.pickNext(ctx, "claude", model, cliproxyexecutor.Options{}, nil)
			if err != nil || a == nil || a.ID != want {
				t.Errorf("pick = %v, %v; want %s", a, err, want)
			}
		}(i)
	}
	wg.Wait()
}

func TestRoutingProfilesPluginDelegateRetainsRotation(t *testing.T) {
	m, cfg, model := profileTestManager(t, &FillFirstSelector{})
	p := cfg.Routing.Profiles["secondary"]
	p.Auths["account1.json"] = config.RoutingAuthOverride{Priority: profileInt(90)}
	m.SetConfig(cfg)
	m.SetPluginScheduler(&fakePluginScheduler{
		resp:    pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinRoundRobin},
		handled: true,
	})
	ctx := usage.WithAPIKey(context.Background(), "secondary-key")
	var picks []string
	for range 4 {
		a, _, err := m.pickNext(ctx, "claude", model, cliproxyexecutor.Options{}, nil)
		if err != nil || a == nil {
			t.Fatalf("delegated pick = %v, %v", a, err)
		}
		picks = append(picks, a.ID)
	}
	if picks[0] == picks[1] || picks[0] != picks[2] || picks[1] != picks[3] {
		t.Fatalf("delegated round robin did not retain its cursor: %v", picks)
	}
}

func TestRoutingProfilesExclusionsAndHotReload(t *testing.T) {
	m, cfg, model := profileTestManager(t, &FillFirstSelector{})
	ctx := usage.WithAPIKey(context.Background(), "secondary-key")
	p := cfg.Routing.Profiles["secondary"]
	p.ExcludedModels = profileList("profile-model-*")
	cfg.Routing.Profiles["secondary"] = p
	m.SetConfig(cfg)
	req := cliproxyexecutor.Request{Model: model}
	_, err := m.Execute(ctx, []string{"claude"}, req, cliproxyexecutor.Options{})
	assertProfileDenied(t, err)
	_, err = m.ExecuteCount(ctx, []string{"claude"}, req, cliproxyexecutor.Options{})
	assertProfileDenied(t, err)
	_, err = m.ExecuteStream(ctx, []string{"claude"}, req, cliproxyexecutor.Options{})
	assertProfileDenied(t, err)
	models := []map[string]any{{"id": model}}
	if len(m.FilterRoutingModels(ctx, models)) != 0 || len(m.FilterRoutingModels(context.Background(), models)) != 1 {
		t.Fatal("model catalog policy mismatch")
	}
	p.ExcludedModels = nil
	p.Auths["account4.json"] = config.RoutingAuthOverride{Disabled: profileBool(true)}
	cfg.Routing.Profiles["secondary"] = p
	m.SetConfig(cfg)
	a, _, err := m.pickNext(ctx, "claude", model, cliproxyexecutor.Options{}, nil)
	if err != nil || a.ID != "account1.json" {
		t.Fatalf("reload: %v %v", a, err)
	}
	delete(cfg.Routing.Profiles, "secondary")
	m.SetConfig(cfg)
	_, err = m.Execute(ctx, []string{"claude"}, req, cliproxyexecutor.Options{})
	assertProfileDenied(t, err)
}
func assertProfileDenied(t *testing.T, err error) {
	t.Helper()
	e, ok := err.(*Error)
	if !ok || e.HTTPStatus != http.StatusForbidden {
		t.Fatalf("want profile denial, got %v", err)
	}
}

func TestRoutingProfilesSessionIsolationAndPriorityReload(t *testing.T) {
	selector := NewSessionAffinitySelector(&FillFirstSelector{})
	m, cfg, model := profileTestManager(t, selector)
	opts := func() cliproxyexecutor.Options {
		return cliproxyexecutor.Options{Headers: http.Header{"X-Claude-Code-Session-Id": {"same-session"}}}
	}
	master := usage.WithAPIKey(context.Background(), "master-key")
	secondary := usage.WithAPIKey(context.Background(), "secondary-key")
	pick := func(ctx context.Context, want string) {
		t.Helper()
		a, _, err := m.pickNext(ctx, "claude", model, opts(), nil)
		if err != nil || a == nil || a.ID != want {
			t.Fatalf("pick=%v, %v; want %s", a, err, want)
		}
	}
	pick(master, "account1.json")
	pick(secondary, "account4.json")
	pick(master, "account1.json")
	p := cfg.Routing.Profiles["secondary"]
	p.Auths["account1.json"] = config.RoutingAuthOverride{Priority: profileInt(100)}
	cfg.Routing.Profiles["secondary"] = p
	m.SetConfig(cfg)
	pick(secondary, "account1.json")
}

func TestRoutingProfilesBlockResolvedModelAliases(t *testing.T) {
	m, cfg, model := profileTestManager(t, &FillFirstSelector{})
	ctx := usage.WithAPIKey(context.Background(), "secondary-key")
	cfg.OAuthModelAlias = map[string][]config.OAuthModelAlias{"claude": {{Name: model, Alias: "friendly"}}}
	p := cfg.Routing.Profiles["secondary"]
	p.ExcludedModels = profileList(model)
	cfg.Routing.Profiles["secondary"] = p
	m.SetConfig(cfg)
	m.SetOAuthModelAlias(cfg.OAuthModelAlias)
	for _, id := range []string{"account1.json", "account4.json"} {
		registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}, {ID: "friendly"}})
	}
	a, _, err := m.pickNext(ctx, "claude", "friendly", cliproxyexecutor.Options{}, nil)
	if err == nil || a != nil {
		t.Fatal("model alias bypassed profile exclusion")
	}
}
