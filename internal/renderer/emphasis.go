package renderer

import (
	"bytes"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// chatStrongParser relaxes punctuation boundaries next to CJK text, as used
// by ChatGPT. Parsing delimiters (rather than rewriting source) leaves escaped
// markers, inline code and fenced code untouched.
type chatStrongParser struct{}
type chatStrongProcessor struct{}

func (*chatStrongProcessor) IsDelimiter(b byte) bool                   { return b == '*' }
func (*chatStrongProcessor) CanOpenCloser(a, b *parser.Delimiter) bool { return a.Char == b.Char }
func (*chatStrongProcessor) OnMatch(n int) ast.Node                    { return ast.NewEmphasis(n) }

var strongProcessor = &chatStrongProcessor{}

func (*chatStrongParser) Trigger() []byte { return []byte{'*'} }
func (*chatStrongParser) Parse(parent ast.Node, reader text.Reader, pc parser.Context) ast.Node {
	line, segment := reader.PeekLine()
	if len(line) < 2 || line[0] != '*' || line[1] != '*' || (len(line) > 2 && line[2] == '*') {
		return nil
	}
	before := reader.PrecendingCharacter()
	after, _ := utf8.DecodeRune(line[2:])
	node := parser.ScanDelimiter(line, before, 2, strongProcessor)
	if node == nil {
		return nil
	}
	// A closing punctuation mark followed immediately by Chinese is still a
	// word boundary. Also tolerate a space after a colon in a bold label.
	if unicode.Is(unicode.Han, after) {
		prior, _ := utf8.DecodeLastRune(bytes.TrimRight(reader.Source()[:segment.Start], " \t"))
		if unicode.IsPunct(prior) && (prior == before || prior == ':' || prior == '：') {
			node.CanClose = true
		}
	}
	if unicode.Is(unicode.Han, before) && unicode.IsPunct(after) {
		node.CanOpen = true
	}
	node.Segment = segment.WithStop(segment.Start + 2)
	reader.Advance(2)
	pc.PushDelimiter(node)
	return node
}
