package render

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

const contentPolicy = "default-src 'none'; script-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:; base-uri 'none'; form-action 'none'; frame-src 'none'; object-src 'none'"

func prepareHTML(input []byte) (string, error) {
	if !utf8.Valid(input) || len(bytes.TrimSpace(input)) == 0 {
		return "", fmt.Errorf("HTML 必须是非空 UTF-8 文档")
	}
	doc, err := html.Parse(bytes.NewReader(input))
	if err != nil {
		return "", fmt.Errorf("解析 HTML: %w", err)
	}
	var head *html.Node
	var visit func(*html.Node) error
	visit = func(n *html.Node) error {
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			switch tag {
			case "script", "noscript", "canvas", "iframe", "frame", "frameset", "object", "embed", "applet", "base", "link", "audio", "video", "source", "track", "portal", "fencedframe", "foreignobject", "animate", "animatemotion", "animatetransform", "set":
				return fmt.Errorf("静态打印稿不支持 <%s>，请将样式和资源内联", tag)
			}
			if tag == "head" && n.Namespace == "" {
				head = n
			}
			for _, attr := range n.Attr {
				key := strings.ToLower(attr.Key)
				value := strings.TrimSpace(attr.Val)
				if tag == "use" && key == "href" && !strings.HasPrefix(value, "#") {
					return fmt.Errorf("SVG use 仅支持文档内锚点")
				}
				if strings.HasPrefix(key, "on") || key == "srcdoc" || key == "srcset" || key == "imagesrcset" || key == "http-equiv" {
					return fmt.Errorf("静态打印稿不支持 %s 属性", key)
				}
				switch key {
				case "src", "href", "background", "poster", "data", "action", "formaction", "ping", "manifest":
					if value != "" && !strings.HasPrefix(value, "#") && !strings.HasPrefix(strings.ToLower(value), "data:") {
						return fmt.Errorf("%s.%s 仅允许 data URL 或文档内锚点", tag, key)
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if err := visit(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(doc); err != nil {
		return "", err
	}
	if head == nil {
		return "", fmt.Errorf("HTML 缺少可用的 head 元素")
	}
	// CSP 必须先于任何用户内容，避免浏览器预加载外部资源
	policy := &html.Node{Type: html.ElementNode, Data: "meta", Attr: []html.Attribute{
		{Key: "http-equiv", Val: "Content-Security-Policy"},
		{Key: "content", Val: contentPolicy},
	}}
	head.InsertBefore(policy, head.FirstChild)
	var out bytes.Buffer
	if err := html.Render(&out, doc); err != nil {
		return "", fmt.Errorf("序列化 HTML: %w", err)
	}
	return out.String(), nil
}
