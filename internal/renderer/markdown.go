package renderer

import (
	"bytes"
	"fmt"
	"regexp"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

var httpsURLRE = regexp.MustCompile(`^https://`)

// newMarkdown builds the Goldmark renderer: GFM plus publish-time syntax
// highlighting emitted as Chroma CSS classes (no inline styles, no runtime JS).
func newMarkdown() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			highlighting.NewHighlighting(
				highlighting.WithFormatOptions(
					chromahtml.WithClasses(true),
					chromahtml.TabWidth(4),
				),
			),
		),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
}

// newSanitizer builds the HTML policy applied to all rendered user content.
// Scripts, event attributes, javascript: URLs, iframes and forms are stripped;
// links are limited to https.
func newSanitizer() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements(
		"a", "abbr", "blockquote", "br", "code", "dd", "del", "details", "div",
		"dl", "dt", "em", "figcaption", "figure", "h1", "h2", "h3", "h4", "h5",
		"h6", "hr", "input", "li", "mark", "ol", "p", "pre", "s", "section",
		"span", "strong", "sub", "summary", "sup", "table", "tbody", "td",
		"tfoot", "th", "thead", "tr", "u", "ul",
	)
	// Chroma emits highlighting purely via class names; classes cannot inject
	// script or style, so allowing the attribute globally is safe.
	p.AllowAttrs("class").Globally()
	p.AllowAttrs("id").OnElements("h1", "h2", "h3", "h4", "h5", "h6")
	p.AllowAttrs("href").Matching(httpsURLRE).OnElements("a")
	p.AllowAttrs("title").OnElements("a", "abbr")
	p.AllowAttrs("type").Matching(regexp.MustCompile(`^checkbox$`)).OnElements("input")
	p.AllowAttrs("checked", "disabled").OnElements("input")
	p.RequireNoFollowOnLinks(true)
	p.RequireNoReferrerOnLinks(true)
	return p
}

// renderMarkdown converts a Markdown source to sanitized HTML.
func (r *Renderer) renderMarkdown(source string) (string, error) {
	var buf bytes.Buffer
	if err := r.markdown.Convert([]byte(source), &buf); err != nil {
		return "", err
	}
	return r.policy.Sanitize(buf.String()), nil
}

// renderCode highlights a standalone code block with Chroma at publish time.
func (r *Renderer) renderCode(content, language string) (string, error) {
	lexer := lexers.Get(language)
	if lexer == nil {
		lexer = lexers.Analyse(content)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)
	iterator, err := lexer.Tokenise(nil, content)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := r.codeFormatter.Format(&buf, styles.Fallback, iterator); err != nil {
		return "", err
	}
	return r.policy.Sanitize(buf.String()), nil
}

// chromaCSS renders the light/dark highlighting stylesheets. The dark variant
// is wrapped in a media query so it only overrides under dark theme.
func chromaCSS() (string, error) {
	formatter := chromahtml.New(chromahtml.WithClasses(true))
	var light, dark bytes.Buffer
	if err := formatter.WriteCSS(&light, styles.Get("github")); err != nil {
		return "", fmt.Errorf("light chroma css: %w", err)
	}
	if err := formatter.WriteCSS(&dark, styles.Get("github-dark")); err != nil {
		return "", fmt.Errorf("dark chroma css: %w", err)
	}
	return light.String() +
		"\n@media (prefers-color-scheme: dark) {\nhtml:not([data-theme=\"light\"]) " +
		"{ color-scheme: dark; }\n" + dark.String() + "}\n", nil
}
