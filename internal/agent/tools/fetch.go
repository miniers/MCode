package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"mcode/internal/agent/llm"

	"golang.org/x/net/html"
)

const (
	fetchTimeout  = 30 * time.Second
	fetchBodyMax  = 5 << 20
	fetchCharsMax = 50000
)

// WebFetchTool 返回抓取网页并转为可读文本的工具。
func WebFetchTool() Tool {
	return Tool{
		Name:        "web_fetch",
		Description: "Fetch a URL and return its content as readable text.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"},"maxChars":{"type":"integer","minimum":1}},"required":["url"]}`),
		ReadOnly:    true,
		Run:         webFetch,
	}
}

func webFetch(ctx context.Context, _ Env, raw json.RawMessage) (Result, error) {
	var a struct {
		URL      string `json:"url"`
		MaxChars int    `json:"maxChars"`
	}
	if e := parse(raw, &a); e != nil {
		return failure(e)
	}
	u, e := url.Parse(strings.TrimSpace(a.URL))
	if e != nil {
		return failure("invalid url")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return failure("unsupported url scheme: " + u.Scheme)
	}
	if u.Host == "" {
		return failure("invalid url")
	}
	maxChars := a.MaxChars
	if maxChars <= 0 {
		maxChars = fetchCharsMax
	}

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return failure(e)
	}
	req.Header.Set("User-Agent", llm.UserAgent)
	client := &http.Client{CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("stopped after 5 redirects")
		}
		return nil
	}}
	resp, e := client.Do(req)
	if e != nil {
		return failure(e)
	}
	defer resp.Body.Close()
	body, e := io.ReadAll(io.LimitReader(resp.Body, fetchBodyMax))
	if e != nil {
		return failure(e)
	}

	finalURL := resp.Request.URL.String()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Result{
			Text:    fmt.Sprintf("URL: %s\nStatus: %d\n\n%s", finalURL, resp.StatusCode, truncateRunes(string(body), 2048)),
			IsError: true,
		}, nil
	}

	ct := resp.Header.Get("Content-Type")
	if mt, _, err := mime.ParseMediaType(ct); err == nil {
		ct = mt
	} else if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.ToLower(strings.TrimSpace(ct))

	var text string
	switch {
	case ct == "text/html":
		text, e = htmlToText(string(body), resp.Request.URL)
		if e != nil {
			return failure(e)
		}
	case strings.HasPrefix(ct, "text/"), ct == "application/json", ct == "application/xml",
		strings.HasSuffix(ct, "+json"), strings.HasSuffix(ct, "+xml"):
		text = string(body)
	default:
		return failure("unsupported content type: " + ct)
	}

	text = truncateRunes(text, maxChars)
	return Result{Text: fmt.Sprintf("URL: %s\nStatus: %d\n\n%s", finalURL, resp.StatusCode, text)}, nil
}

// truncateRunes 按字符数截断文本，并在末尾说明剩余字符数。
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + fmt.Sprintf("\n[truncated: %d more chars]", len(runes)-max)
}

var (
	htmlSkipTags = map[string]bool{
		"script": true, "style": true, "noscript": true, "svg": true,
		"head": true, "template": true,
	}
	htmlBlockTags = map[string]bool{
		"address": true, "article": true, "aside": true, "blockquote": true,
		"body": true, "caption": true, "dd": true, "details": true, "dialog": true,
		"div": true, "dl": true, "dt": true, "fieldset": true, "figcaption": true,
		"figure": true, "footer": true, "form": true, "header": true, "hgroup": true,
		"hr": true, "html": true, "main": true, "nav": true, "ol": true, "p": true,
		"section": true, "summary": true, "table": true, "tbody": true, "td": true,
		"tfoot": true, "th": true, "thead": true, "tr": true, "ul": true,
	}
)

type textExtractor struct {
	b    *strings.Builder
	base *url.URL
	pre  []string
}

func htmlToText(src string, base *url.URL) (string, error) {
	doc, e := html.Parse(strings.NewReader(src))
	if e != nil {
		return "", e
	}
	x := &textExtractor{b: &strings.Builder{}, base: base}
	x.walk(doc)
	out := cleanLines(x.b.String())
	for i, p := range x.pre {
		out = strings.Replace(out, fmt.Sprintf("\x00PRE%d\x00", i), p, 1)
	}
	return out, nil
}

func (x *textExtractor) walk(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		x.b.WriteString(collapseSpaces(n.Data))
	case html.ElementNode:
		tag := strings.ToLower(n.Data)
		if htmlSkipTags[tag] {
			return
		}
		switch {
		case tag == "pre":
			x.writePre(n)
		case tag == "br":
			x.b.WriteString("\n")
		case tag == "a":
			x.writeLink(n)
		case len(tag) == 2 && tag[0] == 'h' && tag[1] >= '1' && tag[1] <= '6':
			x.writePrefixed(n, strings.Repeat("#", int(tag[1]-'0'))+" ")
		case tag == "li":
			x.writePrefixed(n, "- ")
		case htmlBlockTags[tag]:
			x.b.WriteString("\n")
			x.walkChildren(n)
			x.b.WriteString("\n")
		default:
			x.walkChildren(n)
		}
	case html.DocumentNode:
		x.walkChildren(n)
	}
}

func (x *textExtractor) walkChildren(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		x.walk(c)
	}
}

// capture 渲染子节点并返回其文本，用于带前缀的元素。
func (x *textExtractor) capture(n *html.Node) string {
	old := x.b
	x.b = &strings.Builder{}
	x.walkChildren(n)
	s := strings.TrimSpace(x.b.String())
	x.b = old
	return s
}

func (x *textExtractor) writePrefixed(n *html.Node, prefix string) {
	x.b.WriteString("\n" + prefix + x.capture(n) + "\n")
}

func (x *textExtractor) writeLink(n *html.Node) {
	text := x.capture(n)
	href := ""
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, "href") {
			href = strings.TrimSpace(a.Val)
			break
		}
	}
	if href == "" {
		x.b.WriteString(text)
		return
	}
	if ref, e := url.Parse(href); e == nil && x.base != nil {
		href = x.base.ResolveReference(ref).String()
	}
	if text == "" {
		x.b.WriteString(href)
		return
	}
	x.b.WriteString(text + " (" + href + ")")
}

func (x *textExtractor) writePre(n *html.Node) {
	var b strings.Builder
	var collect func(*html.Node)
	collect = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			collect(c)
		}
	}
	collect(n)
	x.pre = append(x.pre, b.String())
	x.b.WriteString(fmt.Sprintf("\n\x00PRE%d\x00\n", len(x.pre)-1))
}

func collapseSpaces(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !space {
				b.WriteByte(' ')
				space = true
			}
			continue
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// cleanLines 去掉行首尾空白并合并连续空行。
func cleanLines(s string) string {
	var out []string
	blank := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(out) > 0 && !blank {
				out = append(out, "")
				blank = true
			}
			continue
		}
		blank = false
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
