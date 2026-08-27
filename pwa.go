package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ── PWA Global Build Hook ───────────────────────────────────────────────────

func setupPWAGlobalAssets() {
	_ = os.WriteFile(filepath.Join(distDir, "icon-192.png"), generatePNGIcon(192), 0644)
	_ = os.WriteFile(filepath.Join(distDir, "icon-512.png"), generatePNGIcon(512), 0644)
}

// ── PWA Per-App Processor Hook ──────────────────────────────────────────────

func processAppPWA(content []byte, name, title, description, category, image, distIdx string, pwaPartial []byte) ([]byte, error) {
	if strings.ToLower(extractMeta("Disable-PWA", content)) == "true" || strings.ToLower(extractMeta("Disable-pwa", content)) == "true" {
		return content, nil
	}

	themeColor := extractMeta("Theme-Color", content)

	// Inject PWA UI Banner if available
	out := content
	if len(pwaPartial) > 0 {
		out = injectBytePartials(out, nil, pwaPartial)
	}

	// Inject PWA Meta Tags & ServiceWorker registration
	out = injectPWATags(out, title, themeColor)

	// Write isolated manifest & service worker for this app
	manifestData := generateManifest(name, title, description, category, image)
	appDir := filepath.Dir(distIdx)
	_ = os.WriteFile(filepath.Join(appDir, "manifest.webmanifest"), manifestData, 0644)

	// Copy icons into app directory as well so both ./ and ../ paths resolve cleanly
	if favData, err := os.ReadFile("favicon.svg"); err == nil {
		_ = os.WriteFile(filepath.Join(appDir, "favicon.svg"), favData, 0644)
		_ = os.WriteFile(filepath.Join(appDir, "favicon.ico"), favData, 0644)
	}
	_ = os.WriteFile(filepath.Join(appDir, "icon-192.png"), generatePNGIcon(192), 0644)
	_ = os.WriteFile(filepath.Join(appDir, "icon-512.png"), generatePNGIcon(512), 0644)

	swData := generateServiceWorker(name, content)
	_ = os.WriteFile(filepath.Join(appDir, "sw.js"), swData, 0644)

	return out, nil
}

// ── PNG Icon Rendering ──────────────────────────────────────────────────────

func generatePNGIcon(size int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	radius := float64(size) / 4.0
	center := float64(size) / 2.0

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx := mathMax(0, mathAbs(float64(x)-center+0.5)-(center-radius))
			dy := mathMax(0, mathAbs(float64(y)-center+0.5)-(center-radius))
			if dx*dx+dy*dy <= radius*radius {
				t := float64(x+y) / float64(size*2)
				r := uint8(float64(0x63)*(1-t) + float64(0xa8)*t)
				g := uint8(float64(0x66)*(1-t) + float64(0x55)*t)
				b := uint8(float64(0xf1)*(1-t) + float64(0xf7)*t)
				img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
			}
		}
	}

	padding := size / 4
	gridW := size / 2
	halfW := (gridW - padding/4) / 2

	drawBox := func(bx, by int) {
		for y := by; y < by+halfW; y++ {
			for x := bx; x < bx+halfW; x++ {
				img.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 230})
			}
		}
	}

	offset := padding / 4
	drawBox(padding, padding)
	drawBox(padding+halfW+offset, padding)
	drawBox(padding, padding+halfW+offset)
	drawBox(padding+halfW+offset, padding+halfW+offset)

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func mathMax(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func mathAbs(a float64) float64 {
	if a < 0 {
		return -a
	}
	return a
}

// ── Web Manifest Generator ──────────────────────────────────────────────────

func generateManifest(name, title, description, category, image string) []byte {
	shortName := title
	if len([]rune(shortName)) > 16 {
		shortName = string([]rune(shortName)[:16])
	}

	manifest := map[string]interface{}{
		"name":             title,
		"short_name":       shortName,
		"description":      description,
		"id":               "./",
		"start_url":        "./",
		"scope":            "./",
		"display":          "standalone",
		"display_override": []string{"window-controls-overlay", "standalone", "minimal-ui"},
		"orientation":      "any",
		"background_color": "#0b0f17",
		"theme_color":      "#0b0f17",
		"categories":       []string{strings.ToLower(category), "utilities", "productivity"},
		"icons": []map[string]string{
			{
				"src":     "./icon-192.png",
				"sizes":   "192x192",
				"type":    "image/png",
				"purpose": "any",
			},
			{
				"src":     "./icon-512.png",
				"sizes":   "512x512",
				"type":    "image/png",
				"purpose": "any",
			},
			{
				"src":     "./icon-512.png",
				"sizes":   "512x512",
				"type":    "image/png",
				"purpose": "maskable",
			},
		},
	}

	if image != "" {
		manifest["screenshots"] = []map[string]string{
			{
				"src":         image,
				"sizes":       "1974x1480",
				"type":        "image/png",
				"form_factor": "wide",
				"label":       fmt.Sprintf("%s Desktop", title),
			},
			{
				"src":         image,
				"sizes":       "1974x1480",
				"type":        "image/png",
				"form_factor": "narrow",
				"label":       fmt.Sprintf("%s Mobile", title),
			},
		}
	}

	data, _ := json.MarshalIndent(manifest, "", "  ")
	return data
}

// ── Service Worker Generator ────────────────────────────────────────────────

func generateServiceWorker(name string, appContent []byte) []byte {
	cdnSet := make(map[string]struct{})

	cdnSet["./"] = struct{}{}
	cdnSet["./index.html"] = struct{}{}
	cdnSet["./manifest.webmanifest"] = struct{}{}
	cdnSet["./favicon.svg"] = struct{}{}
	cdnSet["./icon-192.png"] = struct{}{}
	cdnSet["./icon-512.png"] = struct{}{}
	cdnSet["../favicon.svg"] = struct{}{}
	cdnSet["../icon-192.png"] = struct{}{}
	cdnSet["../icon-512.png"] = struct{}{}

	reScriptSrc := regexp.MustCompile(`(?i)<script[^>]+src=["'](https?://[^"']+)["']`)
	reLinkHref := regexp.MustCompile(`(?i)<link[^>]+href=["'](https?://[^"']+)["']`)

	for _, m := range reScriptSrc.FindAllSubmatch(appContent, -1) {
		if len(m) > 1 {
			cdnSet[string(m[1])] = struct{}{}
		}
	}
	for _, m := range reLinkHref.FindAllSubmatch(appContent, -1) {
		if len(m) > 1 {
			urlStr := string(m[1])
			if strings.Contains(urlStr, ".css") || strings.Contains(urlStr, "font") || strings.Contains(urlStr, "unpkg") || strings.Contains(urlStr, "cdnjs") {
				cdnSet[urlStr] = struct{}{}
			}
		}
	}

	var assetList []string
	for asset := range cdnSet {
		assetList = append(assetList, fmt.Sprintf("%q", asset))
	}
	sort.Strings(assetList)
	assetArrayStr := strings.Join(assetList, ",\n    ")

	cacheKey := fmt.Sprintf("gist-%s-v1", name)

	swCode := fmt.Sprintf(`// Gist Mini App Service Worker - %s
const CACHE_NAME = %q;
const PRECACHE_ASSETS = [
    %s
];

self.addEventListener('install', (event) => {
    event.waitUntil(
        caches.open(CACHE_NAME).then(async (cache) => {
            for (const url of PRECACHE_ASSETS) {
                try {
                    const req = new Request(url, { mode: url.startsWith('http') ? 'cors' : 'same-origin' });
                    const res = await fetch(req);
                    if (res && (res.status === 200 || res.type === 'opaque')) {
                        await cache.put(req, res);
                    }
                } catch (e) {}
            }
        }).then(() => self.skipWaiting())
    );
});

self.addEventListener('activate', (event) => {
    event.waitUntil(
        caches.keys().then((keys) => {
            return Promise.all(
                keys.filter((k) => k.startsWith('gist-%s-') && k !== CACHE_NAME)
                    .map((k) => caches.delete(k))
            );
        }).then(() => self.clients.claim())
    );
});

self.addEventListener('fetch', (event) => {
    const req = event.request;
    if (req.method !== 'GET') return;
    if (!req.url.startsWith('http')) return;

    event.respondWith(
        caches.match(req).then((cachedResponse) => {
            const fetchPromise = fetch(req).then((networkResponse) => {
                if (networkResponse && (networkResponse.status === 200 || networkResponse.type === 'opaque')) {
                    const responseClone = networkResponse.clone();
                    caches.open(CACHE_NAME).then((cache) => {
                        cache.put(req, responseClone);
                    });
                }
                return networkResponse;
            }).catch((err) => {
                if (req.mode === 'navigate') {
                    return caches.match('./index.html') || caches.match('./');
                }
                throw err;
            });

            return cachedResponse || fetchPromise;
        })
    );
});
`, name, cacheKey, assetArrayStr, name)

	return []byte(swCode)
}

// ── HTML Tag Injection ──────────────────────────────────────────────────────

func injectPWATags(content []byte, title, themeColor string) []byte {
	if themeColor == "" {
		themeColor = "#0b0f17"
	}
	var b bytes.Buffer
	b.WriteString("\n    <!-- PWA & Mobile Web App Meta -->\n")
	b.WriteString("    <link rel=\"manifest\" href=\"manifest.webmanifest\">\n")
	fmt.Fprintf(&b, "    <meta name=\"theme-color\" content=\"%s\">\n", themeColor)
	b.WriteString("    <meta name=\"mobile-web-app-capable\" content=\"yes\">\n")
	b.WriteString("    <meta name=\"apple-mobile-web-app-capable\" content=\"yes\">\n")
	b.WriteString("    <meta name=\"apple-mobile-web-app-status-bar-style\" content=\"black-translucent\">\n")
	fmt.Fprintf(&b, "    <meta name=\"apple-mobile-web-app-title\" content=\"%s\">\n", title)
	b.WriteString("    <link rel=\"apple-touch-icon\" href=\"../favicon.svg\">\n")
	b.WriteString(`    <script>
      if ('serviceWorker' in navigator) {
        window.addEventListener('load', () => {
          navigator.serviceWorker.register('./sw.js', { scope: './' }).catch(err => {
            console.debug('ServiceWorker registration optional error:', err);
          });
        });
      }
    </script>
`)

	if bytes.Contains(content, []byte("</head>")) {
		return bytes.Replace(content, []byte("</head>"), append(b.Bytes(), []byte("</head>")...), 1)
	}
	return content
}
