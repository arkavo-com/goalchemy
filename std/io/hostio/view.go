package hostio

import (
	"github.com/eugenioenko/goalchemy/std/errors"
	"github.com/eugenioenko/goalchemy/std/io"
)

// MaxView bounds the bytes one host read of a source stream returns.
const MaxView = 1 << 20

var errCount = errors.New("hostio: invalid read count")

// ReadView reads at most n bytes from r for a host stream view. It retries
// empty reads, so it returns data, end of stream, or an error. eof reports
// that r returned io.EOF; data read before it is returned with it.
func ReadView(r io.Reader, n int) ([]byte, bool, error) {
	if n <= 0 {
		return nil, false, nil
	}
	if n > MaxView {
		n = MaxView
	}
	p := make([]byte, n)
	for empty := 0; ; empty++ {
		k, err := r.Read(p)
		if k < 0 || k > n {
			return nil, false, errCount
		}
		if err == io.EOF {
			return p[:k], true, nil
		}
		if k > 0 || err != nil {
			return p[:k], false, err
		}
		if empty >= 100 {
			return nil, false, io.ErrNoProgress
		}
	}
}

// WriteView writes all of p to w for a host stream view.
func WriteView(w io.Writer, p []byte) (int, error) {
	n, err := w.Write(p)
	if err == nil && n < len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

// SeekView seeks s for a host stream view.
func SeekView(s io.Seeker, offset int64, whence int) (int64, error) {
	return s.Seek(offset, whence)
}

// CloseView closes c for a host stream view.
func CloseView(c io.Closer) error { return c.Close() }
