package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
)

// canonicalJSON reescribe un JSON como lo devolvía Postgres al leerlo de una
// columna jsonb: claves sin repetir (gana la última), ordenadas por longitud y
// luego por bytes, y los números como numeric. Así un evento guardado
// comprimido se lee igual que cuando se guardaba en jsonb.
func canonicalJSON(b []byte) ([]byte, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var out bytes.Buffer
	if err := canonicalValue(d, &out); err != nil {
		return nil, err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON con datos de más")
	}
	return out.Bytes(), nil
}

type member struct {
	key   string
	value []byte
}

func canonicalValue(d *json.Decoder, out *bytes.Buffer) error {
	tok, err := d.Token()
	if err != nil {
		return err
	}
	switch v := tok.(type) {
	case json.Delim:
		if v == '[' {
			out.WriteByte('[')
			for i := 0; d.More(); i++ {
				if i > 0 {
					out.WriteByte(',')
				}
				if err := canonicalValue(d, out); err != nil {
					return err
				}
			}
			out.WriteByte(']')
			_, err := d.Token()
			return err
		}
		var members []member
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return err
			}
			key, _ := k.(string)
			var val bytes.Buffer
			if err := canonicalValue(d, &val); err != nil {
				return err
			}
			if i := slices.IndexFunc(members, func(m member) bool { return m.key == key }); i >= 0 {
				members[i].value = val.Bytes()
			} else {
				members = append(members, member{key, val.Bytes()})
			}
		}
		if _, err := d.Token(); err != nil {
			return err
		}
		slices.SortFunc(members, func(a, b member) int {
			if len(a.key) != len(b.key) {
				return len(a.key) - len(b.key)
			}
			return strings.Compare(a.key, b.key)
		})
		out.WriteByte('{')
		for i, m := range members {
			if i > 0 {
				out.WriteByte(',')
			}
			writeString(out, m.key)
			out.WriteByte(':')
			out.Write(m.value)
		}
		out.WriteByte('}')
	case string:
		writeString(out, v)
	case json.Number:
		out.WriteString(numericText(string(v)))
	case bool:
		out.WriteString(strconv.FormatBool(v))
	case nil:
		out.WriteString("null")
	}
	return nil
}

func writeString(out *bytes.Buffer, s string) {
	b, _ := json.Marshal(s)
	out.Write(b)
}

// maxNumericDigits evita que un exponente enorme dé una cadena enorme; Postgres
// los rechazaba, así que no hay forma anterior que respetar.
const maxNumericDigits = 1000

// numericText es el texto de un número JSON como lo escribe el numeric de
// Postgres: sin exponente y con tantos decimales como tenía la mantisa menos
// el exponente.
func numericText(lit string) string {
	neg := strings.HasPrefix(lit, "-")
	s := strings.TrimPrefix(lit, "-")
	mant, exp := s, 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.Atoi(s[i+1:])
		if err != nil {
			return lit
		}
		mant, exp = s[:i], e
	}
	intPart, frac, _ := strings.Cut(mant, ".")
	scale := max(len(frac)-exp, 0)
	if scale > maxNumericDigits || len(intPart)+exp > maxNumericDigits {
		return lit
	}
	// El valor es digits·10^-scale.
	digits := strings.TrimLeft(intPart+frac, "0")
	if shift := exp - len(frac); shift > 0 && digits != "" {
		digits += strings.Repeat("0", shift)
	}
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	whole, dec := digits[:len(digits)-scale], digits[len(digits)-scale:]
	if strings.Trim(whole+dec, "0") == "" {
		neg = false
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	b.WriteString(whole)
	if scale > 0 {
		b.WriteByte('.')
		b.WriteString(dec)
	}
	return b.String()
}
