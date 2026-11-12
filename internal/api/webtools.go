package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
	"golang.org/x/net/html"
)

var webHTTP = &http.Client{Timeout: 15 * time.Second}

func (c *chatToolSet) webSearch() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "web_search",
		Description: "Search the web. Returns titles, URLs and snippets. Use it to find known CTF challenges, writeups, encoding references, and documentation.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
			"required": []any{"query"},
		},
		ReadOnly: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var in struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return tool.Errorf("invalid input: " + err.Error()), nil
			}
			q := strings.TrimSpace(in.Query)
			if q == "" {
				return tool.Errorf("query is required"), nil
			}
			body, _, _, status, err := webGet(ctx, "https://www.bing.com/search?q="+url.QueryEscape(q))
			var results []webResult
			if err == nil && status < 400 {
				results = parseBing(body)
			}
			if len(results) == 0 {
				b, _, _, status, err := webGet(ctx, "https://html.duckduckgo.com/html/?q="+url.QueryEscape(q))
				if err == nil && status < 400 {
					results = parseDuckDuckGo(b)
				}
			}
			if err != nil && len(results) == 0 {
				return tool.Errorf("web search: " + err.Error()), nil
			}
			if len(results) == 0 {
				return tool.Text("No search results (the search backend returned no parseable results)."), nil
			}
			if len(results) > 8 {
				results = results[:8]
			}
			var out strings.Builder
			for i, r := range results {
				fmt.Fprintf(&out, "%d. %s\n   %s\n", i+1, r.Title, r.URL)
				if r.Snippet != "" {
					fmt.Fprintf(&out, "   %s\n", r.Snippet)
				}
			}
			return tool.Text(strings.TrimSpace(out.String())), nil
		},
	})
}

func (c *chatToolSet) webFetch() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "web_fetch",
		Description: "Fetch a web page or API response and return readable text. Use it to read a page found by web_search.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string"},
			},
			"required": []any{"url"},
		},
		ReadOnly: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var in struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return tool.Errorf("invalid input: " + err.Error()), nil
			}
			u := strings.TrimSpace(in.URL)
			if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
				return tool.Errorf("url must start with http:// or https://"), nil
			}
			body, ct, finalURL, status, err := webGet(ctx, u)
			if err != nil {
				return tool.Errorf("web fetch: " + err.Error()), nil
			}
			if status >= 400 {
				return tool.Errorf(fmt.Sprintf("web fetch: HTTP %d", status)), nil
			}
			var header strings.Builder
			fmt.Fprintf(&header, "URL: %s\nStatus: %d\nContent-Type: %s\n\n", finalURL, status, ct)
			if strings.Contains(ct, "html") || strings.Contains(ct, "xml") {
				title, text := htmlText(body)
				if title != "" {
					header.WriteString("Title: " + title + "\n\n")
				}
				header.WriteString(text)
			} else if strings.HasPrefix(ct, "text/") || strings.Contains(ct, "json") {
				header.Write(body)
			} else {
				header.WriteString("(binary response; not shown)")
			}
			out := header.String()
			if len(out) > 32<<10 {
				out = out[:32<<10] + "\n…(truncated)"
			}
			return tool.Text(out), nil
		},
	})
}

type webResult struct{ Title, URL, Snippet string }

func webGet(ctx context.Context, rawURL string) ([]byte, string, string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", "", 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/124 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json,text/plain;q=0.9,*/*;q=0.8")
	resp, err := webHTTP.Do(req)
	if err != nil {
		return nil, "", "", 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", "", resp.StatusCode, err
	}
	return body, resp.Header.Get("Content-Type"), resp.Request.URL.String(), resp.StatusCode, nil
}

var ddgResultRE = regexp.MustCompile(`(?s)<a[^>]+class="[^"]*result__a[^"]*"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
var ddgSnippetRE = regexp.MustCompile(`(?s)<a[^>]+class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>`)
var bingResultRE = regexp.MustCompile(`(?s)<li class="b_algo".*?<h2[^>]*>\s*<a[^>]*href="([^"]+)"[^>]*>(.*?)</a>(.*?)(?:</li>|$)`)
var bingSnippetRE = regexp.MustCompile(`(?s)<div class="b_caption".*?<p[^>]*>(.*?)</p>`)

func parseDuckDuckGo(body []byte) []webResult {
	matches := ddgResultRE.FindAllSubmatch(body, 8)
	snips := ddgSnippetRE.FindAllSubmatch(body, 8)
	var out []webResult
	for i, m := range matches {
		href := html.UnescapeString(string(m[1]))
		if strings.Contains(href, "uddg=") {
			if u, err := url.Parse(href); err == nil {
				if v := u.Query().Get("uddg"); v != "" {
					href = v
				}
			}
		}
		r := webResult{Title: strings.TrimSpace(stripTags(string(m[2]))), URL: href}
		if i < len(snips) {
			r.Snippet = truncateText(strings.TrimSpace(stripTags(string(snips[i][1]))), 240)
		}
		if r.Title != "" && r.URL != "" {
			out = append(out, r)
		}
	}
	return out
}

func parseBing(body []byte) []webResult {
	matches := bingResultRE.FindAllSubmatch(body, 8)
	var out []webResult
	snips := bingSnippetRE.FindAllSubmatch(body, 8)
	for i, m := range matches {
		r := webResult{
			Title: strings.TrimSpace(stripTags(string(m[2]))),
			URL:   html.UnescapeString(string(m[1])),
		}
		if i < len(snips) {
			r.Snippet = truncateText(strings.TrimSpace(stripTags(string(snips[i][1]))), 240)
		}
		out = append(out, r)
	}
	return out
}

func stripTags(s string) string {
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return s
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return strings.Join(strings.Fields(b.String()), " ")
}

func htmlText(body []byte) (string, string) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", strings.Join(strings.Fields(string(body)), " ")
	}
	var title string
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "svg":
				return
			case "title":
				if n.FirstChild != nil {
					title = strings.TrimSpace(n.FirstChild.Data)
				}
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return title, strings.Join(strings.Fields(b.String()), " ")
}
