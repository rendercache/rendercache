// SPDX-License-Identifier: Apache-2.0
package proxy

import (
	"net/http"

	"github.com/rendercache/rendercache/internal/config"
)

// edit changes the settings in use, as a reload would, without validation.
func (g *Gateway) edit(change func(*config.Config)) {
	c := g.conf().cfg
	change(&c)
	g.set.Store(newSettings(c))
}

func (g *Gateway) classify(r *http.Request) Route {
	set := g.conf()
	return ClassifyPaths(r, set.cfg.Bots, set.cfg.BypassPaths)
}
