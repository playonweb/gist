package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	maxWorkers = 10 // Concurrent worker limit for processing apps
)

// Pre-compiled regexes for performance
var (
	reMetaBlock = regexp.MustCompile(`(?s)<!-- APP-META(.*?)-->`)
	reTitleTag  = regexp.MustCompile(`(?i)<title>([^<]+)</title>`)
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
	Repo   string
	Token  string
}

// ── Metadata Extraction ──────────────────────────────────────────────────────

func extractMeta(key, content string) string {
	match := reMetaBlock.FindStringSubmatch(content)
	if len(match) < 2 {
		return ""
	}

	lines := strings.Split(match[1], "\n")
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

	// Ensure source meta (Synchronous at source level, but fine for local)
	ensureMetaBlock(srcIdx, name)

	content, err := os.ReadFile(srcIdx)
	if err != nil {
		return nil, err
	}
	cStr := string(content)

	status := extractMeta("Status", cStr)
	if status == "" {
		status = "published"
	}

	// Copy & Inject
	if err := os.MkdirAll(filepath.Dir(distIdx), 0755); err != nil {
		return nil, err
	}
	if err := copyFile(srcIdx, distIdx); err != nil {
		return nil, err
	}
	if err := injectIntoFile(distIdx, bCtx.Header, bCtx.Footer); err != nil {
		return nil, err
	}

	if status != "published" {
		logSkip("  Skipped: %s (%s)", name, status)
		return nil, nil
	}

	title := extractMeta("Title", cStr)
	if title == "" {
		title = name
	}

	// Sort Metadata
	modTime := getGitUpdateTime(srcIdx, bCtx)

	return &AppMeta{
		Title:       title,
		Description: extractMeta("Description", cStr),
		Category:    extractMeta("Category", cStr),
		Image:       extractMeta("Image", cStr),
		Icon:        extractMeta("Icon", cStr),
		Status:      status,
		Path:        name + "/",
		UpdatedAt:   modTime,
	}, nil
}

// ── Optimized File Operations ───────────────────────────────────────────────

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func injectIntoFile(target string, header, footer []byte) error {
	content, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	html := string(content)

	if len(header) > 0 {
		hStr := string(header)
		if reBodyTag.MatchString(html) {
			html = reBodyTag.ReplaceAllString(html, "$1\n"+hStr)
		} else if reHtmlTag.MatchString(html) {
			html = reHtmlTag.ReplaceAllString(html, "$1\n"+hStr)
		} else {
			html = hStr + "\n" + html
		}
	}

	if len(footer) > 0 {
		fStr := string(footer)
		if strings.Contains(strings.ToLower(html), "</body>") {
			html = strings.Replace(html, "</body>", fStr+"\n</body>", 1)
		} else if strings.Contains(strings.ToLower(html), "</html>") {
			html = strings.Replace(html, "</html>", fStr+"\n</html>", 1)
		} else {
			html = html + "\n" + fStr
		}
	}

	return os.WriteFile(target, []byte(html), 0644)
}

func getGitUpdateTime(path string, bCtx *BuildCtx) time.Time {
	dir := filepath.Dir(path)

	// 1. Local Git Check
	cmd := exec.Command("git", "log", "-1", "--format=%ct", "--", dir)
	if out, err := cmd.Output(); err == nil && len(out) > 0 {
		if ts, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil && ts > 0 {
			return time.Unix(ts, 0)
		}
	}

	// 2. GitHub API Fallback (CI Optimization)
	if bCtx.Repo != "" && bCtx.Token != "" {
		url := fmt.Sprintf("https://api.github.com/repos/%s/commits?path=%s&per_page=1", bCtx.Repo, dir)
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "token "+bCtx.Token)
		client := &http.Client{Timeout: 3 * time.Second}
		if resp, err := client.Do(req); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				var res []struct {
					Commit struct {
						Committer struct {
							Date time.Time `json:"date"`
						} `json:"committer"`
					} `json:"commit"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&res); err == nil && len(res) > 0 {
					return res[0].Commit.Committer.Date
				}
			}
		}
	}

	// 3. Fallback
	if info, err := os.Stat(path); err == nil {
		return info.ModTime()
	}
	return time.Now()
}

func ensureMetaBlock(path, slug string) {
	bytes, _ := os.ReadFile(path)
	if strings.Contains(string(bytes), "<!-- APP-META") {
		return
	}

	title := slug
	if match := reTitleTag.FindStringSubmatch(string(bytes)); len(match) > 1 {
		title = strings.TrimSpace(match[1])
	}

	meta := fmt.Sprintf("<!-- APP-META\nTitle: %s\nDescription:\nCategory:\nStatus: published\n-->\n", title)
	_ = os.WriteFile(path, append([]byte(meta), bytes...), 0644)
}

// ── Command Implementation ───────────────────────────────────────────────────

func cmdBuild() {
	start := time.Now()
	logInfo("🚀 Initializing high-performance build...")

	// Pre-load assets into memory
	header, _ := os.ReadFile(headerFile)
	footer, _ := os.ReadFile(footerFile)
	bCtx := &BuildCtx{
		Header: header,
		Footer: footer,
		Repo:   os.Getenv("GITHUB_REPOSITORY"),
		Token:  os.Getenv("GITHUB_TOKEN"),
	}

	// Workspace preparation
	os.RemoveAll(distDir)
	_ = os.MkdirAll(distDir, 0755)

	dirs, err := os.ReadDir(srcDir)
	if err != nil {
		logError("Source directory missing: %v", err)
		os.Exit(1)
	}

	// Concurrent Processing
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		apps    []AppMeta
		tokens  = make(chan struct{}, maxWorkers)
		results = make(chan *AppMeta, len(dirs))
	)

	for _, d := range dirs {
		name := d.Name()
		if name == distDir || name == ".DS_Store" {
			continue
		}

		if !d.IsDir() {
			// Process Root Files (e.g., src/index.html)
			srcPath := filepath.Join(srcDir, name)
			distPath := filepath.Join(distDir, name)
			wg.Add(1)
			go func(s, ds string) {
				defer wg.Done()
				_ = copyFile(s, ds)
			}(srcPath, distPath)
			continue
		}

		// Process App Folders
		wg.Add(1)
		go func(entry os.DirEntry) {
			defer wg.Done()
			tokens <- struct{}{}
			defer func() { <-tokens }()

			app, err := processApp(context.Background(), bCtx, entry)
			if err != nil {
				logError("  Failed %s: %v", entry.Name(), err)
				return
			}
			if app != nil {
				mu.Lock()
				apps = append(apps, *app)
				mu.Unlock()
				logSuccess("  Synthesized: %s", entry.Name())
			}
		}(d)
	}

	wg.Wait()
	close(results)

	// Finalize Collection
	sort.Slice(apps, func(i, j int) bool {
		return apps[i].UpdatedAt.After(apps[j].UpdatedAt)
	})

	jsonData, _ := json.MarshalIndent(apps, "", "  ")
	_ = os.WriteFile(filepath.Join(distDir, "apps.json"), jsonData, 0644)

	logSuccess("\n✨ Build complete in %v", time.Since(start))
	logInfo("Registry: dist/apps.json (%d apps)", len(apps))
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "build":
		cmdBuild()
	case "preview":
		port := "8080"
		if len(os.Args) > 2 {
			port = os.Args[2]
		}
		logInfo("📡 Serving dist/ on http://localhost:%s", port)
		log.Fatal(http.ListenAndServe(":"+port, http.FileServer(http.Dir(distDir))))
	case "clean":
		os.RemoveAll(distDir)
		logSuccess("Cleaned workspace.")
	default:
		usage()
	}
}

func usage() {
	fmt.Printf("%sUsage:%s go run gist.go [build|preview|clean]\n", bold, nc)
}
