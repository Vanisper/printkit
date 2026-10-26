package render

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"
)

func TestAcceptanceDocumentSamples(t *testing.T) {
	if os.Getenv("PRINTKIT_ACCEPTANCE_TEST") != "1" {
		t.Skip("设置 PRINTKIT_ACCEPTANCE_TEST=1 后验证单据样例")
	}
	renderer, err := New(Options{ChromePath: os.Getenv("PRINTKIT_CHROME_PATH")})
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		name  string
		pages int
	}{{"dual-copy.html", 1}, {"summary.html", 3}} {
		t.Run(sample.name, func(t *testing.T) {
			input, err := os.ReadFile(filepath.Join("..", "..", "testdata", sample.name))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			pdf, engine, err := renderer.Render(ctx, input, DefaultSettings())
			if err != nil {
				t.Fatal(err)
			}
			// 仅识别 Chrome 输出的未压缩页面字典；页数与纸张检查不替代字形、裁切和内容核对
			pages := regexp.MustCompile(`/Type\s*/Page\b`).FindAll(pdf, -1)
			boxes := regexp.MustCompile(`/MediaBox\s*\[0 0 ([0-9.]+) ([0-9.]+)\]`).FindAllSubmatch(pdf, -1)
			if len(pages) != sample.pages || len(boxes) != sample.pages {
				t.Fatalf("%s：页面字典 %d、纸张记录 %d，期望 %d 页；Chrome 输出结构变化时需更新检查器", engine, len(pages), len(boxes), sample.pages)
			}
			for page, box := range boxes {
				width, _ := strconv.ParseFloat(string(box[1]), 64)
				height, _ := strconv.ParseFloat(string(box[2]), 64)
				if math.Abs(width-210/25.4*72) > 1 || math.Abs(height-297/25.4*72) > 1 {
					t.Errorf("第 %d 页不是 A4：%.2f × %.2f pt", page+1, width, height)
				}
			}
			t.Logf("%s：%d 页 A4，%d 字节，%s", sample.name, sample.pages, len(pdf), engine)
		})
	}
}
