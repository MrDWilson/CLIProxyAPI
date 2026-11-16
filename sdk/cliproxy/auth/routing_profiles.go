package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type requestRoutingProfile struct {
	rules  config.RoutingProfile
	active bool
	valid  bool
}

func (m *Manager) routingProfile(ctx context.Context, opts cliproxyexecutor.Options) requestRoutingProfile {
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil {
		return requestRoutingProfile{valid: true}
	}
	name := "default"
	if key := usage.APIKeyFromContext(ctx); key != "" {
		if assigned, ok := cfg.APIKeyProfiles[key]; ok {
			name = assigned
		}
	} else if scope, _ := opts.Metadata[cliproxyexecutor.CallerScopeMetadataKey].(string); scope != "" {
		// Handler metadata contains a server-generated hash of the authenticated key.
		for key, assigned := range cfg.APIKeyProfiles {
			if session.CallerScope(key) == scope {
				name = assigned
				break
			}
		}
	}
	rules, valid := cfg.EffectiveRoutingProfile(name)
	valid = valid && name != ""
	_, hasDefault := cfg.Routing.Profiles["default"]
	return requestRoutingProfile{rules: rules, active: hasDefault || name != "default", valid: valid}
}

func routingDenied() error {
	return &Error{Code: "routing_profile_denied", Message: "request is not permitted by the API key routing profile", HTTPStatus: http.StatusForbidden}
}

func (m *Manager) validateRequestRoutingProfile(ctx context.Context, model string, opts cliproxyexecutor.Options) error {
	p := m.routingProfile(ctx, opts)
	if !p.valid || (p.active && m.HomeEnabled()) || excludedProfileModel(p.rules.ExcludedModels, model) || excludedProfileModel(p.rules.ExcludedModels, authSelectionModelFromOptions(opts, model)) {
		return routingDenied()
	}
	return nil
}

func authMatchesProfileID(auth *Auth, id string) bool {
	return auth != nil && id != "" && (auth.ID == id || auth.Index == id || auth.FileName == id)
}

// profileCandidate uses a private copy for priority overrides. Stored auth records,
// cooldowns and other requests must never inherit a client's effective priority.
func (m *Manager) profileCandidate(p requestRoutingProfile, auth *Auth, model string) *Auth {
	if auth == nil || !p.valid {
		return nil
	}
	if !p.active {
		return auth
	}
	if list := p.rules.AllowedAuths; list != nil {
		allowed := false
		for _, id := range *list {
			if authMatchesProfileID(auth, id) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil
		}
	}
	var rule config.RoutingAuthOverride
	// Auth IDs are preferred over indexes and filenames when multiple entries match.
	for _, id := range []string{auth.ID, auth.Index, auth.FileName} {
		if r, ok := p.rules.Auths[id]; ok {
			rule = r
			break
		}
	}
	if rule.Disabled != nil && *rule.Disabled {
		return nil
	}
	models := []string{model, rewriteModelForAuth(model, auth), m.selectionModelKeyForAuth(auth, model)}
	models = append(models, m.profileModelCandidates(auth, model)...)
	for _, id := range models {
		if excludedProfileModel(p.rules.ExcludedModels, id) || excludedProfileModel(rule.ExcludedModels, id) {
			return nil
		}
	}
	if rule.Priority == nil {
		return auth
	}
	out := auth.Clone()
	if out.Attributes == nil {
		out.Attributes = make(map[string]string)
	}
	out.Attributes["priority"] = strconv.Itoa(*rule.Priority)
	return out
}

func excludedProfileModel(patterns *[]string, model string) bool {
	if patterns == nil {
		return false
	}
	model = strings.TrimPrefix(strings.TrimSpace(model), "models/")
	base := thinking.ParseSuffix(model).ModelName
	for _, pattern := range *patterns {
		if profileGlob(pattern, model) || (base != "" && profileGlob(pattern, base)) {
			return true
		}
	}
	return false
}

// profileGlob supports only '*', matching the model-exclusion configuration.
func profileGlob(pattern, value string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == value
	}
	if !strings.HasPrefix(value, parts[0]) {
		return false
	}
	value = value[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(value, part)
		if i < 0 {
			return false
		}
		value = value[i+len(part):]
	}
	return strings.HasSuffix(value, parts[len(parts)-1])
}

// FilterRoutingModels hides models that cannot be used through the caller's pool.
// Temporary cooldowns do not remove models from discovery.
func (m *Manager) FilterRoutingModels(ctx context.Context, models []map[string]any) []map[string]any {
	if m == nil {
		return models
	}
	p := m.routingProfile(ctx, cliproxyexecutor.Options{})
	if !p.active && p.valid {
		return models
	}
	out := make([]map[string]any, 0, len(models))
	if !p.valid || m.HomeEnabled() {
		return out
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	reg := registry.GetGlobalRegistry()
	for _, model := range models {
		id, _ := model["id"].(string)
		if id == "" {
			id, _ = model["name"].(string)
		}
		id = strings.TrimPrefix(id, "models/")
		if excludedProfileModel(p.rules.ExcludedModels, id) {
			continue
		}
		for _, auth := range m.auths {
			if auth != nil && !auth.Disabled && auth.Status != StatusDisabled && m.authSupportsRouteModel(reg, auth, id) && m.profileCandidate(p, auth, id) != nil {
				out = append(out, model)
				break
			}
		}
	}
	return out
}

// Resolve all alias targets without advancing the shared model-pool cursor.
func (m *Manager) profileModelCandidates(auth *Auth, model string) []string {
	routing := m.loadAPIKeyModelRouting()
	requested := rewriteModelForAuth(model, auth)
	alias := m.resolveExecutionAliasResultForRequestedWithRouting(routing, auth, requested)
	upstream := executionAliasPoolModel(auth, requested, alias)
	if pool := resolveOpenAICompatUpstreamModelPool(routing.config, auth, upstream); len(pool) > 0 {
		return pool
	}
	return []string{upstream, m.applyAPIKeyModelAliasWithRouting(routing, auth, upstream)}
}

const routingProfileScopeMetadataKey = "routing_profile_scope"

func (m *Manager) withRoutingProfileMetadata(ctx context.Context, opts cliproxyexecutor.Options) cliproxyexecutor.Options {
	p := m.routingProfile(ctx, opts)
	if !p.active {
		return opts
	}
	opts.EnsureMetadata()
	// Include the effective policy so a reload cannot retain a binding chosen
	// under different account priorities. API keys also remain isolated.
	encoded, _ := json.Marshal(p.rules)
	scope, _ := opts.Metadata[cliproxyexecutor.CallerScopeMetadataKey].(string)
	if key := usage.APIKeyFromContext(ctx); key != "" {
		scope = session.CallerScope(key)
	}
	digest := sha256.Sum256(append(encoded, []byte(scope)...))
	opts.Metadata[routingProfileScopeMetadataKey] = hex.EncodeToString(digest[:])
	return opts
}

func routingAffinityModelKey(model string, metadata map[string]any) string {
	if scope, _ := metadata[routingProfileScopeMetadataKey].(string); scope != "" {
		return model + "::profile:" + scope
	}
	return model
}
