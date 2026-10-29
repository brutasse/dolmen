package web

import (
	"strings"
	"testing"
)

func TestRedAccentAndLargerBrand(t *testing.T) {
	css := embeddedAsset(t, "app.css")
	for _, want := range []string{
		"--accent: #DA291C",
		"--accent: light-dark(#DA291C, #ff5540)",
		"font-size: 1.25rem",
		".brand-mark { height: 1.5em",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css missing %q", want)
		}
	}
}

func TestAccentContrast(t *testing.T) {
	for _, tc := range []struct {
		name, foreground, background string
	}{
		{"light links", "#DA291C", "#ffffff"},
		{"dark links", "#ff5540", "#0d1117"},
		{"button text", "#ffffff", "#DA291C"},
	} {
		if ratio := contrastRatio(cssRGB(t, tc.foreground, tc.background), cssRGB(t, tc.background, tc.background)); ratio < 4.5 {
			t.Errorf("%s contrast = %.2f, want at least 4.5", tc.name, ratio)
		}
	}
}

func TestResponsiveHeader(t *testing.T) {
	css := embeddedAsset(t, "app.css")
	for _, want := range []string{
		"@media (max-width: 720px)",
		".head-inner { flex-wrap: wrap",
		".head-nav { flex: 1 0 100%; flex-wrap: wrap",
		".head-nav .user { margin-left: auto",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css missing %q", want)
		}
	}
}
