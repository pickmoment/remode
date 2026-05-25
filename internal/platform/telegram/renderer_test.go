package telegram

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMdToHTML_Headings(t *testing.T) {
	assert.Equal(t, "<b>TITLE</b>", mdToHTML("# Title"))
	assert.Equal(t, "<b>Section</b>", mdToHTML("## Section"))
	assert.Equal(t, "<b><i>Sub</i></b>", mdToHTML("### Sub"))
}

func TestMdToHTML_HorizontalRule(t *testing.T) {
	assert.Equal(t, "──────────", mdToHTML("---"))
	assert.Equal(t, "──────────", mdToHTML("----"))
	assert.Equal(t, "──────────", mdToHTML("***"))
	assert.Equal(t, "──────────", mdToHTML("___"))
}

func TestMdToHTML_Bullets(t *testing.T) {
	assert.Equal(t, "• item", mdToHTML("- item"))
	assert.Equal(t, "• item", mdToHTML("* item"))
	assert.Equal(t, "• alpha\n• beta", mdToHTML("- alpha\n- beta"))
}

func TestMdToHTML_Blockquote(t *testing.T) {
	assert.Equal(t, "│ note", mdToHTML("> note"))
	assert.Equal(t, "│ note", mdToHTML(">note"))
}

func TestMdToHTML_Bold(t *testing.T) {
	assert.Equal(t, "<b>hello</b>", mdToHTML("**hello**"))
	assert.Equal(t, "<b>hello</b>", mdToHTML("__hello__"))
}

func TestMdToHTML_Italic(t *testing.T) {
	assert.Equal(t, "<i>hello</i>", mdToHTML("*hello*"))
}

func TestMdToHTML_Strikethrough(t *testing.T) {
	assert.Equal(t, "<s>old</s>", mdToHTML("~~old~~"))
}

func TestMdToHTML_InlineCode(t *testing.T) {
	assert.Equal(t, "<code>x := 1</code>", mdToHTML("`x := 1`"))
}

func TestMdToHTML_CodeBlock(t *testing.T) {
	input := "```go\nfmt.Println(\"hi\")\n```"
	out := mdToHTML(input)
	assert.Contains(t, out, "<pre><code>")
	assert.Contains(t, out, "fmt.Println(&#34;hi&#34;)")
	assert.Contains(t, out, "</code></pre>")
	assert.NotContains(t, out, "```")
}

func TestMdToHTML_CodeBlockNoLang(t *testing.T) {
	input := "```\nhello\n```"
	out := mdToHTML(input)
	assert.Contains(t, out, "<pre><code>hello</code></pre>")
}

func TestMdToHTML_Link(t *testing.T) {
	out := mdToHTML("[Go docs](https://pkg.go.dev)")
	assert.Equal(t, `<a href="https://pkg.go.dev">Go docs</a>`, out)
}

func TestMdToHTML_HTMLEscape(t *testing.T) {
	assert.Equal(t, "a &amp; b", mdToHTML("a & b"))
	assert.Equal(t, "&lt;tag&gt;", mdToHTML("<tag>"))
}

func TestMdToHTML_BulletWithInlineCode(t *testing.T) {
	out := mdToHTML("- run `go test`")
	assert.Equal(t, "• run <code>go test</code>", out)
}

func TestMdToHTML_HeadingWithCode(t *testing.T) {
	out := mdToHTML("## Install `remode`")
	assert.Equal(t, "<b>Install <code>remode</code></b>", out)
}

func TestMdToHTML_MixedBoldItalic(t *testing.T) {
	out := mdToHTML("**bold** and *italic*")
	assert.Equal(t, "<b>bold</b> and <i>italic</i>", out)
}

func TestMdToHTML_BoldDoesNotTriggerItalic(t *testing.T) {
	// ** should not produce italic matches
	out := mdToHTML("**word**")
	assert.Equal(t, "<b>word</b>", out)
	assert.NotContains(t, out, "<i>")
}

func TestMdToHTML_CodePreservesContent(t *testing.T) {
	// Inline code with markdown-like content should not be processed
	out := mdToHTML("`**not bold**`")
	assert.Equal(t, "<code>**not bold**</code>", out)
	assert.NotContains(t, out, "<b>")
}

func TestMdToHTML_MultilineBullets(t *testing.T) {
	// Two bullet lines should both convert, not trigger italic across lines
	out := mdToHTML("* one\n* two")
	assert.Equal(t, "• one\n• two", out)
	assert.NotContains(t, out, "<i>")
}

func TestMdToHTML_TableWithInlineCode(t *testing.T) {
	input := "| Command | Description |\n|---------|-------------|\n| `/new` | New session |"
	out := mdToHTML(input)
	assert.Contains(t, out, "<code>/new</code>")
	assert.Contains(t, out, "New session")
	assert.NotContains(t, out, "|---|")
}

func TestMdToHTML_Empty(t *testing.T) {
	assert.Equal(t, "", mdToHTML(""))
}

func TestMdToHTML_PlainText(t *testing.T) {
	assert.Equal(t, "hello world", mdToHTML("hello world"))
}
