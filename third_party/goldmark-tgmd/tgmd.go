package tgmd

import (
	"bytes"
	"strconv"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	ext "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/renderer"
	textm "github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// TGMD (telegramMarkdown) endpoint.
func TGMD() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithRenderer(
			renderer.NewRenderer(
				renderer.WithNodeRenderers(util.Prioritized(NewRenderer(), 1000)),
			),
		),
		goldmark.WithExtensions(Strikethroughs),
		goldmark.WithExtensions(Hidden),
		goldmark.WithExtensions(DoubleSpace),
	)
}

// Renderer implement renderer.NodeRenderer object.
type Renderer struct {
	// listIndentStack holds the content-indentation prefix (a run of spaces)
	// for each currently-open list item. When a soft or hard line break occurs
	// inside a list item, the break is followed by the top-of-stack prefix so
	// that continuation lines align under the item's text instead of starting
	// at column 0.
	listIndentStack [][]byte
}

// NewRenderer initialize Renderer as renderer.NodeRenderer.
func NewRenderer() renderer.NodeRenderer {
	return &Renderer{}
}

// RegisterFuncs add AST objects to Renderer.
func (r *Renderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindDocument, r.document)
	reg.Register(ast.KindParagraph, r.paragraph)

	reg.Register(ast.KindText, r.renderText)
	reg.Register(ast.KindString, r.renderString)
	reg.Register(ast.KindEmphasis, r.emphasis)

	reg.Register(ast.KindHeading, r.heading)
	reg.Register(ast.KindList, r.list)
	reg.Register(ast.KindListItem, r.listItem)
	reg.Register(ast.KindLink, r.link)

	reg.Register(ast.KindBlockquote, r.blockquote)
	reg.Register(ast.KindFencedCodeBlock, r.code)
	reg.Register(ast.KindCodeSpan, r.codeSpan)

	reg.Register(ext.KindStrikethrough, r.strikethrough)
	reg.Register(KindHidden, r.hidden)
	reg.Register(KindDoubleSpace, r.doubleSpace)
}

func (r *Renderer) heading(w util.BufWriter, _ []byte, node ast.Node, entering bool) (
	ast.WalkStatus, error,
) {
	n := node.(*ast.Heading)
	if entering {
		if n.Level > 1 && n.Level < 4 {
			writeNewLine(w)
		}
		Config.headings[n.Level-1].writeStart(w)
	} else {
		Config.headings[n.Level-1].writeEnd(w)
	}
	return ast.WalkContinue, nil
}

func (r *Renderer) paragraph(w util.BufWriter, _ []byte, node ast.Node, entering bool) (
	ast.WalkStatus, error,
) {
	n := node.(*ast.Paragraph)
	if entering {
		parent := n.Parent()
		if parent.Kind().String() == ast.KindListItem.String() {
			// A paragraph that is the direct content of a list item must not
			// emit a leading newline: the item has already written its marker
			// (e.g. "  1. ") and the text has to follow on the same line.
			// A leading newline here would split the marker from its content,
			// rendering as "1.\ncontent" with a spurious blank line.
			//
			// For a subsequent paragraph in the same (multi-paragraph) item,
			// start on a new line aligned under the item's text via the
			// content-indent on top of the list-indent stack.
			if n.PreviousSibling() != nil {
				writeNewLine(w)
				if len(r.listIndentStack) > 0 {
					writeRowBytes(w, r.listIndentStack[len(r.listIndentStack)-1])
				}
			}
		} else if parent.Kind().String() != ast.KindBlockquote.String() {
			writeNewLine(w)
		}
	} else {
		writeNewLine(w)
	}
	return ast.WalkContinue, nil
}

func (r *Renderer) list(w util.BufWriter, _ []byte, node ast.Node, entering bool) (
	ast.WalkStatus, error,
) {
	n := node.(*ast.List)
	if !entering {
		if n.Parent().Kind().String() == ast.KindDocument.String() {
			writeNewLine(w)
		}
	}
	return ast.WalkContinue, nil
}

func (r *Renderer) listItem(w util.BufWriter, _ []byte, node ast.Node, entering bool) (
	ast.WalkStatus, error,
) {
	n := node.(*ast.ListItem)
	if !entering {
		// Pop this item's content-indent on the way out so that line breaks
		// after the item are no longer aligned to it.
		if len(r.listIndentStack) > 0 {
			r.listIndentStack = r.listIndentStack[:len(r.listIndentStack)-1]
		}
		return ast.WalkContinue, nil
	}
	writeNewLine(w)

	// Determine the indentation and bullet level from the nesting depth.
	indent := 2
	bullet := Config.listBullets[0]
	if n.Parent().Parent().Kind().String() != ast.KindDocument.String() {
		if n.Parent().Parent().Parent().Parent() != nil &&
			n.Parent().Parent().Parent().Parent().Kind().String() == ast.KindListItem.String() {
			indent = 6
			bullet = Config.listBullets[2]
		} else {
			indent = 4
			bullet = Config.listBullets[1]
		}
	}
	writeRowBytes(w, bytes.Repeat([]byte{SpaceChar.Byte()}, indent))

	// contentIndent is the run of spaces that aligns continuation lines (soft
	// or hard line breaks) under the item's first text character. It equals
	// the list indentation plus the marker's visual width plus the trailing
	// space that separates the marker from the text.
	var contentIndent []byte
	if list, ok := n.Parent().(*ast.List); ok && list.IsOrdered() {
		// Ordered list: render the item number (the list's Start value plus
		// this item's position among its siblings) followed by the list marker
		// ('.' or ')') instead of a bullet, so the original numbering is kept.
		num := list.Start
		if num < 1 {
			num = 1
		}
		for c := n.PreviousSibling(); c != nil; c = c.PreviousSibling() {
			num++
		}
		numStr := strconv.Itoa(num)
		writeCustomBytes(w, []byte(numStr))
		writeCustomBytes(w, []byte{list.Marker})
		contentIndent = bytes.Repeat(
			[]byte{SpaceChar.Byte()},
			indent+len(numStr)+1+1, // indent + number + marker + separating space
		)
	} else {
		writeRune(w, bullet)
		contentIndent = bytes.Repeat(
			[]byte{SpaceChar.Byte()},
			indent+1+1, // indent + bullet + separating space
		)
	}
	writeRowBytes(w, []byte{SpaceChar.Byte()})
	r.listIndentStack = append(r.listIndentStack, contentIndent)
	return ast.WalkContinue, nil
}

func (r *Renderer) code(w util.BufWriter, source []byte, node ast.Node, entering bool) (
	ast.WalkStatus, error,
) {
	n := node.(interface {
		Lines() *textm.Segments
	})
	var content []byte
	l := n.Lines().Len()
	for i := 0; i < l; i++ {
		line := n.Lines().At(i)
		content = append(content, line.Value(source)...)
	}
	content = bytes.ReplaceAll(
		content,
		[]byte{TabChar.Byte()},
		[]byte{SpaceChar.Byte(), SpaceChar.Byte(), SpaceChar.Byte()},
	)
	nn := node.(*ast.FencedCodeBlock)
	if entering {
		writeNewLine(w)
		writeWrapperArr(w.Write(CodeTg.Bytes()))
		writeWrapperArr(w.Write(nn.Language(source)))
	} else {
		writeNewLine(w)
		writeWrapperArr(w.Write(content))
		writeWrapperArr(w.Write(CodeTg.Bytes()))
		writeNewLine(w)
	}
	return ast.WalkContinue, nil
}

func (r *Renderer) renderText(w util.BufWriter, source []byte, node ast.Node, entering bool) (
	ast.WalkStatus, error,
) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.Text)
	// n.Segment.Value returns a sub-slice of source, so it must not be
	// appended to in place (that would overwrite the following source bytes).
	// Write the text first, then the line break and indentation separately.
	render(w, n.Segment.Value(source))
	if n.HardLineBreak() || n.SoftLineBreak() {
		// A soft line break (a single newline within a paragraph, including
		// lazy-continuation lines) or a hard line break is rendered as a
		// newline. Without this the text on either side of the break would be
		// concatenated (e.g. a line following a tight list item would merge
		// into the item's text). When inside a list item, the break is followed
		// by the item's content indentation so continuation lines align under
		// the item's text instead of starting at column 0.
		writeNewLine(w)
		if len(r.listIndentStack) > 0 {
			writeRowBytes(w, r.listIndentStack[len(r.listIndentStack)-1])
		}
	}
	return ast.WalkContinue, nil
}

func (r *Renderer) renderString(w util.BufWriter, source []byte, node ast.Node, entering bool) (
	ast.WalkStatus, error,
) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.String)
	_, _ = w.Write(n.Value)
	// A DoubleSpace hard line break is represented as a String node whose
	// value is a newline. When inside a list item, follow it with the item's
	// content indentation so the wrapped line aligns under the item's text.
	if len(r.listIndentStack) > 0 && len(n.Value) > 0 &&
		n.Value[len(n.Value)-1] == NewLineChar.Byte() {
		_, _ = w.Write(r.listIndentStack[len(r.listIndentStack)-1])
	}
	return ast.WalkContinue, nil
}

func (r *Renderer) emphasis(w util.BufWriter, _ []byte, node ast.Node, _ bool) (
	ast.WalkStatus, error,
) {
	n := node.(*ast.Emphasis)
	if n.Level == 2 {
		writeRowBytes(w, BoldTg.Bytes())
	}
	if n.Level == 1 {
		writeRowBytes(w, ItalicsTg.Bytes())
	}
	return ast.WalkContinue, nil
}

func (r *Renderer) link(w util.BufWriter, _ []byte, node ast.Node, entering bool) (
	ast.WalkStatus, error,
) {
	n := node.(*ast.Link)
	if entering {
		writeRowBytes(w, []byte{OpenBracketChar.Byte()})
	} else {
		writeRowBytes(w, []byte{CloseBracketChar.Byte(), OpenParenChar.Byte()})
		writeRowBytes(w, n.Destination)
		writeRowBytes(w, []byte{CloseParenChar.Byte()})
	}
	return ast.WalkContinue, nil
}

func (r *Renderer) blockquote(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (
	ast.WalkStatus, error,
) {
	if entering {
		writeNewLine(w)
		writeRowBytes(w, []byte{GreaterThanChar.Byte()})
	}
	return ast.WalkContinue, nil
}

func (r *Renderer) codeSpan(w util.BufWriter, _ []byte, _ ast.Node, _ bool) (
	ast.WalkStatus, error,
) {
	writeWrapperArr(w.Write(SpanTg.Bytes()))
	return ast.WalkContinue, nil
}

func (r *Renderer) strikethrough(w util.BufWriter, _ []byte, _ ast.Node, _ bool) (
	ast.WalkStatus, error,
) {
	writeWrapperArr(w.Write(StrikethroughTg.Bytes()))
	return ast.WalkContinue, nil
}

func (r *Renderer) hidden(w util.BufWriter, _ []byte, _ ast.Node, _ bool) (
	ast.WalkStatus, error,
) {
	writeWrapperArr(w.Write(HiddenTg.Bytes()))
	return ast.WalkContinue, nil
}

func (r *Renderer) doubleSpace(_ util.BufWriter, _ []byte, _ ast.Node, _ bool) (
	ast.WalkStatus, error,
) {
	return ast.WalkContinue, nil
}

func (r *Renderer) document(_ util.BufWriter, _ []byte, _ ast.Node, entering bool) (
	ast.WalkStatus, error,
) {
	if entering {
		// Defensive: clear any leftover state in case the Renderer instance is
		// ever reused across conversions.
		r.listIndentStack = nil
	}
	return ast.WalkContinue, nil
}
