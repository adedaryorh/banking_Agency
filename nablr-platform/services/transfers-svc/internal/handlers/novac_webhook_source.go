package handlers

import (
	"net"
	"net/netip"
	"strings"

	"github.com/gin-gonic/gin"
)

// WebhookSourceGuard restricts provider callbacks to configured source IPs.
// Proxy headers are considered only when explicitly enabled; the edge proxy
// must overwrite client-supplied forwarding headers when this is enabled.
type WebhookSourceGuard struct {
	allowed    map[netip.Addr]struct{}
	trustProxy bool
}

func NewWebhookSourceGuard(csv string, trustProxy bool) *WebhookSourceGuard {
	g := &WebhookSourceGuard{allowed: make(map[netip.Addr]struct{}), trustProxy: trustProxy}
	for _, value := range strings.Split(csv, ",") {
		if addr, err := netip.ParseAddr(strings.TrimSpace(value)); err == nil {
			g.allowed[addr.Unmap()] = struct{}{}
		}
	}
	return g
}

func (g *WebhookSourceGuard) Enabled() bool { return g != nil && len(g.allowed) > 0 }

func (g *WebhookSourceGuard) Allowed(c *gin.Context) bool {
	if !g.Enabled() {
		return true
	}
	addr, ok := g.clientAddr(c)
	if !ok {
		return false
	}
	_, ok = g.allowed[addr.Unmap()]
	return ok
}

func (g *WebhookSourceGuard) clientAddr(c *gin.Context) (netip.Addr, bool) {
	if g.trustProxy {
		if value := strings.TrimSpace(c.GetHeader("X-Real-IP")); value != "" {
			if addr, err := netip.ParseAddr(value); err == nil {
				return addr, true
			}
		}
		if value := strings.TrimSpace(c.GetHeader("X-Forwarded-For")); value != "" {
			first, _, _ := strings.Cut(value, ",")
			if addr, err := netip.ParseAddr(strings.TrimSpace(first)); err == nil {
				return addr, true
			}
		}
	}
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		host = c.Request.RemoteAddr
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(host))
	return addr, err == nil
}
