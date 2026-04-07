package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
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
	maxWorkers = 10
)

// Pre-compiled regexes for performance
var (
	reMetaBlock = regexp.MustCompile(`(?s)<!-- APP-META(.*?)-->`)
	reTitleTag  = regexp.MustCompile(`(?i)<title>([^<]+)</title>`)
	reBodyTag   = regexp.MustCompile(`(?i)(<body[^>]*>)`)
	reHtmlTag   = regexp.MustCompile(`(?i)(<html[^>]*>)`)
	reUpdated   = regexp.MustCompile(`(?i)Updated:\s*[^\n]+`)
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

	// Ensure source meta shell is present
	ensureMetaBlock(srcIdx, name)

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

	// Process and Write in one pass
	processedContent := injectBytePartials(content, bCtx.Header, bCtx.Footer)
	if err := os.WriteFile(distIdx, processedContent, 0644); err != nil {
		return nil, err
	}

	if status != "published" {
		logSkip("  Skipped: %s (%s)", name, status)
		return nil, nil
	}

	title := extractMeta("Title", content)
	if title == "" {
		title = name
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
		Description: extractMeta("Description", content),
		Category:    extractMeta("Category", content),
		Image:       extractMeta("Image", content),
		Icon:        extractMeta("Icon", content),
		Status:      status,
		Path:        name + "/",
		UpdatedAt:   updatedAt,
	}, nil
}

// ── Optimized Bytes Operations ──────────────────────────────────────────────

func injectBytePartials(content []byte, header, footer []byte) []byte {
	out := content
	if len(header) > 0 {
		if reBodyTag.Match(out) {
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
			} else {
				out = append(out, append([]byte("\n"), footer...)...)
			}
		}
	}
	return out
}

func ensureMetaBlock(path, slug string) {
	bytesContent, _ := os.ReadFile(path)
	if bytes.Contains(bytesContent, []byte("<!-- APP-META")) {
		return
	}

	title := slug
	if match := reTitleTag.FindSubmatch(bytesContent); len(match) > 1 {
		title = strings.TrimSpace(string(match[1]))
	}

	meta := fmt.Sprintf("<!-- APP-META\nTitle: %s\nDescription:\nCategory:\nStatus: published\nUpdated: %d\n-->\n", 
		title, time.Now().Unix())
	_ = os.WriteFile(path, append([]byte(meta), bytesContent...), 0644)
}

func cmdUpdateMetadata() {
	logInfo("Scanning for changes to update metadata...")
	
	cmd := exec.Command("git", "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		logError("Failed to get git diff: %v", err)
		return
	}

	today := fmt.Sprintf("%d", time.Now().Unix())
	changedApps := make(map[string]bool)
	lines := strings.Split(string(out), "\n")
	
	for _, line := range lines {
		if strings.HasPrefix(line, "src/") {
			parts := strings.Split(line, "/")
			if len(parts) >= 2 {
				appName := parts[1]
				if appName != "index.html" {
					changedApps[appName] = true
				}
			}
		}
	}

	if len(changedApps) == 0 {
		logInfo("No app changes detected in src/.")
		return
	}

	for appName := range changedApps {
		path := filepath.Join(srcDir, appName, "index.html")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}

		content, _ := os.ReadFile(path)
		if !bytes.Contains(content, []byte("<!-- APP-META")) {
			ensureMetaBlock(path, appName)
			continue
		}

		match := reMetaBlock.FindSubmatch(content)
		if len(match) > 0 {
			block := match[1]
			var newBlock []byte
			if reUpdated.Match(block) {
				newBlock = reUpdated.ReplaceAll(block, []byte("Updated: "+today))
			} else {
				newBlock = bytes.Replace(block, []byte("-->"), []byte("Updated: "+today+"\n-->"), 1)
			}
			newContent := bytes.Replace(content, block, newBlock, 1)
			os.WriteFile(path, newContent, 0644)
			logSuccess("  Updated timestamp for %s", appName)
		}
	}
}

// ── Command Implementation ───────────────────────────────────────────────────

func cmdBuild() {
	start := time.Now()
	logInfo("🚀 Initializing high-performance build...")

	header, _ := os.ReadFile(headerFile)
	footer, _ := os.ReadFile(footerFile)
	bCtx := &BuildCtx{Header: header, Footer: footer}

	os.RemoveAll(distDir)
	_ = os.MkdirAll(distDir, 0755)

	// Phase 1: Global Assets
	for _, asset := range []string{"index.html", "favicon.svg"} {
		if data, err := os.ReadFile(asset); err == nil {
			_ = os.WriteFile(filepath.Join(distDir, asset), data, 0644)
		}
	}
	
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

	jsonData, _ := json.MarshalIndent(apps, "", "  ")
	_ = os.WriteFile(filepath.Join(distDir, "apps.json"), jsonData, 0644)

	logSuccess("\n✨ Build complete in %v", time.Since(start))
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run gist.go [build|update-metadata|preview|clean]")
		return
	}

	switch os.Args[1] {
	case "build":
		cmdBuild()
	case "update-metadata":
		cmdUpdateMetadata()
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
