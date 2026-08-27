package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── Configuration ─────────────────────────────────────────────────────────────

const (
	srcDir     = "src"
	distDir    = "dist"
	headerFile = "helpers/header.html"
	footerFile = "helpers/footer.html"
	pwaFile    = "helpers/pwa.html"
	maxWorkers = 10
	baseURL    = "https://gist.rahuldhole.com/"
)

// Pre-compiled regexes for performance
var (
	reMetaBlock = regexp.MustCompile(`(?s)<!-- APP-META(.*?)-->`)
	reTitleTag  = regexp.MustCompile(`(?i)<title>([^<]+)</title>`)
	reHeadTag   = regexp.MustCompile(`(?i)(<head[^>]*>)`)
	reBodyTag   = regexp.MustCompile(`(?i)(<body[^>]*>)`)
	reHtmlTag   = regexp.MustCompile(`(?i)(<html[^>]*>)`)
)

// Colors for terminal output
const (
	red    = "\033[0;31m"
	green  = "\033[0;32m"
	yellow = "\033[0;33m"
	cyan   = "\033[0;36m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	nc     = "\033[0m"
)

func logInfo(msg string, args ...interface{})    { fmt.Printf(cyan+"ℹ"+nc+"  "+msg+"\n", args...) }
func logSuccess(msg string, args ...interface{}) { fmt.Printf(green+"✓"+nc+"  "+msg+"\n", args...) }
func logWarn(msg string, args ...interface{})    { fmt.Printf(yellow+"⚠"+nc+"  "+msg+"\n", args...) }
func logError(msg string, args ...interface{})   { fmt.Printf(red+"✗"+nc+"  "+msg+"\n", args...) }
func logSkip(msg string, args ...interface{})    { fmt.Printf(dim+"⊘  "+msg+nc+"\n", args...) }

// ── App Metadata Model ───────────────────────────────────────────────────────

type AppMeta struct {
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Category    string    `json:"category"`
	Status      string    `json:"status"`
	Image       string    `json:"image"`
	Icon        string    `json:"icon"`
	Path        string    `json:"path"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ── Build Context ─────────────────────────────────────────────────────────────

type BuildCtx struct {
	Header []byte
	Footer []byte
	PWA    []byte
}

// ── Metadata Extraction ──────────────────────────────────────────────────────

func extractMeta(key string, content []byte) string {
	match := reMetaBlock.FindSubmatch(content)
	if len(match) < 2 {
		return ""
	}

	lines := strings.Split(string(match[1]), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), strings.ToLower(key)+":") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

// ── Concurrency Safe App Processor ───────────────────────────────────────────

func processApp(ctx context.Context, bCtx *BuildCtx, appDir os.DirEntry) (*AppMeta, error) {
	name := appDir.Name()
	srcIdx := filepath.Join(srcDir, name, "index.html")
	distIdx := filepath.Join(distDir, name, "index.html")

	content, err := os.ReadFile(srcIdx)
	if err != nil {
		return nil, err
	}

	status := extractMeta("Status", content)
	if status == "" {
		status = "published"
	}

	// Prepare directory
	if err := os.MkdirAll(filepath.Dir(distIdx), 0755); err != nil {
		return nil, err
	}

	// Extract metadata for OG & PWA tags
	title := extractMeta("Title", content)
	if title == "" {
		title = name
	}
	description := extractMeta("Description", content)
	image := extractMeta("Image", content)
	category := extractMeta("Category", content)
	icon := extractMeta("Icon", content)

	// Process content: inject header/footer, OG meta tags, and PWA suite
	footerToInject := bCtx.Footer
	if strings.ToLower(extractMeta("Disable-Footer", content)) == "true" {
		footerToInject = nil
	}
	
	processedContent := injectBytePartials(content, bCtx.Header, footerToInject)
	processedContent = injectOGTags(processedContent, title, description, category, icon, image, name+"/")
	processedContent, _ = processAppPWA(processedContent, name, title, description, category, image, distIdx, bCtx.PWA)
	if err := os.WriteFile(distIdx, processedContent, 0644); err != nil {
		return nil, err
	}

	if status != "published" {
		logSkip("  Skipped: %s (%s)", name, status)
		return nil, nil
	}

	// Parse custom "Updated" field from meta block
	updatedStr := extractMeta("Updated", content)
	var updatedAt time.Time
	if ts, err := strconv.ParseInt(updatedStr, 10, 64); err == nil && ts > 0 {
		updatedAt = time.Unix(ts, 0)
	} else {
		var parseErr error
		updatedAt, parseErr = time.Parse(time.RFC3339, updatedStr)
		if parseErr != nil {
			updatedAt, parseErr = time.Parse("2006-01-02", updatedStr)
			if parseErr == nil {
				updatedAt = time.Date(updatedAt.Year(), updatedAt.Month(), updatedAt.Day(), 23, 59, 59, 0, time.UTC)
			}
		}
	}
	
	if updatedAt.IsZero() {
		if info, err := os.Stat(srcIdx); err == nil {
			updatedAt = info.ModTime()
		} else {
			updatedAt = time.Now()
		}
	}

	return &AppMeta{
		Title:       title,
		Description: description,
		Category:    extractMeta("Category", content),
		Image:       image,
		Icon:        icon,
		Status:      status,
		Path:        name + "/",
		UpdatedAt:   updatedAt,
	}, nil
}

// ── Optimized Bytes Operations ──────────────────────────────────────────────

func injectBytePartials(content []byte, header, footer []byte) []byte {
	out := content
	if len(header) > 0 {
		if reHeadTag.Match(out) {
			out = reHeadTag.ReplaceAll(out, append(reHeadTag.Find(out), append([]byte("\n"), header...)...))
		} else if reBodyTag.Match(out) {
			out = reBodyTag.ReplaceAll(out, append(reBodyTag.Find(out), append([]byte("\n"), header...)...))
		} else if reHtmlTag.Match(out) {
			out = reHtmlTag.ReplaceAll(out, append(reHtmlTag.Find(out), append([]byte("\n"), header...)...))
		} else {
			out = append(header, append([]byte("\n"), out...)...)
		}
	}

	if len(footer) > 0 {
		fStr := []byte("</body>")
		if bytes.Contains(out, fStr) {
			out = bytes.Replace(out, fStr, append(footer, append([]byte("\n"), fStr...)...), 1)
		} else {
			fStr = []byte("</html>")
			if bytes.Contains(out, fStr) {
				out = bytes.Replace(out, fStr, append(footer, append([]byte("\n"), fStr...)...), 1)
			}
		}
	}
	return out
}

// ── Social Meta / OG Tag Injection ──────────────────────────────────────────

func ogEmoji(category, icon string) string {
	cat := strings.ToLower(category)
	switch {
	case strings.Contains(cat, "ai") || strings.Contains(cat, "ml"):
		return "🤖"
	case strings.Contains(cat, "tool"):
		return "🛠️"
	case strings.Contains(cat, "game"):
		return "🎮"
	case strings.Contains(cat, "algo"):
		return "🧠"
	case strings.Contains(cat, "dev"):
		return "💻"
	case strings.Contains(cat, "test"):
		return "🧪"
	case strings.Contains(cat, "calc"):
		return "🔢"
	default:
		return "📦"
	}
}

func ogImageURL(title, description, category, icon string) string {
	params := url.Values{}
	params.Set("template", "gradient")
	params.Set("title", truncate(title, 80))
	params.Set("icon", ogEmoji(category, icon))
	if description != "" {
		params.Set("description", truncate(description, 200))
	}
	params.Set("bg", "1e3a5f")
	params.Set("text", "ffffff")
	return "https://og-image.org/api/og?" + params.Encode()
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

func generateOGTags(title, description, category, icon, customImage, path string) []byte {
	var b bytes.Buffer
	pageURL := baseURL + path

	b.WriteString("\n    <!-- Open Graph / Social Meta Tags -->\n")
	b.WriteString("    <meta property=\"og:type\" content=\"website\" />\n")
	fmt.Fprintf(&b, "    <meta property=\"og:url\" content=\"%s\" />\n", pageURL)
	fmt.Fprintf(&b, "    <meta property=\"og:title\" content=\"%s\" />\n", title)
	if description != "" {
		fmt.Fprintf(&b, "    <meta property=\"og:description\" content=\"%s\" />\n", description)
	}
	ogImg := customImage
	if ogImg == "" {
		ogImg = ogImageURL(title, description, category, icon)
	}
	fmt.Fprintf(&b, "    <meta property=\"og:image\" content=\"%s\" />\n", ogImg)
	fmt.Fprintf(&b, "    <meta property=\"og:image:width\" content=\"1200\" />\n")
	fmt.Fprintf(&b, "    <meta property=\"og:image:height\" content=\"630\" />\n")
	fmt.Fprintf(&b, "    <meta name=\"twitter:card\" content=\"summary_large_image\" />\n")
	fmt.Fprintf(&b, "    <meta name=\"twitter:url\" content=\"%s\" />\n", pageURL)
	fmt.Fprintf(&b, "    <meta name=\"twitter:title\" content=\"%s\" />\n", title)
	if description != "" {
		fmt.Fprintf(&b, "    <meta name=\"twitter:description\" content=\"%s\" />\n", description)
	}
	fmt.Fprintf(&b, "    <meta name=\"twitter:image\" content=\"%s\" />\n", ogImg)

	return b.Bytes()
}

func injectOGTags(content []byte, title, description, category, icon, customImage, path string) []byte {
	ogTags := generateOGTags(title, description, category, icon, customImage, path)
	if len(ogTags) == 0 {
		return content
	}
	if bytes.Contains(content, []byte("</head>")) {
		return bytes.Replace(content, []byte("</head>"), append(ogTags, []byte("\n</head>")...), 1)
	}
	return content
}

// ── Sitemap & Robots Generation ─────────────────────────────────────────────

func generateSitemap(apps []AppMeta) []byte {
	var b bytes.Buffer
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<urlset xmlns=\"http://www.sitemaps.org/schemas/sitemap/0.9\">\n")

	fmt.Fprintf(&b, "  <url>\n    <loc>%s</loc>\n    <priority>1.0</priority>\n  </url>\n", baseURL)

	for _, app := range apps {
		fmt.Fprintf(&b, "  <url>\n    <loc>%s%s</loc>\n", baseURL, app.Path)
		if !app.UpdatedAt.IsZero() {
			fmt.Fprintf(&b, "    <lastmod>%s</lastmod>\n", app.UpdatedAt.Format("2006-01-02"))
		}
		fmt.Fprintf(&b, "    <priority>0.8</priority>\n  </url>\n")
	}

	b.WriteString("</urlset>\n")
	return b.Bytes()
}

func generateRobotsTxt() []byte {
	var b bytes.Buffer
	b.WriteString("User-agent: *\n")
	b.WriteString("Allow: /\n\n")
	fmt.Fprintf(&b, "Sitemap: %ssitemap.xml\n", baseURL)
	return b.Bytes()
}

// ── Command Implementation ───────────────────────────────────────────────────

func cmdBuild() {
	start := time.Now()
	logInfo("🚀 Initializing high-performance build...")

	header, _ := os.ReadFile(headerFile)
	footer, _ := os.ReadFile(footerFile)
	pwa, _ := os.ReadFile(pwaFile)
	bCtx := &BuildCtx{Header: header, Footer: footer, PWA: pwa}

	os.RemoveAll(distDir)
	_ = os.MkdirAll(distDir, 0755)

	// Phase 1: Global Assets
	if data, err := os.ReadFile("favicon.svg"); err == nil {
		_ = os.WriteFile(filepath.Join(distDir, "favicon.svg"), data, 0644)
		_ = os.WriteFile(filepath.Join(distDir, "favicon.ico"), data, 0644)
	}
	setupPWAGlobalAssets()
	
	// Phase 2: App Scanning
	dirs, _ := os.ReadDir(srcDir)
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		apps    []AppMeta
		tokens  = make(chan struct{}, maxWorkers)
	)

	for _, d := range dirs {
		if !d.IsDir() || d.Name() == distDir || d.Name() == ".DS_Store" {
			continue
		}

		wg.Add(1)
		go func(entry os.DirEntry) {
			defer wg.Done()
			tokens <- struct{}{}
			defer func() { <-tokens }()

			app, err := processApp(context.Background(), bCtx, entry)
			if err == nil && app != nil {
				mu.Lock()
				apps = append(apps, *app)
				mu.Unlock()
				logSuccess("  Synthesized: %s", entry.Name())
			}
		}(d)
	}

	wg.Wait()

	sort.Slice(apps, func(i, j int) bool {
		return apps[i].UpdatedAt.After(apps[j].UpdatedAt)
	})

	jsonData, _ := json.Marshal(apps)
	_ = os.WriteFile(filepath.Join(distDir, "apps.json"), jsonData, 0644)

	// Build-time hydration of index.html
	if indexData, err := os.ReadFile("index.html"); err == nil {
		injectedIndex := bytes.Replace(indexData, []byte("/*__APPS_JSON__*/ []"), jsonData, 1)
		_ = os.WriteFile(filepath.Join(distDir, "index.html"), injectedIndex, 0644)
		logSuccess("  Hydrated index.html at build-time")
	} else {
		logError("  Failed to read index.html for hydration")
	}

	// Phase 3: SEO Assets
	sitemapData := generateSitemap(apps)
	_ = os.WriteFile(filepath.Join(distDir, "sitemap.xml"), sitemapData, 0644)
	logSuccess("  Generated: sitemap.xml (%d URLs)", len(apps)+1)

	robotsData := generateRobotsTxt()
	_ = os.WriteFile(filepath.Join(distDir, "robots.txt"), robotsData, 0644)
	logSuccess("  Generated: robots.txt")

	logSuccess("\n✨ Build complete in %v", time.Since(start))
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run . [build|preview|clean]")
		return
	}

	switch os.Args[1] {
	case "build":
		cmdBuild()
	case "preview":
		port := "8080"
		if len(os.Args) > 2 {
			port = os.Args[2]
		}
		log.Fatal(http.ListenAndServe(":"+port, http.FileServer(http.Dir(distDir))))
	case "clean":
		os.RemoveAll(distDir)
	}
}
