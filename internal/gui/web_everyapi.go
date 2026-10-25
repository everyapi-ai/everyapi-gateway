package gui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// WebEveryAPIOnly scopes the standalone product without changing ordinary Magpie.
var WebEveryAPIOnly bool

// WebEveryAPIBase is the validated bootstrap upstream for an unconfigured install.
var WebEveryAPIBase string

func everyAPIOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if WebEveryAPIOnly && (strings.HasPrefix(r.URL.Path, "/api/provider/") || strings.HasPrefix(r.URL.Path, "/api/plugins/") || r.URL.Path == "/api/providers/arrange") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func everyAPIRoutes(mux *http.ServeMux) {
	if !WebEveryAPIOnly {
		return
	}
	mux.HandleFunc("POST /api/everyapi", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Key    string    `json:"key"`
			Region string    `json:"region"`
			Models *[]string `json:"models"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&in); err != nil {
			fail(w, err)
			return
		}
		if in.Region != "" && in.Region != "global" && in.Region != "cn" {
			http.Error(w, "unknown EveryAPI region", http.StatusBadRequest)
			return
		}
		if err := provider.FileError(); err != nil {
			fail(w, err)
			return
		}
		p, err := provider.Find("everyapi")
		if err != nil {
			fresh, err := provider.FromPreset("everyapi")
			if err != nil {
				fail(w, err)
				return
			}
			p = &fresh
			base := WebEveryAPIBase
			if base == "" {
				base = "https://api.everyapi.ai"
			}
			p.Chat, p.Responses, p.Anthropic, p.Gemini = base+"/v1", base+"/v1", base, base+"/v1beta"
		}
		if key := strings.TrimSpace(in.Key); key != "" {
			p.Key = key
			p.Keys = nil
		}
		if p.Key == "" {
			http.Error(w, "EveryAPI API key is required", http.StatusBadRequest)
			return
		}
		if in.Region != "" {
			base := "https://api.everyapi.ai"
			if in.Region == "cn" {
				base = "https://api-cn.everyapi.ai"
			}
			p.Chat, p.Responses, p.Anthropic, p.Gemini = base+"/v1", base+"/v1", base, base+"/v1beta"
		}
		p.ID, p.Name, p.Preset, p.Off = "everyapi", "EveryAPI", "everyapi", false
		if in.Models != nil {
			p.Models, p.NoModels = *in.Models, len(*in.Models) == 0
		}
		if err := provider.Save(*p); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, providersState())
	})
	mux.HandleFunc("POST /api/everyapi/models", func(w http.ResponseWriter, r *http.Request) {
		p, err := provider.Find("everyapi")
		if err != nil {
			fail(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if _, _, err := p.Refetch(ctx); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, providersState())
	})
}
