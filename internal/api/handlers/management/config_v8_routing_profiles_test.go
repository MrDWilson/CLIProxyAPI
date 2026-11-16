package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigV8RoutingProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("config-version: 8\naccess:\n  api-keys: [fixture-key]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	handler := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.PUT("/v8/management/config/*path", handler.ConfigV8)
	router.GET("/v8/management/config/*path", handler.ConfigV8)
	router.PATCH("/v8/management/config", handler.ConfigV8)
	put := func(path, body string, want int) {
		t.Helper()
		r := httptest.NewRecorder()
		router.ServeHTTP(r, httptest.NewRequest(http.MethodPut, "/v8/management/config/"+path, strings.NewReader(body)))
		if r.Code != want {
			t.Fatalf("%s: %d: %s", path, r.Code, r.Body.String())
		}
	}
	put("routing/profiles", `{"default":{"auths":{"account1.json":{"priority":20}}},"secondary":{"allowed-auths":["account1.json","account4.json"],"excluded-models":[]}}`, 200)
	patched := httptest.NewRecorder()
	router.ServeHTTP(patched, httptest.NewRequest(http.MethodPatch, "/v8/management/config", strings.NewReader(`{"access":{"api-key-profiles":{"fixture-key":"secondary"}}}`)))
	if patched.Code != http.StatusOK {
		t.Fatalf("dashboard assignment PATCH: %d: %s", patched.Code, patched.Body.String())
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.APIKeyProfiles["fixture-key"] != "secondary" || loaded.Routing.Profiles["secondary"].ExcludedModels == nil {
		t.Fatal("policy save lost assignment or explicit empty override")
	}
	put("routing/profiles", `{"default":{}}`, 400)
	put("access/api-key-profiles", `{"fixture-key":"missing"}`, 400)
}
