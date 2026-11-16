package config

import (
	"fmt"
	"strings"
)

// RoutingProfile overlays the default profile without modifying stored credentials.
// Nil slices inherit; an explicit empty slice clears an inherited list.
type RoutingProfile struct {
	AllowedAuths   *[]string                      `yaml:"allowed-auths,omitempty" json:"allowed-auths,omitempty"`
	ExcludedModels *[]string                      `yaml:"excluded-models,omitempty" json:"excluded-models,omitempty"`
	Auths          map[string]RoutingAuthOverride `yaml:"auths,omitempty" json:"auths,omitempty"`
}

type RoutingAuthOverride struct {
	Priority       *int      `yaml:"priority,omitempty" json:"priority,omitempty"`
	Disabled       *bool     `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	ExcludedModels *[]string `yaml:"excluded-models,omitempty" json:"excluded-models,omitempty"`
}

// ValidateRoutingProfiles rejects broken references rather than silently granting
// the default pool when an administrator misspells or removes a profile.
func (cfg *Config) ValidateRoutingProfiles() error {
	if cfg.Home.Enabled && (len(cfg.Routing.Profiles) > 0 || len(cfg.APIKeyProfiles) > 0) {
		return fmt.Errorf("routing profiles require local credential routing; CLIProxyAPIHome is not supported")
	}
	for name, profile := range cfg.Routing.Profiles {
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
			return fmt.Errorf("routing profile names must be nonempty and have no surrounding whitespace")
		}
		for id := range profile.Auths {
			if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) {
				return fmt.Errorf("routing profile %q has an invalid auth identifier", name)
			}
		}
		for _, id := range routingProfileStrings(profile.AllowedAuths) {
			if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) {
				return fmt.Errorf("routing profile %q has an invalid allowed auth identifier", name)
			}
		}
		lists := [][]string{routingProfileStrings(profile.ExcludedModels)}
		for _, rule := range profile.Auths {
			lists = append(lists, routingProfileStrings(rule.ExcludedModels))
		}
		for _, list := range lists {
			for _, model := range list {
				if strings.TrimSpace(model) == "" || strings.TrimSpace(model) != model {
					return fmt.Errorf("routing profile %q has an invalid excluded model", name)
				}
			}
		}
	}
	for key, name := range cfg.APIKeyProfiles {
		if strings.TrimSpace(key) == "" || key != strings.TrimSpace(key) {
			return fmt.Errorf("access.api-key-profiles contains an invalid API key")
		}
		if name == "default" {
			continue
		}
		if _, exists := cfg.Routing.Profiles[name]; !exists {
			return fmt.Errorf("access.api-key-profiles references unknown routing profile %q", name)
		}
	}
	return nil
}

// EffectiveRoutingProfile returns the master defaults overlaid by a named profile.
func (cfg *Config) EffectiveRoutingProfile(name string) (RoutingProfile, bool) {
	base := cfg.Routing.Profiles["default"]
	if name == "" || name == "default" {
		return base, true
	}
	override, ok := cfg.Routing.Profiles[name]
	if !ok {
		return RoutingProfile{}, false
	}
	out := base
	if override.AllowedAuths != nil {
		out.AllowedAuths = override.AllowedAuths
	}
	if override.ExcludedModels != nil {
		out.ExcludedModels = override.ExcludedModels
	}
	out.Auths = make(map[string]RoutingAuthOverride, len(base.Auths)+len(override.Auths))
	for id, rule := range base.Auths {
		out.Auths[id] = rule
	}
	for id, rule := range override.Auths {
		merged := out.Auths[id]
		if rule.Priority != nil {
			merged.Priority = rule.Priority
		}
		if rule.Disabled != nil {
			merged.Disabled = rule.Disabled
		}
		if rule.ExcludedModels != nil {
			merged.ExcludedModels = rule.ExcludedModels
		}
		out.Auths[id] = merged
	}
	return out, true
}

func routingProfileStrings(value *[]string) []string {
	if value == nil {
		return nil
	}
	return *value
}
