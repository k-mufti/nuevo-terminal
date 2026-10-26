package cluster

import (
	"strings"
	"unicode"
)

var stopwords = toSet(`a about above after again against all also am amid an and any are as at be because been
before being below between both but by can could did do does doing down during each few for from further had
has have having he her here hers him his how i if in into is it its itself just me more most my no nor not now
of off on once only or other our out over own same she should so some such than that the their them then there
these they this those through to too under until up very was we were what when where which while who whom why
will with would you your
says say said new news live latest update updates video watch photos report reports reportedly year years
week day today tonight first last back get gets got make makes made take takes how what why could here
via amid per`)

// Number words become digits so "seven killed" matches "7 killed".
var numberWords = map[string]string{
	"one": "1", "two": "2", "three": "3", "four": "4", "five": "5",
	"six": "6", "seven": "7", "eight": "8", "nine": "9", "ten": "10",
}

func toSet(s string) map[string]struct{} {
	m := make(map[string]struct{})
	for _, w := range strings.Fields(s) {
		m[w] = struct{}{}
	}
	return m
}

var abbreviations = strings.NewReplacer(
	"U.S.", " usa ", "U.K.", " uk ", "U.N.", " un ", "E.U.", " eu ",
)

// Tokenize turns a headline into a deduplicated set of comparable words.
func Tokenize(title string) []string {
	title = abbreviations.Replace(title)
	fields := strings.FieldsFunc(title, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := make(map[string]struct{}, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		var t string
		if f == "US" {
			t = "usa" // keep "US" (the country) apart from "us" (the pronoun)
		} else {
			t = strings.ToLower(f)
			if d, ok := numberWords[t]; ok {
				t = d
			} else {
				t = stem(t)
			}
		}
		if len([]rune(t)) < 2 && !isDigits(t) {
			continue
		}
		if _, stop := stopwords[t]; stop {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// stem is a deliberately light suffix folding, enough for headlines:
// strikes/striking/struck-ish forms -> "strik", killed/killing -> "kill".
// It only has to be consistent, not produce real words.
func stem(w string) string {
	if !isASCII(w) {
		return w
	}
	n := len(w)
	switch {
	case n > 4 && strings.HasSuffix(w, "ies"):
		w = w[:n-3] + "y"
	case n > 5 && strings.HasSuffix(w, "ing"):
		w = undouble(w[:n-3])
	case n > 4 && strings.HasSuffix(w, "eed"):
		w = w[:n-1] // agreed -> agree
	case n > 4 && strings.HasSuffix(w, "ed"):
		w = undouble(w[:n-2])
	case n > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is"):
		w = w[:n-1]
	}
	if len(w) > 3 && strings.HasSuffix(w, "e") {
		w = w[:len(w)-1]
	}
	return w
}

// undouble turns "stopp" into "stop" (but keeps "kill", "pass", "buzz").
func undouble(w string) string {
	n := len(w)
	if n > 2 && w[n-1] == w[n-2] && !strings.ContainsRune("lsz", rune(w[n-1])) {
		return w[:n-1]
	}
	return w
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
