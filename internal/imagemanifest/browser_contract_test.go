package imagemanifest

import (
	"path/filepath"
	"slices"
	"testing"
)

// This is Playwright v1.62.1's ubuntu24.04-x64 tools+chromium set. Browser
// bytes remain consumer-owned; the pinned Chrome archive only launch-tests
// these libraries in the disposable image smoke instance.
var playwrightChromiumUbuntu2404Packages = []string{
	"fonts-freefont-ttf", "fonts-ipafont-gothic", "fonts-liberation",
	"fonts-noto-color-emoji", "fonts-tlwg-loma-otf", "fonts-unifont",
	"fonts-wqy-zenhei", "libasound2t64", "libatk-bridge2.0-0t64",
	"libatk1.0-0t64", "libatspi2.0-0t64", "libcairo2", "libcups2t64",
	"libdbus-1-3", "libdrm2", "libfontconfig1", "libfreetype6", "libgbm1",
	"libglib2.0-0t64", "libnspr4", "libnss3", "libpango-1.0-0",
	"libx11-6", "libxcb1", "libxcomposite1", "libxdamage1", "libxext6",
	"libxfixes3", "libxkbcommon0", "libxrandr2", "xfonts-cyrillic",
	"xfonts-scalable", "xvfb",
}

func TestIntegrationImageOwnsExactPlaywrightChromiumDependencyContract(t *testing.T) {
	integration, err := Load(filepath.Join("..", "..", "config", "golden-image-container-integration.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	standard, err := Load(filepath.Join("..", "..", "config", "golden-image-container.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if integration.Guest.Browser != "chromium" || standard.Guest.Browser != "" {
		t.Fatalf("browser capability: integration=%q standard=%q", integration.Guest.Browser, standard.Guest.Browser)
	}
	if integration.BrowserSmoke.UpstreamRef != "playwright/v1.62.1:chromium:1234:ubuntu24.04-x64" ||
		integration.BrowserSmoke.ArchiveSHA256 != "ae8736ac28bc69278551500f219fc749575648263c43ec5990749eff43b9fcf8" {
		t.Fatalf("browser smoke provenance = %#v", integration.BrowserSmoke)
	}
	for _, pkg := range playwrightChromiumUbuntu2404Packages {
		if !slices.Contains(integration.Guest.Packages, pkg) {
			t.Errorf("integration image omits Playwright Chromium dependency %q", pkg)
		}
		if slices.Contains(standard.Guest.Packages, pkg) {
			t.Errorf("standard image unexpectedly carries browser-only dependency %q", pkg)
		}
	}
}
