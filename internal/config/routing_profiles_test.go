package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRoutingProfilesV8RoundTripAndInheritance(t *testing.T) {
	data := []byte(`config-version: 8
access:
  api-keys: [master-key, secondary-key]
  api-key-profiles:
    secondary-key: secondary
routing:
  profiles:
    default:
      excluded-models: ["blocked-*"]
      auths:
        account1.json: {priority: 40}
        account2.json: {priority: 30}
    secondary:
      allowed-auths: [account1.json, account4.json]
      excluded-models: []
      auths:
        account1.json: {priority: 10}
        account2.json: {disabled: true}
        account4.json: {priority: 50}
`)
	if err := ValidateV8Config(data); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	p, ok := cfg.EffectiveRoutingProfile("secondary")
	if !ok || *p.Auths["account1.json"].Priority != 10 || *p.Auths["account2.json"].Priority != 30 || !*p.Auths["account2.json"].Disabled {
		t.Fatalf("incorrect inheritance: %#v", p)
	}
	if p.ExcludedModels == nil || len(*p.ExcludedModels) != 0 {
		t.Fatal("explicit empty list must clear inherited exclusions")
	}
	encoded, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _, err = NormalizeConfigLayout(encoded, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateV8Config(encoded); err != nil {
		t.Fatal(err)
	}
	var decoded Config
	if err := yaml.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	p, _ = decoded.EffectiveRoutingProfile("secondary")
	if p.ExcludedModels == nil || len(*p.ExcludedModels) != 0 || decoded.APIKeyProfiles["secondary-key"] != "secondary" {
		t.Fatalf("roundtrip lost policy: %s", encoded)
	}
	clone := cfg.CloneForRuntime()
	*clone.Routing.Profiles["default"].Auths["account1.json"].Priority = 99
	if *cfg.Routing.Profiles["default"].Auths["account1.json"].Priority != 40 {
		t.Fatal("profile clone aliases source")
	}
}

func TestRoutingProfilesRejectUnknownProfileWithoutKeyDisclosure(t *testing.T) {
	cfg := &Config{SDKConfig: SDKConfig{APIKeyProfiles: map[string]string{"secret-client-key": "missing"}}}
	err := cfg.ValidateRoutingProfiles()
	if err == nil || strings.Contains(err.Error(), "secret-client-key") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRoutingProfilesRejectHomeRouting(t *testing.T) {
	cfg := &Config{Routing: RoutingConfig{Profiles: map[string]RoutingProfile{"default": {}}}}
	cfg.Home.Enabled = true
	if err := cfg.ValidateRoutingProfiles(); err == nil || !strings.Contains(err.Error(), "CLIProxyAPIHome") {
		t.Fatalf("expected Home incompatibility error, got %v", err)
	}
}
