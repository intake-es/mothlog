package envelope

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
)

// ErrTooLarge se devuelve cuando el cuerpo descomprimido supera el límite.
var ErrTooLarge = errors.New("envelope demasiado grande")

// Decompress devuelve el cuerpo descomprimido según Content-Encoding, sin
// pasar de limit bytes.
func Decompress(body io.Reader, contentEncoding string, limit int64) ([]byte, error) {
	r, err := decoder(body, contentEncoding)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrMalformed, contentEncoding, err)
	}
	if int64(len(data)) > limit {
		return nil, ErrTooLarge
	}
	return data, nil
}

func decoder(body io.Reader, contentEncoding string) (io.Reader, error) {
	switch enc := strings.ToLower(strings.TrimSpace(contentEncoding)); enc {
	case "", "identity":
		return body, nil
	case "gzip", "x-gzip":
		r, err := gzip.NewReader(body)
		if err != nil {
			return nil, fmt.Errorf("%w: gzip: %v", ErrMalformed, err)
		}
		return r, nil
	case "deflate":
		return deflateReader(body)
	case "br":
		return brotli.NewReader(body), nil
	default:
		return nil, fmt.Errorf("%w: Content-Encoding no soportado: %q", ErrMalformed, contentEncoding)
	}
}

// deflateReader acepta deflate con envoltorio zlib (lo que dice HTTP) y deflate
// a pelo, que también mandan algunos clientes.
func deflateReader(body io.Reader) (io.Reader, error) {
	var head [2]byte
	n, err := io.ReadFull(body, head[:])
	if err != nil {
		return nil, fmt.Errorf("%w: deflate: %v", ErrMalformed, err)
	}
	full := io.MultiReader(bytes.NewReader(head[:n]), body)
	if head[0]&0x0f == 8 && (uint16(head[0])<<8|uint16(head[1]))%31 == 0 {
		r, err := zlib.NewReader(full)
		if err != nil {
			return nil, fmt.Errorf("%w: deflate: %v", ErrMalformed, err)
		}
		return r, nil
	}
	return flate.NewReader(full), nil
}
