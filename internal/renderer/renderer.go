// Package renderer turns a ConversationSnapshot into immutable page and embed
// HTML at publish time. All user content is rendered through the Markdown
// pipeline plus sanitizer before it reaches a template.
package renderer

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"fmt"
	"html"
	"html/template"
	"net/url"
	"strings"
	"time"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"

	"github.com/jihuayu/chatgpt-share-page/internal/conversation"
	"github.com/jihuayu/chatgpt-share-page/web"
)

//go:embed templates
var templatesFS embed.FS

// Version is bumped whenever the rendered output shape changes.
const Version = "r1"

// pageScript powers copy buttons and the theme toggle on full pages.
const pageScript = `(function(){
"use strict";
document.querySelectorAll("pre").forEach(function(pre){
  var btn=document.createElement("button");
  btn.type="button";btn.className="copy-btn";btn.textContent="copy";
  btn.addEventListener("click",function(){
    var clone=pre.cloneNode(true);
    clone.querySelectorAll(".copy-btn").forEach(function(b){b.remove();});
    var text=clone.textContent||"";
    if(navigator.clipboard&&navigator.clipboard.writeText){
      navigator.clipboard.writeText(text);
    }
    btn.textContent="copied";btn.classList.add("copied");
    setTimeout(function(){btn.textContent="copy";btn.classList.remove("copied");},1500);
  });
  pre.appendChild(btn);
});
var stored=null;
try{stored=localStorage.getItem("csp-theme");}catch(e){}
if(stored==="dark"||stored==="light"){document.documentElement.setAttribute("data-theme",stored);}
var toggle=document.getElementById("theme-toggle");
if(toggle){toggle.addEventListener("click",function(){
  var cur=document.documentElement.getAttribute("data-theme");
  var sys=window.matchMedia&&window.matchMedia("(prefers-color-scheme: dark)").matches?"dark":"light";
  var next=(cur||sys)==="dark"?"light":"dark";
  document.documentElement.setAttribute("data-theme",next);
  try{localStorage.setItem("csp-theme",next);}catch(e){}
});}
})();`

// copyScript is the embed-page variant: copy buttons plus height sync.
const copyScript = `(function(){
"use strict";
document.querySelectorAll("pre").forEach(function(pre){
  var btn=document.createElement("button");
  btn.type="button";btn.className="copy-btn";btn.textContent="copy";
  btn.addEventListener("click",function(){
    var clone=pre.cloneNode(true);
    clone.querySelectorAll(".copy-btn").forEach(function(b){b.remove();});
    var text=clone.textContent||"";
    if(navigator.clipboard&&navigator.clipboard.writeText){
      navigator.clipboard.writeText(text);
    }
    btn.textContent="copied";btn.classList.add("copied");
    setTimeout(function(){btn.textContent="copy";btn.classList.remove("copied");},1500);
  });
  pre.appendChild(btn);
});
})();`

// Renderer renders snapshots into immutable HTML artifacts.
type Renderer struct {
	pageTmpl      *template.Template
	embedTmpl     *template.Template
	markdown      goldmark.Markdown
	policy        *bluemonday.Policy
	codeFormatter *chromahtml.Formatter
	css           string
	pageScript    string
	embedScript   string
	pageCSP       string
	embedCSP      string
}

// RenderError indicates HTML generation failed.
type RenderError struct{ Err error }

func (e *RenderError) Error() string { return "render failed: " + e.Err.Error() }
func (e *RenderError) Unwrap() error { return e.Err }

// New loads embedded templates and assets and precomputes CSP script hashes.
func New() (*Renderer, error) {
	pageTmpl, err := template.New("page").ParseFS(templatesFS, "templates/page.html", "templates/message.html")
	if err != nil {
		return nil, fmt.Errorf("parse page template: %w", err)
	}
	embedTmpl, err := template.New("embed").ParseFS(templatesFS, "templates/embed.html", "templates/message.html")
	if err != nil {
		return nil, fmt.Errorf("parse embed template: %w", err)
	}
	appCSS, err := web.Static.ReadFile("static/app.css")
	if err != nil {
		return nil, fmt.Errorf("read app.css: %w", err)
	}
	embedJS, err := web.Static.ReadFile("static/embed.js")
	if err != nil {
		return nil, fmt.Errorf("read embed.js: %w", err)
	}
	codeCSS, err := chromaCSS()
	if err != nil {
		return nil, err
	}
	r := &Renderer{
		pageTmpl:      pageTmpl,
		embedTmpl:     embedTmpl,
		markdown:      newMarkdown(),
		policy:        newSanitizer(),
		codeFormatter: chromahtml.New(chromahtml.WithClasses(true), chromahtml.TabWidth(4)),
		css:           string(appCSS) + "\n" + codeCSS,
		pageScript:    pageScript,
		embedScript:   copyScript + "\n" + string(embedJS),
	}
	r.pageCSP = cspHeader(r.pageScript, "'self'")
	r.embedCSP = cspHeader(r.embedScript, "https:")
	return r, nil
}

// PageCSP returns the Content-Security-Policy for full pages.
func (r *Renderer) PageCSP() string { return r.pageCSP }

// EmbedCSP returns the Content-Security-Policy for embed pages; frame-ancestors
// is open so any site may iframe the embed.
func (r *Renderer) EmbedCSP() string { return r.embedCSP }

func cspHeader(script, frameAncestors string) string {
	sum := sha256.Sum256([]byte(script))
	hash := base64.StdEncoding.EncodeToString(sum[:])
	return "default-src 'none'; " +
		"style-src 'unsafe-inline'; " +
		"img-src https: data:; " +
		"font-src 'none'; " +
		"connect-src 'none'; " +
		"script-src 'sha256-" + hash + "'; " +
		"base-uri 'none'; form-action 'none'; " +
		"frame-ancestors " + frameAncestors
}

// RenderPage produces the immutable full page HTML for a snapshot.
func (r *Renderer) RenderPage(snapshot *conversation.ConversationSnapshot) ([]byte, error) {
	data, err := r.viewData(snapshot, r.pageScript, false)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := r.pageTmpl.ExecuteTemplate(&buf, "page.html", data); err != nil {
		return nil, &RenderError{Err: err}
	}
	return buf.Bytes(), nil
}

// RenderEmbed produces the immutable embed page HTML for a snapshot.
func (r *Renderer) RenderEmbed(snapshot *conversation.ConversationSnapshot) ([]byte, error) {
	data, err := r.viewData(snapshot, r.embedScript, true)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := r.embedTmpl.ExecuteTemplate(&buf, "embed.html", data); err != nil {
		return nil, &RenderError{Err: err}
	}
	return buf.Bytes(), nil
}

type viewData struct {
	Title        string
	SourceURL    string
	SourceHost   string
	ImportedAt   string
	UpdatedAt    string
	Model        string
	MessageCount int
	Messages     []msgView
	CSS          template.CSS
	Script       template.JS
	NoIndex      bool
	Embed        bool
}

type msgView struct {
	Index     int
	Anchor    string
	Role      string
	RoleLabel string
	CreatedAt string
	Hidden    bool
	Blocks    []template.HTML
}

func (r *Renderer) viewData(snapshot *conversation.ConversationSnapshot, script string, embed bool) (viewData, error) {
	location := time.UTC
	if tz := snapshot.Metadata.Timezone; tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			location = loc
		}
	}
	messages := make([]msgView, 0, len(snapshot.Messages))
	for index, msg := range snapshot.Messages {
		blocks := make([]template.HTML, 0, len(msg.Blocks))
		for _, block := range msg.Blocks {
			rendered, err := r.blockHTML(block)
			if err != nil {
				return viewData{}, &RenderError{Err: err}
			}
			if rendered != "" {
				blocks = append(blocks, rendered)
			}
		}
		view := msgView{
			Index:     index + 1,
			Anchor:    fmt.Sprintf("msg-%d", index+1),
			Role:      msg.Role,
			RoleLabel: roleLabel(msg.Role),
			Hidden:    msg.Hidden,
			Blocks:    blocks,
		}
		if msg.CreatedAt != nil {
			view.CreatedAt = msg.CreatedAt.In(location).Format("2006-01-02 15:04 MST")
		}
		messages = append(messages, view)
	}
	return viewData{
		Title:        snapshot.Title,
		SourceURL:    snapshot.Source.URL,
		SourceHost:   hostOf(snapshot.Source.URL),
		ImportedAt:   snapshot.ImportedAt.In(location).Format("2006-01-02 15:04 MST"),
		UpdatedAt:    snapshot.UpdatedAt.In(location).Format("2006-01-02 15:04 MST"),
		Model:        snapshot.Metadata.Model,
		MessageCount: snapshot.Metadata.MessageCount,
		Messages:     messages,
		CSS:          template.CSS(r.css),
		Script:       template.JS(script),
		NoIndex:      true,
		Embed:        embed,
	}, nil
}

// blockHTML renders one ContentBlock to a safe HTML fragment.
func (r *Renderer) blockHTML(block conversation.ContentBlock) (template.HTML, error) {
	switch block.Type {
	case "markdown":
		out, err := r.renderMarkdown(block.Content)
		if err != nil {
			return "", fmt.Errorf("markdown block: %w", err)
		}
		return template.HTML(out), nil
	case "code":
		out, err := r.renderCode(block.Content, block.Language)
		if err != nil {
			return "", fmt.Errorf("code block: %w", err)
		}
		return template.HTML(out), nil
	case "quote":
		out, err := r.renderMarkdown(block.Content)
		if err != nil {
			return "", fmt.Errorf("quote block: %w", err)
		}
		var b strings.Builder
		b.WriteString(`<div class="block-quote">`)
		if block.Title != "" {
			b.WriteString(`<div class="quote-title">` + html.EscapeString(block.Title) + `</div>`)
		}
		b.WriteString(out)
		b.WriteString(`</div>`)
		return template.HTML(b.String()), nil
	case "citation":
		if !strings.HasPrefix(block.URL, "https://") {
			return "", nil
		}
		title := block.Title
		if title == "" {
			title = block.URL
		}
		return template.HTML(`<span class="citation"><a href="` + html.EscapeString(block.URL) +
			`" target="_blank" rel="nofollow noopener noreferrer">` + html.EscapeString(title) + `</a></span>`), nil
	case "attachment":
		var b strings.Builder
		b.WriteString(`<div class="attachment"><span class="att-icon">file</span>`)
		b.WriteString(`<span class="att-name">` + html.EscapeString(block.Title) + `</span>`)
		if block.Content != "" {
			b.WriteString(`<span class="att-desc">` + html.EscapeString(block.Content) + `</span>`)
		}
		b.WriteString(`</div>`)
		return template.HTML(b.String()), nil
	default:
		return "", nil
	}
}

func roleLabel(role string) string {
	if role == "" {
		return "Unknown"
	}
	words := strings.Fields(strings.ReplaceAll(role, "_", " "))
	for i, word := range words {
		if word != "" {
			words[i] = strings.ToUpper(word[:1]) + word[1:]
		}
	}
	return strings.Join(words, " ")
}

func hostOf(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Host
}
