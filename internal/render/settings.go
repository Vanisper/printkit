package render

import (
	"fmt"
	"math"
)

// Settings 是单份 HTML 生成 PDF 时使用的完整排版设置，零值无效
type Settings struct {
	// Paper 指定缺省纸张，目前支持 A4
	Paper string `json:"paper"`
	// PreferCSSPageSize 允许文档的 @page 覆盖纸张尺寸
	PreferCSSPageSize   bool `json:"prefer_css_page_size"`
	PrintBackground     bool `json:"print_background"`
	DisplayHeaderFooter bool `json:"display_header_footer"`
	// MarginMM 指定未被文档 @page 覆盖的页面边距，单位为毫米
	MarginMM Margins `json:"margin_mm"`
	// Scale 为内容缩放比例，范围为 0.1～2
	Scale float64 `json:"scale"`
}

// Margins 是页面四边的非负毫米边距
type Margins struct {
	Top    float64 `json:"top"`
	Right  float64 `json:"right"`
	Bottom float64 `json:"bottom"`
	Left   float64 `json:"left"`
}

// DefaultSettings 返回新任务的完整排版设置
func DefaultSettings() Settings {
	return Settings{
		Paper: "A4", PreferCSSPageSize: true, PrintBackground: true,
		MarginMM: Margins{Top: 10, Right: 10, Bottom: 10, Left: 10}, Scale: 1,
	}
}

// Validate 检查纸张、缩放及边距是否可用于排版
func (s Settings) Validate() error {
	if s.Paper != "A4" {
		return fmt.Errorf("不支持的缺省纸张 %q", s.Paper)
	}
	if math.IsNaN(s.Scale) || s.Scale < 0.1 || s.Scale > 2 {
		return fmt.Errorf("缩放比例须为 0.1～2")
	}
	for _, margin := range []float64{s.MarginMM.Top, s.MarginMM.Right, s.MarginMM.Bottom, s.MarginMM.Left} {
		if math.IsNaN(margin) || math.IsInf(margin, 0) || margin < 0 {
			return fmt.Errorf("页面边距须为有限的非负数")
		}
	}
	if s.MarginMM.Left+s.MarginMM.Right >= 210 || s.MarginMM.Top+s.MarginMM.Bottom >= 297 {
		return fmt.Errorf("页面边距须保留可用的 A4 内容区域")
	}
	return nil
}
