// Copyright 2015-2019 Brett Vickers.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package etree provides XML services through an Element Tree
// abstraction.
package etree

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
)

const (
	// NoIndent is used with the Document Indent function to disable all
	// indenting.
	NoIndent = -1
)

// ErrXML is returned when XML parsing fails due to incorrect formatting.
var ErrXML = errors.New("etree: invalid XML format")

// ReadSettings determine the default behavior of the Document's ReadFrom*
// methods.
type ReadSettings struct {
	// CharsetReader to be passed to standard xml.Decoder. Default: nil.
	CharsetReader func(charset string, input io.Reader) (io.Reader, error)

	// Permissive allows input containing common mistakes such as missing tags
	// or attribute values. Default: false.
	Permissive bool

	// Entity to be passed to standard xml.Decoder. Default: nil.
	Entity map[string]string
}

// newReadSettings creates a default ReadSettings record.
func newReadSettings() ReadSettings {
	return ReadSettings{
		CharsetReader: func(label string, input io.Reader) (io.Reader, error) {
			return input, nil
		},
		Permissive: false,
	}
}

// dup creates a duplicate of the ReadSettings object.
func (s *ReadSettings) dup() ReadSettings {
	var entityCopy map[string]string
	if s.Entity != nil {
		entityCopy = make(map[string]string)
		for k, v := range s.Entity {
			entityCopy[k] = v
		}
	}
	return ReadSettings{
		CharsetReader: s.CharsetReader,
		Permissive:    s.Permissive,
		Entity:        entityCopy,
	}
}

// WriteSettings determine the behavior of the Document's WriteTo* and
// Indent* methods.
type WriteSettings struct {
	// CanonicalEndTags forces the production of XML end tags, even for
	// elements that have no child elements. Default: false.
	CanonicalEndTags bool

	// CanonicalText forces the production of XML character references for
	// text data characters &, <, and >. If false, XML character references
	// are also produced for " and '. Default: false.
	CanonicalText bool

	// CanonicalAttrVal forces the production of XML character references for
	// attribute value characters &, < and ". If false, XML character
	// references are also produced for > and '. Default: false.
	CanonicalAttrVal bool

	// UseCRLF causes the document's indentation methods to use a carriage
	// return followed by a linefeed ("\r\n") when outputting a newline. If
	// false, only a linefeed is used ("\n"). Default: false.
	UseCRLF bool
}

// XMLWriter is a Writer that also has convenience methods for writing
// strings an single bytes.
type XMLWriter interface {
	io.StringWriter
	io.ByteWriter
	io.Writer
}

// newWriteSettings creates a default WriteSettings record.
func newWriteSettings() WriteSettings {
	return WriteSettings{
		CanonicalEndTags: false,
		CanonicalText:    false,
		CanonicalAttrVal: false,
		UseCRLF:          false,
	}
}

// dup creates a dulicate of the WriteSettings object.
func (s *WriteSettings) dup() WriteSettings {
	return *s
}

// A Token is an interface type used to represent XML elements, character
// data, CDATA sections, XML comments, XML directives, and XML processing
// instructions.
type Token interface {
	Parent() *Element
	Index() int
	WriteTo(w XMLWriter, s *WriteSettings)
	dup(parent *Element) Token
	setParent(parent *Element)
	setIndex(index int)
}

// A Document is a container holding a complete XML tree.
//
// A document has a single embedded element, which contains zero or more child
// tokens, one of which is usually the root element. The embedded element may
// include other children such as processing instruction tokens or character
// data tokens. The document's embedded element is never directly serialized;
// only its children are.
//
// A document also contains read and write settings, which influence the way
// the document is deserialized, serialized, and indented.
type Document struct {
	Element
	ReadSettings  ReadSettings
	WriteSettings WriteSettings
}

// An Element represents an XML element, its attributes, and its child tokens.
type Element struct {
	Space, Tag string   // namespace prefix and tag
	Attr       []Attr   // key-value attribute pairs
	Child      []Token  // child tokens (elements, comments, etc.)
	parent     *Element // parent element
	index      int      // token index in parent's children
}

// An Attr represents a key-value attribute within an XML element.
type Attr struct {
	Space, Key string   // The attribute's namespace prefix and key
	Value      string   // The attribute value string
	element    *Element // element containing the attribute
}

// charDataFlags are used with CharData tokens to store additional settings.
type charDataFlags uint8

const (
	// The CharData contains only whitespace.
	whitespaceFlag charDataFlags = 1 << iota

	// The CharData contains a CDATA section.
	cdataFlag
)

// CharData may be used to represent simple text data or a CDATA section
// within an XML document. The Data property should never be modified
// directly; use the SetData method instead.
type CharData struct {
	Data   string // the simple text or CDATA section content
	parent *Element
	index  int
	flags  charDataFlags
}

// A Comment represents an XML comment.
type Comment struct {
	Data   string // the comment's text
	parent *Element
	index  int
}

// A Directive represents an XML directive.
type Directive struct {
	Data   string // the directive string
	parent *Element
	index  int
}

// A ProcInst represents an XML processing instruction.
type ProcInst struct {
	Target string // the processing instruction target
	Inst   string // the processing instruction value
	parent *Element
	index  int
}

// NewDocument creates an XML document without a root element.
func NewDocument() *Document {
	return &Document{
		Element:       Element{Child: make([]Token, 0)},
		ReadSettings:  newReadSettings(),
		WriteSettings: newWriteSettings(),
	}
}

// NewDocumentWithRoot creates an XML document and sets the element 'e' as its
// root element. If the element 'e' is already part of another document, it is
// first removed from its existing document.
func NewDocumentWithRoot(e *Element) *Document {
	d := NewDocument()
	d.SetRoot(e)
	return d
}

// Copy returns a recursive, deep copy of the document.
func (d *Document) Copy() *Document {
	return &Document{
		Element:       *(d.Element.dup(nil).(*Element)),
		ReadSettings:  d.ReadSettings.dup(),
		WriteSettings: d.WriteSettings.dup(),
	}
}

// Root returns the root element of the document. It returns nil if there is
// no root element.
func (d *Document) Root() *Element {
	for _, t := range d.Child {
		if c, ok := t.(*Element); ok {
			return c
		}
	}
	return nil
}

// SetRoot replaces the document's root element with the element 'e'. If the
// document already has a root element when this function is called, then the
// existing root element is unbound from the document. If the element 'e' is
// part of another document, then it is unbound from the other document.
func (d *Document) SetRoot(e *Element) {
	if e.parent != nil {
		e.parent.RemoveChild(e)
	}

	// If there is already a root element, replace it.
	p := &d.Element
	for i, t := range p.Child {
		if _, ok := t.(*Element); ok {
			t.setParent(nil)
			t.setIndex(-1)
			p.Child[i] = e
			e.setParent(p)
			e.setIndex(i)
			return
		}
	}

	// No existing root element, so add it.
	p.addChild(e)
}

// ReadFrom reads XML from the reader 'r' into this document. The function
// returns the number of bytes read and any error encountered.
func (d *Document) ReadFrom(r io.Reader) (n int64, err error) {
	return d.Element.readFrom(r, d.ReadSettings)
}

// ReadFromFile reads XML from a local file at path 'filepath' into this
// document.
func (d *Document) ReadFromFile(filepath string) error {
	f, err := os.Open(filepath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = d.ReadFrom(f)
	return err
}

// ReadFromBytes reads XML from the byte slice 'b' into the this document.
func (d *Document) ReadFromBytes(b []byte) error {
	_, err := d.ReadFrom(bytes.NewReader(b))
	return err
}

// ReadFromString reads XML from the string 's' into this document.
func (d *Document) ReadFromString(s string) error {
	_, err := d.ReadFrom(strings.NewReader(s))
	return err
}

// WriteTo serializes the document out to the writer 'w'. The function returns
// the number of bytes written and any error encountered.
func (d *Document) WriteTo(w io.Writer) (n int64, err error) {
	cw := newCountWriter(w)
	b := bufio.NewWriter(cw)
	for _, c := range d.Child {
		c.WriteTo(b, &d.WriteSettings)
	}
	err, n = b.Flush(), cw.bytes
	return
}

// WriteToFile serializes the document out to the file at path 'filepath'.
func (d *Document) WriteToFile(filepath string) error {
	f, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = d.WriteTo(f)
	return err
}

// WriteToBytes serializes this document into a slice of bytes.
func (d *Document) WriteToBytes() (b []byte, err error) {
	var buf bytes.Buffer
	if _, err = d.WriteTo(&buf); err != nil {
		return
	}
	return buf.Bytes(), nil
}

// WriteToString serializes this document into a string.
func (d *Document) WriteToString() (s string, err error) {
	var b []byte
	if b, err = d.WriteToBytes(); err != nil {
		return
	}
	return string(b), nil
}

type indentFunc func(depth int) string

// Indent modifies the document's element tree by inserting character data
// tokens containing newlines and indentation. The amount of indentation per
// depth level is given by the 'spaces' parameter. Pass etree.NoIndent for
// 'spaces' if you want no indentation at all.
func (d *Document) Indent(spaces int) {
	var indent indentFunc
	switch {
	case spaces < 0:
		indent = func(depth int) string { return "" }
	case d.WriteSettings.UseCRLF:
		indent = func(depth int) string { return indentCRLF(depth*spaces, indentSpaces) }
	default:
		indent = func(depth int) string { return indentLF(depth*spaces, indentSpaces) }
	}
	d.Element.indent(0, indent)
}

// IndentTabs modifies the document's element tree by inserting CharData
// tokens containing newlines and tabs for indentation.  One tab is used per
// indentation level.
func (d *Document) IndentTabs() {
	var indent indentFunc
	switch d.WriteSettings.UseCRLF {
	case true:
		indent = func(depth int) string { return indentCRLF(depth, indentTabs) }
	default:
		indent = func(depth int) string { return indentLF(depth, indentTabs) }
	}
	d.Element.indent(0, indent)
}

// NewElement creates an unparented element with the specified tag (i.e.,
// name). The tag may include a namespace prefix followed by a colon.
func NewElement(tag string) *Element {
	space, stag := spaceDecompose(tag)
	return newElement(space, stag, nil)
}

// newElement is a helper function that creates an element and binds it to
// a parent element if possible.
func newElement(space, tag string, parent *Element) *Element {
	e := &Element{
		Space:  space,
		Tag:    tag,
		Attr:   make([]Attr, 0),
		Child:  make([]Token, 0),
		parent: parent,
		index:  -1,
	}
	if parent != nil {
		parent.addChild(e)
	}
	return e
}

// Copy creates a recursive, deep copy of the element and all its attributes
// and children. The returned element has no parent but can be parented to a
// another element using AddChild, or added to a document with SetRoot or
// NewDocumentWithRoot.
func (e *Element) Copy() *Element {
	return e.dup(nil).(*Element)
}

// FullTag returns the element e's complete tag, including namespace prefix if
// present.
func (e *Element) FullTag() string {
	if e.Space == "" {
		return e.Tag
	}
	return e.Space + ":" + e.Tag
}

// NamespaceURI returns the XML namespace URI associated with the element. If
// the element is part of the XML default namespace, NamespaceURI returns the
// empty string.
func (e *Element) NamespaceURI() string {
	if e.Space == "" {
		return e.findDefaultNamespaceURI()
	}
	return e.findLocalNamespaceURI(e.Space)
}

// findLocalNamespaceURI finds the namespace URI corresponding to the
// requested prefix.
func (e *Element) findLocalNamespaceURI(prefix string) string {
	for _, a := range e.Attr {
		if a.Space == "xmlns" && a.Key == prefix {
			return a.Value
		}
	}

	if e.parent == nil {
		return ""
	}

	return e.parent.findLocalNamespaceURI(prefix)
}

// findDefaultNamespaceURI finds the default namespace URI of the element.
func (e *Element) findDefaultNamespaceURI() string {
	for _, a := range e.Attr {
		if a.Space == "" && a.Key == "xmlns" {
			return a.Value
		}
	}

	if e.parent == nil {
		return ""
	}

	return e.parent.findDefaultNamespaceURI()
}

// namespacePrefix returns the namespace prefix associated with the element.
func (e *Element) namespacePrefix() string {
	return e.Space
}

// name returns the tag associated with the element.
func (e *Element) name() string {
	return e.Tag
}

// Text returns all character data immediately following the element's opening
// tag.
func (e *Element) Text() string {
	if len(e.Child) == 0 {
		return ""
	}

	text := ""
	for _, ch := range e.Child {
		if cd, ok := ch.(*CharData); ok {
			if text == "" {
				text = cd.Data
			} else {
				text += cd.Data
			}
		} else {
			break
		}
	}
	return text
}

// SetText replaces all character data immediately following an element's
// opening tag with the requested string.
func (e *Element) SetText(text string) {
	e.replaceText(0, text, 0)
}

// SetCData replaces all character data immediately following an element's
// opening tag with a CDATA section.
func (e *Element) SetCData(text string) {
	e.replaceText(0, text, cdataFlag)
}

// Tail returns all character data immediately following the element's end
// tag.
func (e *Element) Tail() string {
	if e.Parent() == nil {
		return ""
	}

	p := e.Parent()
	i := e.Index()

	text := ""
	for _, ch := range p.Child[i+1:] {
		if cd, ok := ch.(*CharData); ok {
			if text == "" {
				text = cd.Data
			} else {
				text += cd.Data
			}
		} else {
			break
		}
	}
	return text
}

// SetTail replaces all character data immediately following the element's end
// tag with the requested string.
func (e *Element) SetTail(text string) {
	if e.Parent() == nil {
		return
	}

	p := e.Parent()
	p.replaceText(e.Index()+1, text, 0)
}

// replaceText is a helper function that replaces a series of chardata tokens
// starting at index i with the requested text.
func (e *Element) replaceText(i int, text string, flags charDataFlags) {
	end := e.findTermCharDataIndex(i)

	switch {
	case end == i:
		if text != "" {
			// insert a new chardata token at index i
			cd := newCharData(text, flags, nil)
			e.InsertChildAt(i, cd)
		}

	case end == i+1:
		if text == "" {
			// remove the chardata token at index i
			e.RemoveChildAt(i)
		} else {
			// replace the first and only character token at index i
			cd := e.Child[i].(*CharData)
			cd.Data, cd.flags = text, flags
		}

	default:
		if text == "" {
			// remove all chardata tokens starting from index i
			copy(e.Child[i:], e.Child[end:])
			removed := end - i
			e.Child = e.Child[:len(e.Child)-removed]
			for j := i; j < len(e.Child); j++ {
				e.Child[j].setIndex(j)
			}
		} else {
			// replace the first chardata token at index i and remove all
			// subsequent chardata tokens
			cd := e.Child[i].(*CharData)
			cd.Data, cd.flags = text, flags
			copy(e.Child[i+1:], e.Child[end:])
			removed := end - (i + 1)
			e.Child = e.Child[:len(e.Child)-removed]
			for j := i + 1; j < len(e.Child); j++ {
				e.Child[j].setIndex(j)
			}
		}
	}
}

// findTermCharDataIndex finds the index of the first child token that isn't
// a CharData token. It starts from the requested start index.
func (e *Element) findTermCharDataIndex(start int) int {
	for i := start; i < len(e.Child); i++ {
		if _, ok := e.Child[i].(*CharData); !ok {
			return i
		}
	}
	return len(e.Child)
}

// CreateElement creates a new element with the specified tag (i.e., name) and
// adds it as the last child token of this element. The tag may include a
// prefix followed by a colon.
func (e *Element) CreateElement(tag string) *Element {
	space, stag := spaceDecompose(tag)
	return newElement(space, stag, e)
}

// AddChild adds the token 't' as the last child of the element. If token 't'
// was already the child of another element, it is first removed from its
// parent element.
func (e *Element) AddChild(t Token) {
	if t.Parent() != nil {
		t.Parent().RemoveChild(t)
	}
	e.addChild(t)
}

// InsertChild inserts the token 't' into this element's list of children just
// before the element's existing child token 'ex'. If the existing element
// 'ex' does not appear in this element's list of child tokens, then 't' is
// added to the end of this element's list of child tokens. If token 't' is
// already the child of another element, it is first removed from the other
// element's list of child tokens.
//
// Deprecated: InsertChild is deprecated. Use InsertChildAt instead.
func (e *Element) InsertChild(ex Token, t Token) {
	if ex == nil || ex.Parent() != e {
		e.AddChild(t)
		return
	}

	if t.Parent() != nil {
		t.Parent().RemoveChild(t)
	}

	t.setParent(e)

	i := ex.Index()
	e.Child = append(e.Child, nil)
	copy(e.Child[i+1:], e.Child[i:])
	e.Child[i] = t

	for j := i; j < len(e.Child); j++ {
		e.Child[j].setIndex(j)
	}
}

// InsertChildAt inserts the token 't' into this element's list of child
// tokens just before the requested 'index'. If the index is greater than or
// equal to the length of the list of child tokens, then the token 't' is
// added to the end of the list of child tokens.
func (e *Element) InsertChildAt(index int, t Token) {
	if index >= len(e.Child) {
		e.AddChild(t)
		return
	}

	if t.Parent() != nil {
		if t.Parent() == e && t.Index() > index {
			index--
		}
		t.Parent().RemoveChild(t)
	}

	t.setParent(e)

	e.Child = append(e.Child, nil)
	copy(e.Child[index+1:], e.Child[index:])
	e.Child[index] = t

	for j := index; j < len(e.Child); j++ {
		e.Child[j].setIndex(j)
	}
}

// RemoveChild attempts to remove the token 't' from this element's list of
// child tokens. If the token 't' was a child of this element, then it is
// removed and returned. Otherwise, nil is returned.
func (e *Element) RemoveChild(t Token) Token {
	if t.Parent() != e {
		return nil
	}
	return e.RemoveChildAt(t.Index())
}

// RemoveChildAt removes the child token appearing in slot 'index' of this
// element's list of child tokens. The removed child token is then returned.
// If the index is out of bounds, no child is removed and nil is returned.
func (e *Element) RemoveChildAt(index int) Token {
	if index >= len(e.Child) {
		return nil
	}

	t := e.Child[index]
	for j := index + 1; j < len(e.Child); j++ {
		e.Child[j].setIndex(j - 1)
	}
	e.Child = append(e.Child[:index], e.Child[index+1:]...)
	t.setIndex(-1)
	t.setParent(nil)
	return t
}

var cdataSection = []byte("<![CDATA[")

// ReadFrom reads XML from the reader 'ri' and stores the result as a new
// child of this element.
func (e *Element) readFrom(ri io.Reader, settings ReadSettings) (n int64, err error) {
	var (
		offset int64
		buf    bytes.Buffer
	)

	r := newCountReader(ri)

	// Tee decoder reads to a buffer for inspection
	reader := io.TeeReader(r, &buf)

	dec := xml.NewDecoder(reader)
	dec.CharsetReader = settings.CharsetReader
	dec.Strict = !settings.Permissive
	dec.Entity = settings.Entity
	var stack stack
	stack.push(e)
	for {
		t, err := dec.RawToken()
		switch {
		case err == io.EOF:
			if len(stack.data) != 1 {
				return r.bytes, ErrXML
			}

			return r.bytes, nil
		case err != nil:
			return r.bytes, err
		case stack.empty():
			return r.bytes, ErrXML
		}

		top := stack.peek().(*Element)

		switch t := t.(type) {
		case xml.StartElement:
			e := newElement(t.Name.Space, t.Name.Local, top)
			for _, a := range t.Attr {
				e.createAttr(a.Name.Space, a.Name.Local, a.Value, e)
			}
			stack.push(e)
		case xml.EndElement:
			if top.Tag != t.Name.Local || top.Space != t.Name.Space {
				return r.bytes, ErrXML
			}
			stack.pop()
		case xml.CharData:
			data := string(t)

			var flags charDataFlags
			if isWhitespace(data) {
				flags = whitespaceFlag
			}

			peek := buf.Bytes()
			if len(peek) > 9 {
				peek = peek[0:9]
			}

			if bytes.EqualFold(peek, cdataSection) {
				flags = flags | cdataFlag
			}

			newCharData(data, flags, top)
		case xml.Comment:
			newComment(string(t), top)
		case xml.Directive:
			newDirective(string(t), top)
		case xml.ProcInst:
			newProcInst(t.Target, string(t.Inst), top)
		}

		// Calculate the number of read bytes from the last offset.
		read := dec.InputOffset() - offset

		// Advance the buffer so that it's located at the input offset.
		_ = buf.Next(int(read))

		offset = dec.InputOffset()
	}
}

// SelectAttr finds an element attribute matching the requested 'key' and, if
// found, returns a pointer to the matching attribute. The function returns
// nil if no matching attribute is found. The key may include a namespace
// prefix followed by a colon.
func (e *Element) SelectAttr(key string) *Attr {
	space, skey := spaceDecompose(key)
	for i, a := range e.Attr {
		if spaceMatch(space, a.Space) && skey == a.Key {
			return &e.Attr[i]
		}
	}
	return nil
}

// SelectAttrValue finds an element attribute matching the requested 'key' and
// returns its value if found. If no matching attribute is found, the function
// returns the 'dflt' value instead. The key may include a namespace prefix
// followed by a colon.
func (e *Element) SelectAttrValue(key, dflt string) string {
	space, skey := spaceDecompose(key)
	for _, a := range e.Attr {
		if spaceMatch(space, a.Space) && skey == a.Key {
			return a.Value
		}
	}
	return dflt
}

// ChildElements returns all elements that are children of this element.
func (e *Element) ChildElements() []*Element {
	var elements []*Element
	for _, t := range e.Child {
		if c, ok := t.(*Element); ok {
			elements = append(elements, c)
		}
	}
	return elements
}

// SelectElement returns the first child element with the given 'tag' (i.e.,
// name). The function returns nil if no child element matching the tag is
// found. The tag may include a namespace prefix followed by a colon.
func (e *Element) SelectElement(tag string) *Element {
	space, stag := spaceDecompose(tag)
	for _, t := range e.Child {
		if c, ok := t.(*Element); ok && spaceMatch(space, c.Space) && stag == c.Tag {
			return c
		}
	}
	return nil
}

// SelectElements returns a slice of all child elements with the given 'tag'
// (i.e., name). The tag may include a namespace prefix followed by a colon.
func (e *Element) SelectElements(tag string) []*Element {
	space, stag := spaceDecompose(tag)
	var elements []*Element
	for _, t := range e.Child {
		if c, ok := t.(*Element); ok && spaceMatch(space, c.Space) && stag == c.Tag {
			elements = append(elements, c)
		}
	}
	return elements
}

// FindElement returns the first element matched by the XPath-like 'path'
// string. The function returns nil if no child element is found using the
// path. It panics if an invalid path string is supplied.
func (e *Element) FindElement(path string) *Element {
	return e.FindElementPath(MustCompilePath(path))
}

// FindElementPath returns the first element matched by the 'path' object. The
// function returns nil if no element is found using the path.
func (e *Element) FindElementPath(path Path) *Element {
	p := newPather()
	elements := p.traverse(e, path)
	if len(elements) > 0 {
		return elements[0]
	}
	return nil
}

// FindElements returns a slice of elements matched by the XPath-like 'path'
// string. The function returns nil if no child element is found using the
// path. It panics if an invalid path string is supplied.
func (e *Element) FindElements(path string) []*Element {
	return e.FindElementsPath(MustCompilePath(path))
}

// FindElementsPath returns a slice of elements matched by the 'path' object.
func (e *Element) FindElementsPath(path Path) []*Element {
	p := newPather()
	return p.traverse(e, path)
}

// GetPath returns the absolute path of the element. The absolute path is the
// full path from the document's root.
func (e *Element) GetPath() string {
	path := []string{}
	for seg := e; seg != nil; seg = seg.Parent() {
		if seg.Tag != "" {
			path = append(path, seg.Tag)
		}
	}

	// Reverse the path.
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}

	return "/" + strings.Join(path, "/")
}

// GetRelativePath returns the path of this element relative to the 'source'
// element. If the two elements are not part of the same element tree, then
// the function returns the empty string.
func (e *Element) GetRelativePath(source *Element) string {
	var path []*Element

	if source == nil {
		return ""
	}

	// Build a reverse path from the element toward the root. Stop if the
	// source element is encountered.
	var seg *Element
	for seg = e; seg != nil && seg != source; seg = seg.Parent() {
		path = append(path, seg)
	}

	// If we found the source element, reverse the path and compose the
	// string.
	if seg == source {
		if len(path) == 0 {
			return "."
		}
		parts := []string{}
		for i := len(path) - 1; i >= 0; i-- {
			parts = append(parts, path[i].Tag)
		}
		return "./" + strings.Join(parts, "/")
	}

	// The source wasn't encountered, so climb from the source element toward
	// the root of the tree until an element in the reversed path is
	// encountered.

	findPathIndex := func(e *Element, path []*Element) int {
		for i, ee := range path {
			if e == ee {
				return i
			}
		}
		return -1
	}

	climb := 0
	for seg = source; seg != nil; seg = seg.Parent() {
		i := findPathIndex(seg, path)
		if i >= 0 {
			path = path[:i] // truncate at found segment
			break
		}
		climb++
	}

	// No element in the reversed path was encountered, so the two elements
	// must not be part of the same tree.
	if seg == nil {
		return ""
	}

	// Reverse the (possibly truncated) path and prepend ".." segments to
	// climb.
	parts := []string{}
	for i := 0; i < climb; i++ {
		parts = append(parts, "..")
	}
	for i := len(path) - 1; i >= 0; i-- {
		parts = append(parts, path[i].Tag)
	}
	return strings.Join(parts, "/")
}

// indent recursively inserts proper indentation between an XML element's
// child tokens.
func (e *Element) indent(depth int, indent indentFunc) {
	e.stripIndent()
	n := len(e.Child)
	if n == 0 {
		return
	}

	oldChild := e.Child
	e.Child = make([]Token, 0, n*2+1)
	isCharData, firstNonCharData := false, true
	for _, c := range oldChild {
		// Insert NL+indent before child if it's not character data.
		// Exceptions: when it's the first non-character-data child, or when
		// the child is at root depth.
		_, isCharData = c.(*CharData)
		if !isCharData {
			if !firstNonCharData || depth > 0 {
				s := indent(depth)
				if s != "" {
					newCharData(s, whitespaceFlag, e)
				}
			}
			firstNonCharData = false
		}

		e.addChild(c)

		// Recursively process child elements.
		if ce, ok := c.(*Element); ok {
			ce.indent(depth+1, indent)
		}
	}

	// Insert NL+indent before the last child.
	if !isCharData {
		if !firstNonCharData || depth > 0 {
			s := indent(depth - 1)
			if s != "" {
				newCharData(s, whitespaceFlag, e)
			}
		}
	}
}

// stripIndent removes any previously inserted indentation.
func (e *Element) stripIndent() {
	// Count the number of non-indent child tokens
	n := len(e.Child)
	for _, c := range e.Child {
		if cd, ok := c.(*CharData); ok && cd.IsWhitespace() {
			n--
		}
	}
	if n == len(e.Child) {
		return
	}

	// Strip out indent CharData
	newChild := make([]Token, n)
	j := 0
	for _, c := range e.Child {
		if cd, ok := c.(*CharData); ok && cd.IsWhitespace() {
			continue
		}
		newChild[j] = c
		newChild[j].setIndex(j)
		j++
	}
	e.Child = newChild
}

// dup duplicates the element.
func (e *Element) dup(parent *Element) Token {
	ne := &Element{
		Space:  e.Space,
		Tag:    e.Tag,
		Attr:   make([]Attr, len(e.Attr)),
		Child:  make([]Token, len(e.Child)),
		parent: parent,
		index:  e.index,
	}
	for i, t := range e.Child {
		ne.Child[i] = t.dup(ne)
	}
	copy(ne.Attr, e.Attr)
	return ne
}

// Parent returns this element's parent element. It returns nil if this
// element has no parent.
func (e *Element) Parent() *Element {
	return e.parent
}

// Index returns the index of this element within its parent element's
// list of child tokens. If this element has no parent, then the function
// returns -1.
func (e *Element) Index() int {
	return e.index
}

// setParent replaces this element token's parent.
func (e *Element) setParent(parent *Element) {
	e.parent = parent
}

// setIndex sets this element token's index within its parent's Child slice.
func (e *Element) setIndex(index int) {
	e.index = index
}

// WriteTo serializes the element to the writer w.
func (e *Element) WriteTo(w XMLWriter, s *WriteSettings) {
	w.WriteByte('<')
	w.WriteString(e.FullTag())
	for _, a := range e.Attr {
		w.WriteByte(' ')
		a.WriteTo(w, s)
	}
	if len(e.Child) > 0 {
		w.WriteByte('>')
		for _, c := range e.Child {
			c.WriteTo(w, s)
		}
		w.Write([]byte{'<', '/'})
		w.WriteString(e.FullTag())
		w.WriteByte('>')
	} else {
		if s.CanonicalEndTags {
			w.Write([]byte{'>', '<', '/'})
			w.WriteString(e.FullTag())
			w.WriteByte('>')
		} else {
			w.Write([]byte{'/', '>'})
		}
	}
}

// addChild adds a child token to the element e.
func (e *Element) addChild(t Token) {
	t.setParent(e)
	t.setIndex(len(e.Child))
	e.Child = append(e.Child, t)
}

// CreateAttr creates an attribute with the specified 'key' and 'value' and
// adds it to this element. If an attribute with same key already exists on
// this element, then its value is replaced. The key may include a namespace
// prefix followed by a colon.
func (e *Element) CreateAttr(key, value string) *Attr {
	space, skey := spaceDecompose(key)
	return e.createAttr(space, skey, value, e)
}

// createAttr is a helper function that creates attributes.
func (e *Element) createAttr(space, key, value string, parent *Element) *Attr {
	for i, a := range e.Attr {
		if space == a.Space && key == a.Key {
			e.Attr[i].Value = value
			return &e.Attr[i]
		}
	}
	a := Attr{
		Space:   space,
		Key:     key,
		Value:   value,
		element: parent,
	}
	e.Attr = append(e.Attr, a)
	return &e.Attr[len(e.Attr)-1]
}

// RemoveAttr removes the first attribute of this element whose key matches
// 'key'. It returns a copy of the removed attribute if a match is found. If
// no match is found, it returns nil. The key may include a namespace prefix
// followed by a colon.
func (e *Element) RemoveAttr(key string) *Attr {
	space, skey := spaceDecompose(key)
	for i, a := range e.Attr {
		if space == a.Space && skey == a.Key {
			e.Attr = append(e.Attr[0:i], e.Attr[i+1:]...)
			return &Attr{
				Space:   a.Space,
				Key:     a.Key,
				Value:   a.Value,
				element: nil,
			}
		}
	}
	return nil
}

// SortAttrs sorts this element's attributes lexicographically by key.
func (e *Element) SortAttrs() {
	sort.Sort(byAttr(e.Attr))
}

type byAttr []Attr

func (a byAttr) Len() int {
	return len(a)
}

func (a byAttr) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}

func (a byAttr) Less(i, j int) bool {
	sp := strings.Compare(a[i].Space, a[j].Space)
	if sp == 0 {
		return strings.Compare(a[i].Key, a[j].Key) < 0
	}
	return sp < 0
}

// FullKey returns this attribute's complete key, including namespace prefix
// if present.
func (a *Attr) FullKey() string {
	if a.Space == "" {
		return a.Key
	}
	return a.Space + ":" + a.Key
}

// Element returns a pointer to the element containing this attribute.
func (a *Attr) Element() *Element {
	return a.element
}

// NamespaceURI returns the XML namespace URI associated with this attribute.
// The function returns the empty string if the attribute is unprefixed or
// if the attribute is part of the XML default namespace.
func (a *Attr) NamespaceURI() string {
	if a.Space == "" {
		return ""
	}
	return a.element.findLocalNamespaceURI(a.Space)
}

// WriteTo serializes the attribute to the writer.
func (a *Attr) WriteTo(w XMLWriter, s *WriteSettings) {
	w.WriteString(a.FullKey())
	w.WriteString(`="`)
	var m escapeMode
	if s.CanonicalAttrVal {
		m = escapeCanonicalAttr
	} else {
		m = escapeNormal
	}
	escapeString(w, a.Value, m)
	w.WriteByte('"')
}

// NewText creates an unparented CharData token containing simple text data.
func NewText(text string) *CharData {
	return newCharData(text, 0, nil)
}

// NewCData creates an unparented XML character CDATA section with 'data' as
// its content.
func NewCData(data string) *CharData {
	return newCharData(data, cdataFlag, nil)
}

// NewCharData creates an unparented CharData token containing simple text
// data.
//
// Deprecated: NewCharData is deprecated. Instead, use NewText, which does the
// same thing.
func NewCharData(data string) *CharData {
	return newCharData(data, 0, nil)
}

// newCharData creates a character data token and binds it to a parent
// element. If parent is nil, the CharData token remains unbound.
func newCharData(data string, flags charDataFlags, parent *Element) *CharData {
	c := &CharData{
		Data:   data,
		parent: nil,
		index:  -1,
		flags:  flags,
	}
	if parent != nil {
		parent.addChild(c)
	}
	return c
}

// CreateText creates a CharData token simple text data and adds it to the
// end of this element's list of child tokens.
func (e *Element) CreateText(text string) *CharData {
	return newCharData(text, 0, e)
}

// CreateCData creates a CharData token containing a CDATA section with 'data'
// as its content and adds it to the end of this element's list of child
// tokens.
func (e *Element) CreateCData(data string) *CharData {
	return newCharData(data, cdataFlag, e)
}

// CreateCharData creates a CharData token simple text data and adds it to the
// end of this element's list of child tokens.
//
// Deprecated: CreateCharData is deprecated. Instead, use CreateText, which
// does the same thing.
func (e *Element) CreateCharData(data string) *CharData {
	return newCharData(data, 0, e)
}

// SetData modifies the content of the CharData token. In the case of a
// CharData token containing simple text, the simple text is modified. In the
// case of a CharData token containing a CDATA section, the CDATA section's
// content is modified.
func (c *CharData) SetData(text string) {
	c.Data = text
	if isWhitespace(text) {
		c.flags |= whitespaceFlag
	} else {
		c.flags &= ^whitespaceFlag
	}
}

// IsCData returns true if this CharData token is contains a CDATA section. It
// returns false if the CharData token contains simple text.
func (c *CharData) IsCData() bool {
	return (c.flags & cdataFlag) != 0
}

// IsWhitespace returns true if this CharData token contains only whitespace.
func (c *CharData) IsWhitespace() bool {
	return (c.flags & whitespaceFlag) != 0
}

// Parent returns this CharData token's parent element, or nil if it has no
// parent.
func (c *CharData) Parent() *Element {
	return c.parent
}

// Index returns the index of this CharData token within its parent element's
// list of child tokens. If this CharData token has no parent, then the
// function returns -1.
func (c *CharData) Index() int {
	return c.index
}

// dup duplicates the character data.
func (c *CharData) dup(parent *Element) Token {
	return &CharData{
		Data:   c.Data,
		flags:  c.flags,
		parent: parent,
		index:  c.index,
	}
}

// setParent replaces the character data token's parent.
func (c *CharData) setParent(parent *Element) {
	c.parent = parent
}

// setIndex sets the CharData token's index within its parent element's Child
// slice.
func (c *CharData) setIndex(index int) {
	c.index = index
}

// WriteTo serializes character data to the writer.
func (c *CharData) WriteTo(w XMLWriter, s *WriteSettings) {
	if c.IsCData() {
		w.WriteString(`<![CDATA[`)
		w.WriteString(c.Data)
		w.WriteString(`]]>`)
	} else {
		var m escapeMode
		if s.CanonicalText {
			m = escapeCanonicalText
		} else {
			m = escapeNormal
		}
		escapeString(w, c.Data, m)
	}
}

// NewComment creates an unparented comment token.
func NewComment(comment string) *Comment {
	return newComment(comment, nil)
}

// NewComment creates a comment token and sets its parent element to 'parent'.
func newComment(comment string, parent *Element) *Comment {
	c := &Comment{
		Data:   comment,
		parent: nil,
		index:  -1,
	}
	if parent != nil {
		parent.addChild(c)
	}
	return c
}

// CreateComment creates a comment token using the specified 'comment' string
// and adds it as the last child token of this element.
func (e *Element) CreateComment(comment string) *Comment {
	return newComment(comment, e)
}

// dup duplicates the comment.
func (c *Comment) dup(parent *Element) Token {
	return &Comment{
		Data:   c.Data,
		parent: parent,
		index:  c.index,
	}
}

// Parent returns comment token's parent element, or nil if it has no parent.
func (c *Comment) Parent() *Element {
	return c.parent
}

// Index returns the index of this Comment token within its parent element's
// list of child tokens. If this Comment token has no parent, then the
// function returns -1.
func (c *Comment) Index() int {
	return c.index
}

// setParent replaces the comment token's parent.
func (c *Comment) setParent(parent *Element) {
	c.parent = parent
}

// setIndex sets the Comment token's index within its parent element's Child
// slice.
func (c *Comment) setIndex(index int) {
	c.index = index
}

// WriteTo serialies the comment to the writer.
func (c *Comment) WriteTo(w XMLWriter, s *WriteSettings) {
	w.WriteString("<!--")
	w.WriteString(c.Data)
	w.WriteString("-->")
}

// NewDirective creates an unparented XML directive token.
func NewDirective(data string) *Directive {
	return newDirective(data, nil)
}

// newDirective creates an XML directive and binds it to a parent element. If
// parent is nil, the Directive remains unbound.
func newDirective(data string, parent *Element) *Directive {
	d := &Directive{
		Data:   data,
		parent: nil,
		index:  -1,
	}
	if parent != nil {
		parent.addChild(d)
	}
	return d
}

// CreateDirective creates an XML directive token with the specified 'data'
// value and adds it as the last child token of this element.
func (e *Element) CreateDirective(data string) *Directive {
	return newDirective(data, e)
}

// dup duplicates the directive.
func (d *Directive) dup(parent *Element) Token {
	return &Directive{
		Data:   d.Data,
		parent: parent,
		index:  d.index,
	}
}

// Parent returns directive token's parent element, or nil if it has no
// parent.
func (d *Directive) Parent() *Element {
	return d.parent
}

// Index returns the index of this Directive token within its parent element's
// list of child tokens. If this Directive token has no parent, then the
// function returns -1.
func (d *Directive) Index() int {
	return d.index
}

// setParent replaces the directive token's parent.
func (d *Directive) setParent(parent *Element) {
	d.parent = parent
}

// setIndex sets the Directive token's index within its parent element's Child
// slice.
func (d *Directive) setIndex(index int) {
	d.index = index
}

// WriteTo serializes the XML directive to the writer.
func (d *Directive) WriteTo(w XMLWriter, s *WriteSettings) {
	w.WriteString("<!")
	w.WriteString(d.Data)
	w.WriteString(">")
}

// NewProcInst creates an unparented XML processing instruction.
func NewProcInst(target, inst string) *ProcInst {
	return newProcInst(target, inst, nil)
}

// newProcInst creates an XML processing instruction and binds it to a parent
// element. If parent is nil, the ProcInst remains unbound.
func newProcInst(target, inst string, parent *Element) *ProcInst {
	p := &ProcInst{
		Target: target,
		Inst:   inst,
		parent: nil,
		index:  -1,
	}
	if parent != nil {
		parent.addChild(p)
	}
	return p
}

// CreateProcInst creates an XML processing instruction token with the
// sepcified 'target' and instruction 'inst'. It is then added as the last
// child token of this element.
func (e *Element) CreateProcInst(target, inst string) *ProcInst {
	return newProcInst(target, inst, e)
}

// dup duplicates the procinst.
func (p *ProcInst) dup(parent *Element) Token {
	return &ProcInst{
		Target: p.Target,
		Inst:   p.Inst,
		parent: parent,
		index:  p.index,
	}
}

// Parent returns processing instruction token's parent element, or nil if it
// has no parent.
func (p *ProcInst) Parent() *Element {
	return p.parent
}

// Index returns the index of this ProcInst token within its parent element's
// list of child tokens. If this ProcInst token has no parent, then the
// function returns -1.
func (p *ProcInst) Index() int {
	return p.index
}

// setParent replaces the processing instruction token's parent.
func (p *ProcInst) setParent(parent *Element) {
	p.parent = parent
}

// setIndex sets the processing instruction token's index within its parent
// element's Child slice.
func (p *ProcInst) setIndex(index int) {
	p.index = index
}

// WriteTo serializes the processing instruction to the writer.
func (p *ProcInst) WriteTo(w XMLWriter, s *WriteSettings) {
	w.WriteString("<?")
	w.WriteString(p.Target)
	if p.Inst != "" {
		w.WriteByte(' ')
		w.WriteString(p.Inst)
	}
	w.WriteString("?>")
}
