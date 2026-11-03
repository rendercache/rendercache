// SPDX-License-Identifier: Apache-2.0
package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/rendercache/rendercache/internal/config"
	"github.com/rendercache/rendercache/internal/verify"
)

// Reload says what a reload did.
type Reload struct {
	Applied         []string `json:"applied"`
	RestartRequired []string `json:"restart_required"`
	RangesError     string   `json:"crawler_ranges_error,omitempty"`
}

// reloadable are the settings a running gateway takes from a new
// configuration. Everything else (listeners, origin, storage, Chrome,
// warming) is only read at start, so a change there is reported, not applied.
var reloadable = map[string]bool{
	"bots": true, "bypass_paths": true, "ignore_query_params": true, "trusted_proxies": true,
	"edge_token": true, "admin_token": true, "snapshot_ttl": true,
	"verify_crawlers": true, "unverifiable_crawlers": true,
}

var applying sync.Mutex

// Apply takes the reloadable settings of c and lists the other settings
// that differ and need a restart. c must be valid.
func (g *Gateway) Apply(c config.Config) (Reload, error) {
	if err := c.Validate(); err != nil {
		return Reload{}, err
	}
	applying.Lock()
	defer applying.Unlock()
	old := g.conf().cfg
	next := old
	out := Reload{Applied: []string{}, RestartRequired: []string{}}
	ov, nv, xv := reflect.ValueOf(old), reflect.ValueOf(c), reflect.ValueOf(&next).Elem()
	t := ov.Type()
	for i := 0; i < t.NumField(); i++ {
		name := jsonName(t.Field(i))
		if reflect.DeepEqual(ov.Field(i).Interface(), nv.Field(i).Interface()) {
			continue
		}
		if reloadable[name] {
			xv.Field(i).Set(nv.Field(i))
			out.Applied = append(out.Applied, name)
		} else {
			out.RestartRequired = append(out.RestartRequired, name)
		}
	}
	sort.Strings(out.Applied)
	sort.Strings(out.RestartRequired)
	g.set.Store(newSettings(next))
	return out, nil
}

func jsonName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	for i := 0; i < len(tag); i++ {
		if tag[i] == ',' {
			return tag[:i]
		}
	}
	if tag == "" {
		return f.Name
	}
	return tag
}

// SetVerifier must be called before starting the HTTP listeners.
func (g *Gateway) SetVerifier(v *verify.Verifier) { g.verifier = v }

// SetReloader gives the admin listener a POST /reload. load re-reads the
// configuration the way the process read it at start.
func (g *Gateway) SetReloader(load func(context.Context) (Reload, error)) { g.reloader = load }

func (g *Gateway) reload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if g.reloader == nil {
		w.WriteHeader(http.StatusNotImplemented)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "reload is not available in this process"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	result, err := g.reloader(ctx)
	if err != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "applied": []string{}})
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

func (g *Gateway) verificationStatus() any {
	set := g.conf()
	out := map[string]any{"enabled": set.cfg.VerifyCrawlers, "unverifiable_crawlers": set.cfg.UnverifiableCrawlers}
	if g.verifier != nil {
		out["sources"] = g.verifier.Status()
	}
	return out
}

// RefreshRanges fetches the crawler range lists now, when verification is set up.
func (g *Gateway) RefreshRanges(ctx context.Context) error {
	if g.verifier == nil {
		return nil
	}
	return g.verifier.Refresh(ctx)
}

// Settings returns the configuration in use.
func (g *Gateway) Settings() config.Config { return g.conf().cfg }
