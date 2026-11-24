// Command enlaces comprueba que los enlaces relativos de los Markdown del
// repositorio apuntan a ficheros que existen y, si llevan #ancla, a un título
// de ese fichero. Los enlaces a otras webs no se miran.
//
//	go run ./tools/enlaces [raíz]
package main

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	files, broken, err := check(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for _, b := range broken {
		fmt.Println(b)
	}
	fmt.Printf("%d ficheros Markdown, %d enlaces rotos\n", files, len(broken))
	if len(broken) > 0 {
		os.Exit(1)
	}
}

var skipDirs = []string{".git", "node_modules", ".angular", "dist", "trabajo"}

// check devuelve cuántos Markdown ha leído y los enlaces rotos, como
// «fichero:línea: enlace: motivo».
func check(root string) (int, []string, error) {
	var mds []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && slices.Contains(skipDirs, d.Name()) {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".md") {
			mds = append(mds, path)
		}
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	anchors := map[string]map[string]bool{}
	anchorsOf := func(path string) (map[string]bool, error) {
		if a, ok := anchors[path]; ok {
			return a, nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		a := headingAnchors(string(b))
		anchors[path] = a
		return a, nil
	}
	var broken []string
	for _, md := range mds {
		b, err := os.ReadFile(md)
		if err != nil {
			return 0, nil, err
		}
		rel, _ := filepath.Rel(root, md)
		for _, l := range links(string(b)) {
			if why := checkLink(md, l.target, anchorsOf); why != "" {
				broken = append(broken, filepath.ToSlash(rel)+":"+strconv.Itoa(l.line)+": "+l.target+": "+why)
			}
		}
	}
	return len(mds), broken, nil
}

func checkLink(from, target string, anchorsOf func(string) (map[string]bool, error)) string {
	if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
		return ""
	}
	path, frag, _ := strings.Cut(target, "#")
	file := from
	if path != "" {
		p, err := url.PathUnescape(path)
		if err != nil {
			return "ruta mal codificada"
		}
		file = filepath.Join(filepath.Dir(from), filepath.FromSlash(p))
		st, err := os.Stat(file)
		if err != nil {
			return "no existe"
		}
		if st.IsDir() {
			if frag != "" {
				return "ancla en un directorio"
			}
			return ""
		}
	}
	if frag == "" {
		return ""
	}
	if !strings.HasSuffix(file, ".md") {
		return "ancla en un fichero que no es Markdown"
	}
	a, err := anchorsOf(file)
	if err != nil {
		return err.Error()
	}
	f, err := url.PathUnescape(frag)
	if err != nil || !a[strings.ToLower(f)] {
		return "no hay ningún título con esa ancla"
	}
	return ""
}

type link struct {
	line   int
	target string
}

var (
	// [texto](destino) y ![alt](destino), con un título opcional entre comillas.
	inlineLink = regexp.MustCompile(`!?\[(?:[^\[\]]|\[[^\]]*\])*\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)`)
	codeSpan   = regexp.MustCompile("`+[^`]*`+")
	heading    = regexp.MustCompile(`^#{1,6}\s+(.*?)\s*#*\s*$`)
)

// links saca los enlaces de fuera de los bloques y los trozos de código.
func links(md string) []link {
	var out []link
	fence := ""
	for i, line := range strings.Split(md, "\n") {
		if f := fenceOf(line); f != "" {
			switch {
			case fence == "":
				fence = f
			case strings.HasPrefix(strings.TrimSpace(line), fence):
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		line = codeSpan.ReplaceAllString(line, "")
		for _, m := range inlineLink.FindAllStringSubmatch(line, -1) {
			out = append(out, link{line: i + 1, target: m[1]})
		}
	}
	return out
}

func fenceOf(line string) string {
	t := strings.TrimSpace(line)
	for _, f := range []string{"```", "~~~"} {
		if strings.HasPrefix(t, f) {
			return f
		}
	}
	return ""
}

// headingAnchors son las anclas que GitHub da a los títulos de md: en
// minúsculas, sin puntuación, con guiones por espacios y -1, -2… en los repetidos.
func headingAnchors(md string) map[string]bool {
	out := map[string]bool{}
	seen := map[string]int{}
	fence := ""
	for _, line := range strings.Split(md, "\n") {
		if f := fenceOf(line); f != "" {
			switch {
			case fence == "":
				fence = f
			case strings.HasPrefix(strings.TrimSpace(line), fence):
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		m := heading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		slug := slugify(m[1])
		if n := seen[slug]; n > 0 {
			out[slug+"-"+strconv.Itoa(n)] = true
		} else {
			out[slug] = true
		}
		seen[slug]++
	}
	return out
}

func slugify(title string) string {
	title = inlineLink.ReplaceAllStringFunc(title, func(s string) string {
		return s[strings.Index(s, "[")+1 : strings.LastIndex(s, "]")]
	})
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}
