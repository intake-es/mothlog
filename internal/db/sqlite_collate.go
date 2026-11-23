package db

import (
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
	"modernc.org/sqlite"
)

// en_us ordena el texto como Postgres con la colación en_US.UTF-8 de glibc:
// primero las letras y los números, sin la puntuación ni los espacios, con
// las tildes y las mayúsculas detrás; luego la puntuación, en el orden de
// glibc; y, si siguen empatados, los bytes, como hace Postgres.
func init() {
	sqlite.MustRegisterCollationUtf8("en_us", compareEnUS)
}

// collators: un collate.Collator no se puede usar desde dos goroutines a la vez.
var collators = sync.Pool{New: func() any { return collate.New(language.English) }}

func compareEnUS(a, b string) int {
	c := collators.Get().(*collate.Collator)
	r := c.CompareString(alnum(a), alnum(b))
	collators.Put(c)
	if r != 0 {
		return r
	}
	if r := comparePunct(a, b); r != 0 {
		return r
	}
	return strings.Compare(a, b)
}

func isAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func alnum(s string) string {
	return strings.Map(func(r rune) rune {
		if isAlnum(r) {
			return r
		}
		return -1
	}, s)
}

// punctOrder es el orden de glibc de la puntuación ASCII.
const punctOrder = "$ !\"#%&'()*+,-./:;<=>?@[\\]^_`{|}~"

// punctWeight es el peso de r en el desempate por puntuación: la puntuación,
// por su orden en glibc, antes que las letras y los números.
func punctWeight(r rune) int {
	if isAlnum(r) {
		return 1 << 20
	}
	if i := strings.IndexRune(punctOrder, r); i >= 0 {
		return i
	}
	return len(punctOrder) + int(r)
}

func comparePunct(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	for i := range min(len(ra), len(rb)) {
		if wa, wb := punctWeight(ra[i]), punctWeight(rb[i]); wa != wb {
			if wa < wb {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(ra) < len(rb):
		return -1
	case len(ra) > len(rb):
		return 1
	}
	return 0
}
