package codec

import (
	"strconv"
	"strings"
)

// xpath is the location of a value inside a payload, kept as a linked list
// and rendered to a string only when an error is reported ("a.b[2].c",
// "a.b@attr"). Building the string eagerly at every nesting level costs
// O(depth^2) memory, which a hostile deeply nested payload turns into
// gigabytes. A nil *xpath is the root.
type xpath struct {
	parent *xpath
	name   string
	index  int
	kind   xpathKind
}

type xpathKind uint8

const (
	xpField xpathKind = iota // ".name" (no dot at the root)
	xpAttr                   // "@name"
	xpIndex                  // "[i]"
)

func (p *xpath) field(name string) *xpath { return &xpath{parent: p, name: name, kind: xpField} }
func (p *xpath) attr(name string) *xpath  { return &xpath{parent: p, name: name, kind: xpAttr} }
func (p *xpath) at(i int) *xpath          { return &xpath{parent: p, index: i, kind: xpIndex} }

// String renders the path; the root renders as "".
func (p *xpath) String() string {
	if p == nil {
		return ""
	}
	var nodes []*xpath
	for n := p; n != nil; n = n.parent {
		nodes = append(nodes, n)
	}
	var b strings.Builder
	for i := len(nodes) - 1; i >= 0; i-- {
		n := nodes[i]
		switch n.kind {
		case xpField:
			if b.Len() > 0 {
				b.WriteByte('.')
			}
			b.WriteString(n.name)
		case xpAttr:
			b.WriteByte('@')
			b.WriteString(n.name)
		case xpIndex:
			b.WriteByte('[')
			b.WriteString(strconv.Itoa(n.index))
			b.WriteByte(']')
		}
	}
	return b.String()
}

// leaf renders the path extended with a field name (joinPath for error
// sites that do not need a child node).
func (p *xpath) leaf(name string) string { return p.field(name).String() }

// attrLeaf renders the path extended with an attribute.
func (p *xpath) attrLeaf(name string) string { return p.attr(name).String() }

// or renders the path, or name when the path is the root.
func (p *xpath) or(name string) string {
	if s := p.String(); s != "" {
		return s
	}
	return name
}
