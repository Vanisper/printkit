package render

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const pixel = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aMioAAAAASUVORK5CYII="

func integrationRenderer(t *testing.T, maxOutput int64) *Renderer {
	t.Helper()
	if os.Getenv("PRINTKIT_CHROME_TEST") != "1" {
		t.Skip("set PRINTKIT_CHROME_TEST=1 to test installed Chrome")
	}
	r, err := New(Options{ChromePath: os.Getenv("PRINTKIT_CHROME_PATH"), MaxOutputBytes: maxOutput})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestChromePrintCSSAndImages(t *testing.T) {
	r := integrationRenderer(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	input := `<!doctype html><style>@page { size: 100mm 150mm; margin: 10mm } @media print { body { color: black } } h1 {background-image: url("` + pixel + `")}</style><h1>printkit 双联单</h1><img src="` + pixel + `">`
	pdf, version, err := r.Render(ctx, []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || !strings.Contains(version, "Chrome/") {
		t.Fatalf("invalid PDF or engine: %d bytes, %s", len(pdf), version)
	}
	// Chrome 会按像素取整，保留 1 pt 容差，同时区分 CSS 纸张与默认 A4
	box := regexp.MustCompile(`/MediaBox\s*\[0 0 ([0-9.]+) ([0-9.]+)\]`).FindSubmatch(pdf)
	if len(box) != 3 {
		t.Fatal("PDF has no page box")
	}
	width, _ := strconv.ParseFloat(string(box[1]), 64)
	height, _ := strconv.ParseFloat(string(box[2]), 64)
	if math.Abs(width-100/25.4*72) > 1 || math.Abs(height-150/25.4*72) > 1 {
		t.Fatalf("CSS page size not retained: %f x %f", width, height)
	}
}

func TestChromeEmbeddedFontAndVectorImage(t *testing.T) {
	r := integrationRenderer(t, 0)
	font, err := os.ReadFile("testdata/fixture.woff")
	if err != nil {
		t.Fatal(err)
	}
	svg := base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20"><rect width="20" height="20" fill="red"/></svg>`))
	input := `<style>@font-face{font-family:fixture;src:url(data:font/woff;base64,` + base64.StdEncoding.EncodeToString(font) + `)}p{font-family:fixture}</style><p>A</p><img src="data:image/svg+xml;base64,` + svg + `">`
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, _, err := r.Render(ctx, []byte(input)); err != nil {
		t.Fatal(err)
	}
}

func TestChromeRejectsMissingResourcesWithoutNetwork(t *testing.T) {
	r := integrationRenderer(t, 0)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { requests.Add(1); w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	nestedSVG := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><image href="`+server.URL+`/nested.png"/></svg>`))
	filterSVG := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect width="20" height="20" filter="url(`+server.URL+`/filter.svg#f)"/></svg>`))
	for name, input := range map[string]string{
		"img_network":         `<img src="` + server.URL + `/image.png">`,
		"css_network":         `<style>p{background-image:url("` + server.URL + `/background.png")}</style><p>network</p>`,
		"css_import":          `<style>@import url("` + server.URL + `/style.css");</style><p>import</p>`,
		"css_escaped":         `<style>p{background-image:u\72l("` + server.URL + `/escaped.png")}</style><p>escaped</p>`,
		"script":              `<script>fetch('` + server.URL + `/script')</script><p>script</p>`,
		"image_corrupt":       `<img src="data:image/png;base64,AAAA">`,
		"css_image_corrupt":   `<style>p{background-image:url(data:image/png;base64,AAAA)}</style><p>image</p>`,
		"font_corrupt":        `<style>@font-face{font-family:broken;src:url(data:font/woff2;base64,AAAA)}p{font-family:broken}</style><p>font</p>`,
		"file_css":            `<style>p{background-image:url(file:///etc/passwd)}</style><p>file</p>`,
		"svg_nested_image":    `<img src="` + nestedSVG + `">`,
		"svg_nested_filter":   `<img src="` + filterSVG + `">`,
		"input_image_corrupt": `<input type="image" src="data:image/png;base64,AAAA">`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			pdf, _, err := r.Render(ctx, []byte(input))
			if err == nil || pdf != nil {
				t.Fatalf("expected rejection, got %d bytes, %v", len(pdf), err)
			}
			if errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("resource failure must not wait until timeout: %v", err)
			}
		})
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("sent %d network requests", got)
	}
}

func TestChromeOutputLimit(t *testing.T) {
	r := integrationRenderer(t, 100)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, _, err := r.Render(ctx, []byte(`<p>PDF exceeds one hundred bytes</p>`)); err == nil || !strings.Contains(err.Error(), "100 字节") {
		t.Fatalf("want output limit error, got %v", err)
	}
}

func TestChromeCancellationCleansProfile(t *testing.T) {
	r := integrationRenderer(t, 0)
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, _, err := r.Render(ctx, []byte(`<p>cancel during Chrome startup</p>`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline, got %v", err)
	}
	profiles, err := filepath.Glob(filepath.Join(dir, "chromedp-runner*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 0 {
		t.Fatalf("browser profiles remain after cancellation: %v", profiles)
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		processes, err := exec.Command("ps", "-ax", "-o", "command=").Output()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(processes, []byte("--user-data-dir="+dir)) {
			t.Fatal("Chrome child processes remain after cancellation")
		}
	}
}
