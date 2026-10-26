package render

import (
	"context"
	"math"
	"strings"
	"testing"
)

func TestRenderRejectsInvalidSettingsBeforeStartingChrome(t *testing.T) {
	r, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Settings){
		"空设置":    func(s *Settings) { *s = Settings{} },
		"纸张":     func(s *Settings) { s.Paper = "Letter" },
		"缩放下限":   func(s *Settings) { s.Scale = 0 },
		"缩放上限":   func(s *Settings) { s.Scale = 3 },
		"缩放非数值":  func(s *Settings) { s.Scale = math.NaN() },
		"负边距":    func(s *Settings) { s.MarginMM.Left = -1 },
		"无穷边距":   func(s *Settings) { s.MarginMM.Top = math.Inf(1) },
		"横向无内容区": func(s *Settings) { s.MarginMM.Right = 200 },
		"纵向无内容区": func(s *Settings) { s.MarginMM.Bottom = 287 },
	} {
		t.Run(name, func(t *testing.T) {
			settings := DefaultSettings()
			change(&settings)
			if _, _, err := r.Render(context.Background(), []byte("<p>单据</p>"), settings); err == nil || !strings.Contains(err.Error(), "排版设置") {
				t.Fatalf("无效设置未在启动前被拒绝：%v", err)
			}
		})
	}
}
