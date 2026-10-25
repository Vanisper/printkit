package render

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPrepareHTMLRejectsActiveContent(t *testing.T) {
	for name, input := range map[string]string{
		"script":        `<script>fetch('https://example.com')</script>`,
		"noscript":      `<noscript><img src="https://example.com"></noscript>`,
		"canvas":        `<canvas width="100" height="100"></canvas>`,
		"handler":       `<img src="data:image/png;base64,AA==" onerror="alert(1)">`,
		"frame":         `<iframe srcdoc="hello"></iframe>`,
		"refresh":       `<meta http-equiv="refresh" content="0;url=https://example.com">`,
		"base":          `<base href="https://example.com">`,
		"stylesheet":    `<link rel="stylesheet" href="data:text/css,p{}">`,
		"file":          `<img src="file:///etc/passwd">`,
		"network":       `<img src="https://example.com/a.png">`,
		"relative":      `<img src="images/a.png">`,
		"srcset":        `<img srcset="https://example.com/a.png 2x">`,
		"svg_script":    `<svg><script>alert(1)</script></svg>`,
		"svg_animation": `<svg><set attributeName="href" to="https://example.com"></set></svg>`,
		"svg_href":      `<svg><image xlink:href="file:///tmp/a.png"></image></svg>`,
		"empty":         ` `,
		"encoding":      string([]byte{0xff, 0xfe}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := prepareHTML([]byte(input)); err == nil {
				t.Fatal("expected input rejection")
			}
		})
	}
}

func TestPrepareHTMLPreservesPrintCSSAndInstallsPolicyFirst(t *testing.T) {
	input := `<!doctype html><html><head><style>@page{size:A5}@media print{h1{color:red}}</style></head><body><h1>双联单</h1><img src="data:image/png;base64,AA=="></body></html>`
	got, err := prepareHTML([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<head><meta http-equiv="Content-Security-Policy"`, `@page{size:A5}`, `双联单`, `data:image/png;base64,AA==`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
}

func TestConfigurationAndCancellation(t *testing.T) {
	if _, err := New(Options{MaxOutputBytes: -1}); err == nil {
		t.Fatal("expected invalid limit")
	}
	if _, err := New(Options{ChromePath: "/nonexistent/printkit-chrome"}); err == nil {
		t.Fatal("expected invalid chrome path")
	}
	r, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := r.Render(ctx, []byte("<p>hello</p>")); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context cancellation, got %v", err)
	}
}
