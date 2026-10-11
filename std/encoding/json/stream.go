package json

import (
	libjson "github.com/eugenioenko/goalchemy/lib/json"
	"github.com/eugenioenko/goalchemy/std/bytes"
	"github.com/eugenioenko/goalchemy/std/errors"
	"github.com/eugenioenko/goalchemy/std/io"
	"github.com/eugenioenko/goalchemy/std/strconv"
)

// A Decoder reads and decodes JSON values from an input stream, with the
// buffering, tokens and errors of the reference toolchain's
// encoding/json.Decoder, which is built on encoding/json/jsontext.
type Decoder struct {
	r          io.Reader
	buf        []byte
	prevStart  int
	prevEnd    int
	baseOffset int64
	peekPos    int
	peekErr    error
	stack      []int64
	last       int64

	err             error
	hadPeeked       bool
	hadEOF          bool
	useNumber       bool
	disallowUnknown bool
}

// NewDecoder returns a new decoder that reads from r.
//
// The decoder introduces its own buffering and may read data from r beyond
// the JSON values requested.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: r}
}

// UseNumber causes the Decoder to unmarshal a number into an interface value
// as a Number instead of as a float64.
func (dec *Decoder) UseNumber() { dec.useNumber = true }

// DisallowUnknownFields causes the Decoder to return an error when the
// destination is a struct and the input contains object keys which do not
// match any non-ignored, exported fields in the destination.
func (dec *Decoder) DisallowUnknownFields() { dec.disallowUnknown = true }

// Decode reads the next JSON-encoded value from its input and stores it in
// the value pointed to by v.
func (dec *Decoder) Decode(v any) error {
	data, err := dec.nextValue()
	if err != nil {
		return err
	}
	return nativeUnmarshalResult(libjson.UnmarshalStream(data, v, dec.useNumber, dec.disallowUnknown, numberValue))
}

func numberValue(s string) any { return Number(s) }

func (dec *Decoder) state(data []byte) *decodeState {
	return &decodeState{data: data, useNumber: dec.useNumber, disallowUnknown: dec.disallowUnknown, checked: true}
}

func decoderTyped(dec *Decoder, p any, t *typeInfo, isNil bool, root string) error {
	data, err := dec.nextValue()
	if err != nil {
		return err
	}
	d := dec.state(data)
	d.root = root
	return d.typed(p, t, isNil)
}

func decoderAny(dec *Decoder, v any) error {
	data, err := dec.nextValue()
	if err != nil {
		return err
	}
	return dec.state(data).dynamic(v)
}

// dynamicUnmarshalStream stands in for lib/json.UnmarshalStream when the
// program is compiled: values reach Decode through an interface only.
func dynamicUnmarshalStream(data []byte, v any, useNumber, disallowUnknown bool, number func(string) any) (int, string, string, int64, string, string, string, error) {
	d := &decodeState{data: data, useNumber: useNumber, disallowUnknown: disallowUnknown, checked: true}
	return nativeError, "", "", 0, "", "", "", d.dynamic(v)
}

func (dec *Decoder) nextValue() ([]byte, error) {
	if dec.err != nil {
		return nil, dec.err
	}
	b, err := dec.readValue()
	if err != nil {
		dec.err = streamError(err)
		if dec.err.Error() == unexpectedEnd {
			dec.err = io.ErrUnexpectedEOF
		}
		return nil, dec.err
	}
	dec.hadPeeked = false
	dec.hadEOF = false
	return b, nil
}

// Buffered returns a reader of the data remaining in the Decoder's buffer.
// The reader is valid until the next call to Decode.
func (dec *Decoder) Buffered() io.Reader {
	return bytes.NewReader(dec.buf[dec.prevEnd:])
}

// A Token holds a value of one of these types: Delim, for the four JSON
// delimiters [ ] { }; bool, for JSON booleans; float64, for JSON numbers;
// Number, for JSON numbers; string, for JSON string literals; nil, for JSON
// null.
type Token any

// A Delim is a JSON array or object delimiter, one of [ ] { or }.
type Delim rune

func (d Delim) String() string {
	return string(d)
}

// Token returns the next JSON token in the input stream. At the end of the
// input stream, Token returns nil, io.EOF.
//
// Token guarantees that the delimiters [ ] { } it returns are properly
// nested and matched: if Token encounters an unexpected delimiter in the
// input, it will return an error.
func (dec *Decoder) Token() (Token, error) {
	if dec.err != nil {
		return nil, dec.err
	}
	k, err := dec.readToken()
	if err != nil {
		if isUnexpectedEnd(err) {
			if onlySeparators(dec.buf[dec.prevEnd:]) {
				dec.hadEOF = true
				return nil, io.EOF
			}
			return nil, io.ErrUnexpectedEOF
		}
		return nil, streamError(err)
	}
	dec.hadPeeked = false
	dec.hadEOF = false
	switch k {
	case 'n':
		return nil, nil
	case 'f':
		return false, nil
	case 't':
		return true, nil
	case '"':
		return unquote(dec.buf[dec.prevStart:dec.prevEnd]), nil
	case '0':
		s := string(dec.buf[dec.prevStart:dec.prevEnd])
		if dec.useNumber {
			return Number(s), nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, &UnmarshalTypeError{Value: "number " + s, Type: "float64", Offset: dec.InputOffset()}
		}
		return f, nil
	}
	return Delim(k), nil
}

// More reports whether there is another element in the current array or
// object being parsed.
func (dec *Decoder) More() bool {
	dec.hadPeeked = true
	k := dec.peekKind()
	if k == 0 {
		if dec.err == nil {
			_, err := dec.readToken()
			if err == nil {
				err = errors.New("json: successful read after failed peek")
			}
			dec.err = streamError(err)
		}
		return dec.err != io.EOF
	}
	return k != ']' && k != '}'
}

// InputOffset returns the input stream byte offset of the current decoder
// position. The offset gives the location of the end of the most recently
// returned token and the beginning of the next token.
func (dec *Decoder) InputOffset() int64 {
	offset := dec.baseOffset + int64(dec.prevEnd)
	if dec.hadPeeked || dec.hadEOF {
		unread := dec.buf[dec.prevEnd:]
		trailing := unread[consumeWhitespace(unread):]
		if len(trailing) > 0 {
			offset += int64(len(unread) - len(trailing))
			if dec.hadEOF && (trailing[0] == ',' || trailing[0] == ':') {
				offset++
			}
		}
	}
	return offset
}

func onlySeparators(b []byte) bool {
	for _, c := range b {
		if !isSpace(c) && c != ',' && c != ':' {
			return false
		}
	}
	return true
}

const unexpectedEnd = "unexpected end of JSON input"

type ioError struct{ err error }

func (e *ioError) Error() string { return "jsontext: read error: " + e.err.Error() }

var errFetchEOF = errors.New("fetch: EOF")

func streamError(err error) error {
	if e, ok := err.(*ioError); ok {
		return e.err
	}
	return err
}

func isUnexpectedEnd(err error) bool {
	e, ok := err.(*SyntaxError)
	return ok && e.msg == unexpectedEnd
}

const (
	entryObject = int64(1) << 62
	maxStack    = 10000
)

func (dec *Decoder) depth() int { return len(dec.stack) + 1 }

func (dec *Decoder) lastLength() int64 { return dec.last &^ entryObject }

func (dec *Decoder) lastIsObject() bool { return dec.last&entryObject != 0 }

func (dec *Decoder) needObjectName() bool { return dec.lastIsObject() && dec.lastLength()%2 == 0 }

func (dec *Decoder) needObjectValue() bool { return dec.lastIsObject() && dec.lastLength()%2 == 1 }

func (dec *Decoder) needDelim(next byte) byte {
	switch {
	case dec.needObjectValue():
		return ':'
	case dec.lastLength() > 0 && next != '}' && next != ']' && len(dec.stack) != 0:
		return ','
	}
	return 0
}

var (
	errNonStringName = errors.New("object member name must be a string")
	errMismatchDelim = errors.New("mismatching structural token for object or array")
	errMissingValue  = errors.New("missing value after object name")
	errMaxDepth      = errors.New("exceeded max depth")
)

func (dec *Decoder) appendLiteral() error {
	if dec.needObjectName() {
		return errNonStringName
	}
	dec.last++
	return nil
}

func (dec *Decoder) push(object bool) error {
	switch {
	case dec.needObjectName():
		return errNonStringName
	case len(dec.stack) == maxStack:
		return errMaxDepth
	}
	dec.last++
	dec.stack = append(dec.stack, dec.last)
	dec.last = 0
	if object {
		dec.last = entryObject
	}
	return nil
}

func (dec *Decoder) pop(object bool) error {
	switch {
	case object && !dec.lastIsObject(), !object && (dec.lastIsObject() || len(dec.stack) == 0):
		return errMismatchDelim
	case object && dec.needObjectValue():
		return errMissingValue
	}
	dec.last = dec.stack[len(dec.stack)-1]
	dec.stack = dec.stack[:len(dec.stack)-1]
	return nil
}

func kindOf(c byte) byte {
	switch {
	case c == 'n' || c == 'f' || c == 't' || c == '"' || c == '{' || c == '}' || c == '[' || c == ']':
		return c
	case c == '-' || '0' <= c && c <= '9':
		return '0'
	}
	return 0
}

func (dec *Decoder) offsetAt(pos int) int64 { return dec.baseOffset + int64(pos) }

// syntaxError reports err at pos the way encoding/json does: invalid text
// is located after the text and structural errors after their first byte.
func (dec *Decoder) syntaxError(err error, pos int) error {
	switch err {
	case io.EOF:
		return err
	case errFetchEOF:
		return &SyntaxError{unexpectedEnd, dec.offsetAt(pos)}
	case errMismatchDelim:
		where := "at start of value"
		if len(dec.stack) > 0 && dec.lastLength() > 0 {
			if dec.lastIsObject() {
				where = "after object value (expecting ',' or '}')"
			} else {
				where = "after array element (expecting ',' or ']')"
			}
		}
		return dec.faultError(invalidCharacter(dec.buf[pos:], where), pos)
	}
	if _, ok := err.(*ioError); ok {
		return err
	}
	return &SyntaxError{legacyMessage(err.Error()), dec.offsetAt(pos) + 1}
}

func (dec *Decoder) faultError(f *fault, pos int) error {
	if f == eofFault {
		return &SyntaxError{unexpectedEnd, dec.offsetAt(pos)}
	}
	e := f.syntaxError(pos)
	e.Offset += dec.baseOffset
	return e
}

func (dec *Decoder) fetch() error {
	if cap(dec.buf) == 0 {
		dec.buf = make([]byte, 0, 64)
	}
	const maxBufferSize = 4 << 10
	grow := cap(dec.buf) <= maxBufferSize/2 && int64(cap(dec.buf)) < (dec.baseOffset+int64(dec.prevEnd))/2
	grow = grow || dec.prevStart == 0 && len(dec.buf) >= 3*cap(dec.buf)/4
	if grow {
		buf := make([]byte, 0, cap(dec.buf)*2)
		dec.buf = append(buf, dec.buf[dec.prevStart:]...)
	} else {
		n := copy(dec.buf[:cap(dec.buf)], dec.buf[dec.prevStart:])
		dec.buf = dec.buf[:n]
	}
	dec.baseOffset += int64(dec.prevStart)
	dec.prevEnd -= dec.prevStart
	dec.prevStart = 0
	for {
		n, err := dec.r.Read(dec.buf[len(dec.buf):cap(dec.buf)])
		switch {
		case n > 0:
			dec.buf = dec.buf[:len(dec.buf)+n]
			return nil
		case err == io.EOF:
			return errFetchEOF
		case err != nil:
			return &ioError{err}
		}
	}
}

// refetch reads more input while keeping pos at the same input offset.
func (dec *Decoder) refetch(pos int) (int, error) {
	abs := dec.offsetAt(pos)
	err := dec.fetch()
	return int(abs - dec.baseOffset), err
}

func (dec *Decoder) skipSpace(pos int) (int, error) {
	for {
		pos += consumeWhitespace(dec.buf[pos:])
		if pos < len(dec.buf) {
			return pos, nil
		}
		var err error
		if pos, err = dec.refetch(pos); err != nil {
			return pos, err
		}
	}
}

func (dec *Decoder) invalidatePreviousRead() {
	if dec.prevStart < dec.prevEnd {
		dec.prevStart = dec.prevEnd
	}
}

// start finds the next token, consuming a separating comma or colon, as
// jsontext.Decoder does before reading a token or value.
func (dec *Decoder) start() (int, byte, error) {
	if pos := dec.peekPos; pos != 0 {
		if err := dec.peekErr; err != nil {
			dec.peekPos, dec.peekErr = 0, nil
			return 0, 0, err
		}
		dec.peekPos = 0
		return pos, kindOf(dec.buf[pos]), nil
	}
	dec.invalidatePreviousRead()
	pos, err := dec.skipSpace(dec.prevEnd)
	if err != nil {
		if err == errFetchEOF && dec.depth() == 1 {
			err = io.EOF
		}
		return 0, 0, dec.syntaxError(err, pos)
	}
	var delim byte
	if c := dec.buf[pos]; c == ':' || c == ',' {
		delim = c
		if pos, err = dec.skipSpace(pos + 1); err != nil {
			err = dec.syntaxError(err, pos)
			if dec.needDelim('"') != delim {
				err = dec.delimError(delim, '"')
			}
			return 0, 0, err
		}
	}
	next := kindOf(dec.buf[pos])
	if dec.needDelim(next) != delim {
		return 0, 0, dec.delimError(delim, next)
	}
	return pos, next, nil
}

func (dec *Decoder) delimError(delim, next byte) error {
	where := "at start of value"
	switch dec.needDelim(next) {
	case ':':
		where = "after object name (expecting ':')"
	case ',':
		if dec.lastIsObject() {
			where = "after object value (expecting ',' or '}')"
		} else {
			where = "after array element (expecting ',' or ']')"
		}
	}
	pos := dec.prevEnd + consumeWhitespace(dec.buf[dec.prevEnd:])
	return dec.faultError(invalidCharacter(dec.buf[pos:], where), pos)
}

func (dec *Decoder) peekKind() byte {
	if dec.peekPos > 0 {
		return kindOf(dec.buf[dec.peekPos])
	}
	dec.peekPos = 0
	pos, next, err := dec.start()
	if err != nil {
		dec.peekPos, dec.peekErr = -1, err
		return 0
	}
	dec.peekPos, dec.peekErr = pos, nil
	return next
}

// consume validates the value or, with a literal kind, the literal at pos,
// reading more input until it is complete, and returns its length. Strings,
// objects and arrays are first tracked by an incremental scanner so that a
// value arriving in many reads is validated once it is complete.
func (dec *Decoder) consume(pos int, kind byte) (int, error) {
	var scan *scanner
	scanned := 0
	if kind == '"' || kind == '{' || kind == '[' {
		scan = newScanner()
		scan.depthBase = dec.depth() - 1
	}
	var fetchErr error
	for {
		if fetchErr != nil || scan == nil || dec.scanAhead(scan, pos, &scanned) {
			var n int
			var f *fault
			switch kind {
			case 'n':
				n, f = checkLiteral(dec.buf[pos:], "null")
			case 'f':
				n, f = checkLiteral(dec.buf[pos:], "false")
			case 't':
				n, f = checkLiteral(dec.buf[pos:], "true")
			default:
				n, f = checkValue(dec.buf[pos:], dec.depth(), true)
			}
			if f == nil && !(kind == '0' && pos+n == len(dec.buf)) {
				return n, nil
			}
			if f != nil && f != eofFault {
				if f.where == "at start of value" && (f.what == "}" && !dec.lastIsObject() || f.what == "]" && dec.lastIsObject()) {
					return 0, dec.syntaxError(errMismatchDelim, pos+n)
				}
				return 0, dec.faultError(f, pos+n)
			}
			if fetchErr != nil {
				if f == nil {
					if fetchErr == errFetchEOF {
						return n, nil
					}
					return 0, dec.syntaxError(fetchErr, pos)
				}
				return 0, dec.syntaxError(fetchErr, pos+n)
			}
		}
		pos, fetchErr = dec.refetch(pos)
	}
}

// scanAhead feeds the unscanned bytes of the value at pos to scan and
// reports whether the value is complete or invalid.
func (dec *Decoder) scanAhead(scan *scanner, pos int, scanned *int) bool {
	for ; pos+*scanned < len(dec.buf); *scanned++ {
		if r := scan.step(scan, dec.buf[pos+*scanned]); r == scanError || r == scanEnd {
			return true
		}
	}
	probe := *scan
	return probe.step(&probe, ' ') == scanEnd
}

func (dec *Decoder) readValue() ([]byte, error) {
	pos, next, err := dec.start()
	if err != nil {
		return nil, err
	}
	start := dec.offsetAt(pos)
	n, err := dec.consume(pos, next)
	if err != nil {
		return nil, err
	}
	pos = int(start - dec.baseOffset)
	switch next {
	case 'n', 't', 'f', '0':
		err = dec.appendLiteral()
	case '"':
		dec.last++
	case '{', '[':
		if err = dec.push(next == '{'); err == nil {
			err = dec.pop(next == '{')
		}
	}
	if err != nil {
		return nil, dec.syntaxError(err, pos)
	}
	dec.prevStart, dec.prevEnd = pos, pos+n
	return dec.buf[pos : pos+n], nil
}

func (dec *Decoder) readToken() (byte, error) {
	pos, next, err := dec.start()
	if err != nil {
		return 0, err
	}
	switch next {
	case 'n', 'f', 't', '"', '0':
		start := dec.offsetAt(pos)
		n, err := dec.consume(pos, next)
		if err != nil {
			return 0, err
		}
		pos = int(start - dec.baseOffset)
		if next == '"' {
			dec.last++
		} else if err := dec.appendLiteral(); err != nil {
			return 0, dec.syntaxError(err, pos)
		}
		if next == 'n' || next == 'f' || next == 't' {
			dec.prevStart, dec.prevEnd = pos+n, pos+n
		} else {
			dec.prevStart, dec.prevEnd = pos, pos+n
		}
		return next, nil
	case '{', '[':
		if err := dec.push(next == '{'); err != nil {
			return 0, dec.syntaxError(err, pos)
		}
	case '}', ']':
		if err := dec.pop(next == '}'); err != nil {
			return 0, dec.syntaxError(err, pos)
		}
	default:
		return 0, dec.faultError(invalidCharacter(dec.buf[pos:], "at start of value"), pos)
	}
	dec.prevStart, dec.prevEnd = pos+1, pos+1
	return next, nil
}

// An Encoder writes JSON values to an output stream.
type Encoder struct {
	w            io.Writer
	err          error
	escapeHTML   bool
	indentPrefix string
	indentValue  string
}

// NewEncoder returns a new encoder that writes to w.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w, escapeHTML: true}
}

// Encode writes the JSON encoding of v to the stream, with insignificant
// space characters elided, followed by a newline character, as
// encoding/json.Encoder.Encode does.
func (enc *Encoder) Encode(v any) error {
	if enc.err != nil {
		return enc.err
	}
	return enc.write(nativeResult(libjson.MarshalStream(v, enc.escapeHTML)))
}

func encoderTyped(enc *Encoder, p any, t *typeInfo) error {
	if enc.err != nil {
		return enc.err
	}
	e := &encodeState{rawHTML: !enc.escapeHTML}
	if err := e.typed(p, t, false, false); err != nil {
		return err
	}
	return enc.write(e.buf, nil)
}

func encoderAny(enc *Encoder, v any) error {
	if enc.err != nil {
		return enc.err
	}
	return enc.write(marshalStream(v, enc.escapeHTML))
}

func marshalStream(v any, escapeHTML bool) ([]byte, error) {
	e := &encodeState{rawHTML: !escapeHTML}
	if err := e.dynamic(v, false); err != nil {
		return nil, err
	}
	return e.buf, nil
}

// dynamicMarshalStream stands in for lib/json.MarshalStream when the
// program is compiled: values reach Encode through an interface only.
func dynamicMarshalStream(v any, escapeHTML bool) ([]byte, int, string, string, string, int64, error) {
	b, err := marshalStream(v, escapeHTML)
	if err != nil {
		return nil, nativeError, "", "", "", 0, err
	}
	return b, nativeOK, "", "", "", 0, nil
}

func (enc *Encoder) write(b []byte, err error) error {
	if err != nil {
		return err
	}
	if enc.indentPrefix != "" || enc.indentValue != "" {
		b, err = appendIndent(nil, b, enc.indentPrefix, enc.indentValue)
		if err != nil {
			return err
		}
	}
	b = append(b, '\n')
	if _, err = enc.w.Write(b); err != nil {
		enc.err = err
	}
	return err
}

// SetIndent instructs the encoder to format each subsequent encoded value as
// if indented by the package-level function Indent(dst, src, prefix, indent).
// Calling SetIndent("", "") disables indentation.
func (enc *Encoder) SetIndent(prefix, indent string) {
	enc.indentPrefix = prefix
	enc.indentValue = indent
}

// SetEscapeHTML specifies whether problematic HTML characters should be
// escaped inside JSON quoted strings. The default behavior is to escape &, <,
// and > to &, <, and >.
func (enc *Encoder) SetEscapeHTML(on bool) {
	enc.escapeHTML = on
}
