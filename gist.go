package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ── Configuration ─────────────────────────────────────────────────────────────

const (
	srcDir     = "src"
	distDir    = "dist"
	headerFile = "helpers/header.html"
	footerFile = "helpers/footer.html"
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
	Title       string `json:"title"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Status      string `json:"status"`
	Image       string    `json:"image"`
	Icon        string    `json:"icon"`
	Path        string    `json:"path"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ── Metadata Extraction ──────────────────────────────────────────────────────

func extractMeta(key string, content string) string {
	re := regexp.MustCompile(`(?s)<!-- APP-META(.*?)-->`)
	match := re.FindStringSubmatch(content)
	if len(match) < 2 {
		return ""
	}
	metaBlock := match[1]
	
	lines := strings.Split(metaBlock, "\n")
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

func hasMetaBlock(content string) bool {
	return strings.Contains(content, "<!-- APP-META")
}

// ── File Operations ──────────────────────────────────────────────────────────

func copyDir(src string, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target)
	})
}

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

// ── Injection Logic ──────────────────────────────────────────────────────────

func injectIntoFile(target string) error {
	content, err := ioutil.ReadFile(target)
	if err != nil {
		return err
	}
	html := string(content)

	header, _ := ioutil.ReadFile(headerFile)
	footer, _ := ioutil.ReadFile(footerFile)

	if len(header) > 0 {
		hStr := string(header)
		if strings.Contains(strings.ToLower(html), "<body") {
			re := regexp.MustCompile(`(?i)(<body[^>]*>)`)
			html = re.ReplaceAllString(html, "$1\n"+hStr)
		} else if strings.Contains(strings.ToLower(html), "<html") {
			re := regexp.MustCompile(`(?i)(<html[^>]*>)`)
			html = re.ReplaceAllString(html, "$1\n"+hStr)
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

	return ioutil.WriteFile(target, []byte(html), 0644)
}

func getGitUpdateTime(path string) time.Time {
	// 1. Try local Git history first
	dir := filepath.Dir(path)
	cmd := exec.Command("git", "log", "-1", "--format=%ct", "--", dir)
	out, err := cmd.Output()
	if err == nil && len(out) > 0 {
		tsStr := strings.TrimSpace(string(out))
		ts, err := strconv.ParseInt(tsStr, 10, 64)
		if err == nil && ts > 0 {
			return time.Unix(ts, 0)
		}
	}

	// 2. Fallback to GitHub API only in CI (to avoid rate limits and slow clones)
	repo := os.Getenv("GITHUB_REPOSITORY") // owner/repo
	token := os.Getenv("GITHUB_TOKEN")
	if repo != "" && token != "" {
		url := fmt.Sprintf("https://api.github.com/repos/%s/commits?path=%s&per_page=1", repo, dir)
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "token "+token)
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			var result []struct {
				Commit struct {
					Committer struct {
						Date time.Time `json:"date"`
					} `json:"committer"`
				} `json:"commit"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err == nil && len(result) > 0 {
				return result[0].Commit.Committer.Date
			}
		}
	}

	// 3. Last resort: local file time
	info, _ := os.Stat(path)
	return info.ModTime()
}

// ── Metadata Management ──────────────────────────────────────────────────────

func ensureMetaBlock(path string, slug string) {
	content, _ := ioutil.ReadFile(path)
	if hasMetaBlock(string(content)) {
		return
	}

	title := ""
	re := regexp.MustCompile(`(?i)<title>([^<]+)</title>`)
	match := re.FindStringSubmatch(string(content))
	if len(match) > 1 {
		title = strings.TrimSpace(match[1])
	} else {
		title = strings.Title(strings.ReplaceAll(slug, "-", " "))
	}

	metaBlock := fmt.Sprintf("<!-- APP-META\nTitle: %s\nDescription:\nCategory:\nStatus: published\n-->\n", title)
	ioutil.WriteFile(path, []byte(metaBlock+string(content)), 0644)
	logInfo("Added APP-META block to src/%s/index.html", slug)
}

// ── Commands ─────────────────────────────────────────────────────────────────

func cmdBuild() {
	logInfo("Starting build...")

	// Phase 0: Clean
	os.RemoveAll(distDir)
	err := copyDir(srcDir, distDir)
	if err != nil {
		logError("Failed to copy src to dist: %v", err)
		os.Exit(1)
	}
	logSuccess("Copied %ssrc/%s → %sdist/%s", bold, nc, bold, nc)

	// Phase 1: Scan
	logInfo("Scanning for apps...")
	var apps []AppMeta

	dirs, _ := ioutil.ReadDir(srcDir)
	for _, d := range dirs {
		if !d.IsDir() || d.Name() == "dist" {
			continue
		}
		
		srcIdx := filepath.Join(srcDir, d.Name(), "index.html")
		if _, err := os.Stat(srcIdx); os.IsNotExist(err) {
			continue
		}

		// Ensure meta in SOURCE
		ensureMetaBlock(srcIdx, d.Name())
		
		content, _ := ioutil.ReadFile(srcIdx)
		cStr := string(content)

		status := extractMeta("Status", cStr)
		if status == "" {
			status = "published"
		}

		// Copy potentially updated source to dist
		distIdx := filepath.Join(distDir, d.Name(), "index.html")
		copyFile(srcIdx, distIdx)

		// Inject
		injectIntoFile(distIdx)
		logSuccess("  Built: %s%s/%s %s(%s)%s", bold, d.Name(), nc, dim, status, nc)

		if status != "published" {
			logSkip("  Skipped from apps.json: %s (%s)", d.Name(), status)
			continue
		}

		title := extractMeta("Title", cStr)
		if title == "" {
			title = d.Name()
		}

		// Get modification time via Git
		modTime := getGitUpdateTime(srcIdx)

		apps = append(apps, AppMeta{
			Title:       title,
			Description: extractMeta("Description", cStr),
			Category:    extractMeta("Category", cStr),
			Image:       extractMeta("Image", cStr),
			Icon:        extractMeta("Icon", cStr),
			Status:      status,
			Path:        d.Name() + "/",
			UpdatedAt:   modTime,
		})
	}

	// Sort apps by UpdatedAt DESC (latest first)
	sort.Slice(apps, func(i, j int) bool {
		return apps[i].UpdatedAt.After(apps[j].UpdatedAt)
	})

	// Phase 2: JSON
	jsonData, _ := json.MarshalIndent(apps, "", "  ")
	ioutil.WriteFile(filepath.Join(distDir, "apps.json"), jsonData, 0644)
	logSuccess("Generated %sdist/apps.json%s with %d published app(s).", bold, nc, len(apps))

	fmt.Println()
	logSuccess("%sBuild complete!%s", bold, nc)
	logInfo("Preview: %sgo run gist.go preview%s", bold, nc)
}

func cmdPreview(port string) {
	if _, err := os.Stat(distDir); os.IsNotExist(err) {
		logWarn("dist/ does not exist. Building first...")
		cmdBuild()
	}

	logInfo("Serving %sdist/%s at %shttp://localhost:%s%s", bold, nc, bold, port, nc)
	logInfo("Press Ctrl+C to stop.")
	
	fs := http.FileServer(http.Dir(distDir))
	http.Handle("/", fs)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func cmdClean() {
	if _, err := os.Stat(distDir); err == nil {
		os.RemoveAll(distDir)
		logSuccess("Removed %sdist/%s", bold, nc)
	} else {
		logInfo("Nothing to clean.")
	}
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	switch cmd {
	case "build":
		cmdBuild()
	case "preview":
		port := "8080"
		if len(os.Args) > 2 {
			port = os.Args[2]
		}
		cmdPreview(port)
	case "clean":
		cmdClean()
	default:
		logError("Unknown command: %s", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Printf("%sUsage:%s go run gist.go <command>\n\n", bold, nc)
	fmt.Println("Commands:")
	fmt.Println("  build      Copy src/ → dist/, inject header/footer, generate apps.json.")
	fmt.Println("  preview    Serve dist/ locally (default port: 8080).")
	fmt.Println("  clean      Remove dist/.")
	fmt.Println("\nExamples:")
	fmt.Println("  go run gist.go build")
	fmt.Println("  go run gist.go preview 3000")
}
