package sourcemap

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Map es un source map v3 decodificado.
// Formato: https://tc39.es/ecma426/
type Map struct {
	sources []string
	// contents son las líneas de cada original, partidas al parsear; nil si
	// el mapa no trae su sourcesContent.
	contents [][]string
	index    map[string]int
	lines    [][]segment
	size     int64
}

type segment struct {
	genCol  int32
	src     int32 // -1 si el segmento no apunta a ningún original
	srcLine int32
	srcCol  int32
}

type rawMap struct {
	Version        *int             `json:"version"`
	SourceRoot     string           `json:"sourceRoot"`
	Sources        []*string        `json:"sources"`
	SourcesContent []*string        `json:"sourcesContent"`
	Mappings       *string          `json:"mappings"`
	Sections       *json.RawMessage `json:"sections"`
}

// Parse decodifica y valida un source map v3.
func Parse(b []byte) (*Map, error) {
	var r rawMap
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if r.Version == nil || *r.Version != 3 {
		return nil, errors.New("version is not 3")
	}
	if r.Sections != nil {
		return nil, errors.New("index maps (sections) are not supported")
	}
	if r.Mappings == nil {
		return nil, errors.New("missing mappings")
	}
	m := &Map{
		sources:  make([]string, len(r.Sources)),
		contents: make([][]string, len(r.Sources)),
		index:    make(map[string]int, len(r.Sources)),
		// El texto de las fuentes y su contenido sale del JSON, que no se
		// retiene; cada fuente cuesta además su cadena, su slice de líneas y
		// su entrada en el índice.
		size: int64(len(b)) + int64(len(r.Sources))*(16+24+64),
	}
	for i, s := range r.Sources {
		if s != nil {
			m.sources[i] = cleanSource(r.SourceRoot, *s)
		}
		if _, ok := m.index[m.sources[i]]; !ok {
			m.index[m.sources[i]] = i
		}
		if i < len(r.SourcesContent) && r.SourcesContent[i] != nil {
			lines := strings.Split(*r.SourcesContent[i], "\n")
			for j, l := range lines {
				lines[j] = strings.TrimSuffix(l, "\r")
			}
			m.contents[i] = lines
			m.size += int64(len(lines)) * 16
		}
	}
	lines, err := decodeMappings(*r.Mappings, len(m.sources))
	if err != nil {
		return nil, err
	}
	m.lines = lines
	m.size += int64(cap(lines)) * 24
	for _, l := range lines {
		m.size += int64(cap(l)) * 16
	}
	return m, nil
}

// Original es la posición en el código fuente, con línea y columna en base 0.
type Original struct {
	Source string
	Line   int
	Column int
}

// Lookup traduce una posición del fichero generado (línea y columna en base 0).
func (m *Map) Lookup(line, col int) (Original, bool) {
	if line < 0 || line >= len(m.lines) || col < 0 {
		return Original{}, false
	}
	segs := m.lines[line]
	i, found := slices.BinarySearchFunc(segs, int32(col), func(s segment, c int32) int { return int(s.genCol - c) })
	if !found {
		i--
	}
	if i < 0 || segs[i].src < 0 {
		return Original{}, false
	}
	s := segs[i]
	return Original{Source: m.sources[s.src], Line: int(s.srcLine), Column: int(s.srcCol)}, true
}

// SourceLines devuelve las líneas del original si el mapa trae su contenido.
// Son las de la caché: quien las use no debe modificarlas.
func (m *Map) SourceLines(source string) []string {
	i, ok := m.index[source]
	if !ok {
		return nil
	}
	return m.contents[i]
}

func cleanSource(root, s string) string {
	if root != "" && !strings.Contains(s, "://") && !strings.HasPrefix(s, "/") {
		s = strings.TrimSuffix(root, "/") + "/" + s
	}
	if rest, ok := strings.CutPrefix(s, "webpack://"); ok {
		if r, ok := strings.CutPrefix(rest, "/"); ok {
			s = r
		} else if _, after, ok := strings.Cut(rest, "/"); ok {
			s = after // webpack://<espacio>/ruta
		} else {
			s = rest
		}
	}
	for strings.HasPrefix(s, "./") {
		s = s[2:]
	}
	return s
}

const base64Chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

var base64Value = func() (t [256]int8) {
	for i := range t {
		t[i] = -1
	}
	for i, c := range base64Chars {
		t[c] = int8(i)
	}
	return t
}()

// decodeMappings devuelve los segmentos de cada línea generada, ordenados por
// columna.
func decodeMappings(s string, nSources int) ([][]segment, error) {
	var (
		lines                [][]segment
		cur                  []segment
		src, srcLine, srcCol int64
		genCol               int64
		fields               [5]int64
		nf                   int
	)
	flushSegment := func() error {
		switch nf {
		case 0:
			return nil
		case 1, 4, 5:
		default:
			return fmt.Errorf("mappings: segment with %d fields on line %d", nf, len(lines)+1)
		}
		genCol += fields[0]
		seg := segment{genCol: int32(genCol), src: -1}
		if nf >= 4 {
			src += fields[1]
			srcLine += fields[2]
			srcCol += fields[3]
			if src < 0 || src >= int64(nSources) {
				return fmt.Errorf("mappings: source %d out of range on line %d", src, len(lines)+1)
			}
			if srcLine < 0 || srcCol < 0 || srcLine > 1<<30 || srcCol > 1<<30 {
				return fmt.Errorf("mappings: invalid original position on line %d", len(lines)+1)
			}
			seg.src, seg.srcLine, seg.srcCol = int32(src), int32(srcLine), int32(srcCol)
		}
		if genCol < 0 || genCol > 1<<30 {
			return fmt.Errorf("mappings: invalid column on line %d", len(lines)+1)
		}
		cur = append(cur, seg)
		nf = 0
		return nil
	}
	flushLine := func() {
		if !slices.IsSortedFunc(cur, func(a, b segment) int { return int(a.genCol - b.genCol) }) {
			slices.SortStableFunc(cur, func(a, b segment) int { return int(a.genCol - b.genCol) })
		}
		lines = append(lines, cur)
		cur = nil
		genCol = 0
	}

	for i := 0; i < len(s); {
		switch s[i] {
		case ';':
			if err := flushSegment(); err != nil {
				return nil, err
			}
			flushLine()
			i++
			continue
		case ',':
			if nf == 0 {
				return nil, fmt.Errorf("mappings: empty segment on line %d", len(lines)+1)
			}
			if err := flushSegment(); err != nil {
				return nil, err
			}
			i++
			continue
		}
		v, n, err := decodeVLQ(s[i:])
		if err != nil {
			return nil, fmt.Errorf("mappings: %w on line %d", err, len(lines)+1)
		}
		if nf == len(fields) {
			return nil, fmt.Errorf("mappings: segment with more than 5 fields on line %d", len(lines)+1)
		}
		fields[nf] = v
		nf++
		i += n
	}
	if err := flushSegment(); err != nil {
		return nil, err
	}
	flushLine()
	return lines, nil
}

func decodeVLQ(s string) (int64, int, error) {
	var v int64
	shift := 0
	for i := 0; i < len(s); i++ {
		d := base64Value[s[i]]
		if d < 0 {
			return 0, 0, fmt.Errorf("invalid character %q", s[i])
		}
		v |= int64(d&31) << shift
		if d&32 == 0 {
			if v&1 == 1 {
				return -(v >> 1), i + 1, nil
			}
			return v >> 1, i + 1, nil
		}
		shift += 5
		if shift > 35 {
			return 0, 0, errors.New("VLQ too long")
		}
	}
	return 0, 0, errors.New("VLQ incompleto")
}
