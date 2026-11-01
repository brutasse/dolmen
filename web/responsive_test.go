package web

import (
	"strings"
	"testing"
)

func TestResponsiveHeader(t *testing.T) {
	css := embeddedAsset(t, "app.css")
	for _, want := range []string{
		"@media (max-width: 720px)",
		".head-inner { flex-wrap: wrap",
		".head-nav { flex: 1 0 100%; flex-wrap: wrap",
		".head-nav .btn, .head-nav .pill, .head-nav .user { white-space: nowrap",
		".head-nav .user { margin-left: auto",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css missing %q", want)
		}
	}
}
