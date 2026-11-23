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

const (
	// maxObjectMembers es el tope de miembros de un objeto, repetidos incluidos.
	maxObjectMembers = 10_000
	// Con más miembros que esto, los repetidos se buscan en un mapa.
	linearMembers = 8
)

var errCanonicalTooBig = errors.New("JSON demasiado grande al normalizarlo")

// canonicalJSON reescribe un JSON como lo devolvía Postgres al leerlo de una
// columna jsonb: claves sin repetir (gana la última), ordenadas por longitud y
// luego por bytes, y los números como numeric. Así un evento guardado
// comprimido se lee igual que cuando se guardaba en jsonb.
//
// La salida no pasa del doble de la entrada más 1 KiB; si pasaría, devuelve
// error y quien llama se queda con el JSON tal cual.
func canonicalJSON(b []byte) ([]byte, error) {
	c := canon{d: json.NewDecoder(bytes.NewReader(b)), budget: 2*len(b) + 1024}
	c.d.UseNumber()
	root, err := c.value()
	if err != nil {
		return nil, err
	}
	if _, err := c.d.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON con datos de más")
	}
	if root.node < 0 {
		return c.buf[root.start:root.end:root.end], nil
	}
	out := make([]byte, 0, len(c.buf))
	out = c.emit(out, root)
	if len(out) > c.budget {
		return nil, errCanonicalTooBig
	}
	return out, nil
}

// canon escribe cada valor una vez en buf, en el orden en que llega. Un
// objeto se reordena ahí mismo si sus miembros no se han movido antes; si
// no, queda como nodo y se ordena al escribir la salida, para que ningún
// byte se copie una vez por cada nivel que lo contiene.
type canon struct {
	d       *json.Decoder
	budget  int
	buf     []byte
	scratch []byte
	pending []kid
	nodes   []node
}

// ref es un valor ya normalizado: buf[start:end] o, si node >= 0, un nodo.
type ref struct {
	start, end int
	node       int
	// moved dice si alguno de sus bytes ya se ha movido al reordenar.
	moved bool
}

// kid es un miembro de un objeto (buf[start:colon] es `"clave":`) o un
// elemento de un array, que puede abarcar varios elementos seguidos.
type kid struct {
	key          string
	start, colon int
	val          ref
}

type node struct {
	object bool
	kids   []kid
}

func (c *canon) write(p ...byte) error {
	c.buf = append(c.buf, p...)
	return c.check()
}

func (c *canon) writeText(s string) error {
	c.buf = append(c.buf, s...)
	return c.check()
}

func (c *canon) check() error {
	if len(c.buf) > c.budget {
		return errCanonicalTooBig
	}
	return nil
}

func (c *canon) value() (ref, error) {
	tok, err := c.d.Token()
	if err != nil {
		return ref{}, err
	}
	start := len(c.buf)
	switch v := tok.(type) {
	case json.Delim:
		if v == '[' {
			return c.array()
		}
		return c.object()
	case string:
		err = c.writeString(v)
	case json.Number:
		err = c.writeText(numericText(string(v)))
	case bool:
		err = c.writeText(strconv.FormatBool(v))
	case nil:
		err = c.writeText("null")
	}
	return ref{start: start, end: len(c.buf), node: -1}, err
}

func (c *canon) writeString(s string) error {
	b, _ := json.Marshal(s)
	return c.write(b...)
}

func (c *canon) array() (ref, error) {
	start, p0 := len(c.buf), len(c.pending)
	defer func() { c.pending = c.pending[:p0] }()
	if err := c.write('['); err != nil {
		return ref{}, err
	}
	collapsed, moved, prevSpan := true, false, false
	for i := 0; c.d.More(); i++ {
		if i > 0 && prevSpan {
			if err := c.write(','); err != nil {
				return ref{}, err
			}
		}
		v, err := c.value()
		if err != nil {
			return ref{}, err
		}
		moved = moved || v.moved
		if v.node >= 0 {
			collapsed = false
		}
		// Los elementos seguidos que ya están en buf van en un solo trozo.
		if last := len(c.pending) - 1; v.node < 0 && prevSpan && last >= p0 {
			c.pending[last].val.end = v.end
			c.pending[last].val.moved = c.pending[last].val.moved || v.moved
		} else {
			c.pending = append(c.pending, kid{val: v})
		}
		prevSpan = v.node < 0
	}
	if _, err := c.d.Token(); err != nil {
		return ref{}, err
	}
	if collapsed {
		err := c.write(']')
		return ref{start: start, end: len(c.buf), node: -1, moved: moved}, err
	}
	return c.newNode(false, p0, moved), nil
}

func (c *canon) object() (ref, error) {
	start, p0 := len(c.buf), len(c.pending)
	defer func() { c.pending = c.pending[:p0] }()
	if err := c.write('{'); err != nil {
		return ref{}, err
	}
	var (
		index                 map[string]int
		dup, hasNode, anyMove bool
	)
	for n := 0; c.d.More(); n++ {
		if n >= maxObjectMembers {
			return ref{}, errCanonicalTooBig
		}
		k, err := c.d.Token()
		if err != nil {
			return ref{}, err
		}
		key, _ := k.(string)
		if n > 0 {
			if err := c.write(','); err != nil {
				return ref{}, err
			}
		}
		at := len(c.buf)
		if err := c.writeString(key); err != nil {
			return ref{}, err
		}
		if err := c.write(':'); err != nil {
			return ref{}, err
		}
		colon := len(c.buf)
		v, err := c.value()
		if err != nil {
			return ref{}, err
		}
		hasNode = hasNode || v.node >= 0
		anyMove = anyMove || v.moved
		m := kid{key: key, start: at, colon: colon, val: v}

		members := c.pending[p0:]
		i := -1
		if index != nil {
			if j, ok := index[key]; ok {
				i = j
			}
		} else {
			i = slices.IndexFunc(members, func(m kid) bool { return m.key == key })
		}
		if i >= 0 {
			members[i] = m
			dup = true
			continue
		}
		c.pending = append(c.pending, m)
		if index == nil && len(members)+1 > linearMembers {
			index = make(map[string]int, 2*linearMembers)
			for j, m := range c.pending[p0:] {
				index[m.key] = j
			}
		} else if index != nil {
			index[key] = len(members)
		}
	}
	if _, err := c.d.Token(); err != nil {
		return ref{}, err
	}
	members := c.pending[p0:]
	if !hasNode && !dup && slices.IsSortedFunc(members, compareKids) {
		err := c.write('}')
		return ref{start: start, end: len(c.buf), node: -1, moved: anyMove}, err
	}
	slices.SortFunc(members, compareKids)
	if hasNode || anyMove {
		return c.newNode(true, p0, anyMove), nil
	}
	// Todos los miembros están en buf y nunca se han movido: se reordenan ahí.
	c.scratch = append(c.scratch[:0], c.buf[start:]...)
	c.buf = append(c.buf[:start], '{')
	for i, m := range members {
		if i > 0 {
			c.buf = append(c.buf, ',')
		}
		c.buf = append(c.buf, c.scratch[m.start-start:m.val.end-start]...)
	}
	err := c.write('}')
	return ref{start: start, end: len(c.buf), node: -1, moved: true}, err
}

func (c *canon) newNode(object bool, p0 int, moved bool) ref {
	c.nodes = append(c.nodes, node{object: object, kids: slices.Clone(c.pending[p0:])})
	return ref{node: len(c.nodes) - 1, moved: moved}
}

func compareKids(a, b kid) int {
	if len(a.key) != len(b.key) {
		return len(a.key) - len(b.key)
	}
	return strings.Compare(a.key, b.key)
}

func (c *canon) emit(out []byte, v ref) []byte {
	if v.node < 0 {
		return append(out, c.buf[v.start:v.end]...)
	}
	n := c.nodes[v.node]
	open, close := byte('['), byte(']')
	if n.object {
		open, close = '{', '}'
	}
	out = append(out, open)
	for i, k := range n.kids {
		if i > 0 {
			out = append(out, ',')
		}
		if n.object {
			out = append(out, c.buf[k.start:k.colon]...)
		}
		out = c.emit(out, k.val)
	}
	return append(out, close)
}

// maxNumericDigits evita que un exponente enorme dé una cadena enorme; Postgres
// los rechazaba, así que no hay forma anterior que respetar.
const maxNumericDigits = 1000

// maxNumericGrowth es lo más que puede crecer un número al escribirlo sin
// exponente; si crecería más, se queda como venía.
const maxNumericGrowth = 32

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
	if max(len(intPart)+exp, 1)+scale+2 > len(lit)+maxNumericGrowth {
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
