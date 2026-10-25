package audit

import (
	"errors"
	"strings"
)

// word is one shell word after quote removal. Substitutions are kept in Text
// verbatim and their inner commands are listed in Subs for recursive checks.
type word struct {
	Text    string
	Quoted  bool // any quoting or escaping was used
	Dynamic bool // contains $VAR, ${...}, $(...), `...`, <(...) or >(...)
	AnsiC   bool // $'...' quoting (can spell arbitrary bytes)
	Glob    bool // unquoted * ? or [
	Subs    []string
}

type redirect struct {
	Op     string
	Target word
}

type simple struct {
	Words     []word
	Redirects []redirect
	Piped     bool // stdin comes from the previous pipeline stage
	Stdin     bool // stdin comes from a here-doc, here-string or "<" file
	Pipeline  int  // index of the pipeline this command belongs to
	Stage     int  // position inside the pipeline
}

type token struct {
	op   string // operator; empty for words
	word word
}

var errParse = errors.New("unparseable")

// lex splits a shell command into words and operators. It understands single,
// double and ANSI-C quotes, backslash escapes, $(...), ${...}, backticks,
// process substitution, comments and here-documents.
func lex(s string) ([]token, error) {
	var out []token
	var heredocs []string
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '\\' && i+1 < len(s) && s[i+1] == '\n':
			i += 2
		case c == '\n':
			out = append(out, token{op: "\n"})
			i++
			for _, delim := range heredocs {
				i = skipHeredoc(s, i, delim)
			}
			heredocs = nil
		case c == '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == ';' || c == '(' || c == ')':
			if c == ';' && i+1 < len(s) && s[i+1] == ';' {
				out = append(out, token{op: ";;"})
				i += 2
				continue
			}
			out = append(out, token{op: string(c)})
			i++
		case c == '|' || c == '&':
			if i+1 < len(s) && (s[i+1] == c || (c == '|' && s[i+1] == '&')) {
				out = append(out, token{op: s[i : i+2]})
				i += 2
				continue
			}
			if c == '&' && i+1 < len(s) && s[i+1] == '>' {
				op, n := redirOp(s[i:])
				out = append(out, token{op: op})
				i += n
				continue
			}
			out = append(out, token{op: string(c)})
			i++
		case c == '<' || c == '>':
			if i+1 < len(s) && s[i+1] == '(' {
				w, n, err := lexWord(s[i:])
				if err != nil {
					return nil, err
				}
				out = append(out, token{word: w})
				i += n
				continue
			}
			op, n := redirOp(s[i:])
			out = append(out, token{op: op})
			i += n
			if op == "<<" || op == "<<-" {
				for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
					i++
				}
				w, n, err := lexWord(s[i:])
				if err != nil || n == 0 {
					return nil, errParse
				}
				out = append(out, token{word: w})
				i += n
				heredocs = append(heredocs, w.Text)
			}
		default:
			// A leading file-descriptor number belongs to the redirection.
			j := i
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if j > i && j < len(s) && (s[j] == '<' || s[j] == '>') {
				op, n := redirOp(s[j:])
				out = append(out, token{op: op})
				i = j + n
				continue
			}
			w, n, err := lexWord(s[i:])
			if err != nil {
				return nil, err
			}
			if n == 0 {
				return nil, errParse
			}
			out = append(out, token{word: w})
			i += n
		}
	}
	return out, nil
}

func redirOp(s string) (string, int) {
	for _, op := range []string{"&>>", "<<<", "<<-", "&>", ">>", ">|", ">&", "<&", "<>", "<<", ">", "<"} {
		if strings.HasPrefix(s, op) {
			return op, len(op)
		}
	}
	return s[:1], 1
}

func skipHeredoc(s string, i int, delim string) int {
	for i < len(s) {
		end := strings.IndexByte(s[i:], '\n')
		line := s[i:]
		next := len(s)
		if end >= 0 {
			line = s[i : i+end]
			next = i + end + 1
		}
		i = next
		if strings.TrimLeft(line, "\t") == delim {
			return i
		}
	}
	return i
}

func isMeta(c byte) bool {
	return strings.IndexByte(" \t\r\n;&|()<>", c) >= 0
}

// lexWord reads one word starting at s[0] and returns it with its length.
func lexWord(s string) (word, int, error) {
	var w word
	var b strings.Builder
	i := 0
	for i < len(s) {
		c := s[i]
		if isMeta(c) {
			if (c == '<' || c == '>') && i+1 < len(s) && s[i+1] == '(' {
				end, err := matchParen(s, i+1)
				if err != nil {
					return w, 0, err
				}
				w.Dynamic = true
				w.Subs = append(w.Subs, s[i+2:end])
				b.WriteString(s[i : end+1])
				i = end + 1
				continue
			}
			break
		}
		switch c {
		case '\\':
			w.Quoted = true
			if i+1 < len(s) {
				b.WriteByte(s[i+1])
				i += 2
			} else {
				i++
			}
		case '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return w, 0, errParse
			}
			w.Quoted = true
			b.WriteString(s[i+1 : i+1+end])
			i += end + 2
		case '"':
			w.Quoted = true
			j := i + 1
			closed := false
			for j < len(s) {
				d := s[j]
				if d == '"' {
					closed = true
					j++
					break
				}
				if d == '\\' && j+1 < len(s) {
					if strings.IndexByte("$`\"\\\n", s[j+1]) >= 0 {
						b.WriteByte(s[j+1])
					} else {
						b.WriteByte('\\')
						b.WriteByte(s[j+1])
					}
					j += 2
					continue
				}
				if d == '$' || d == '`' {
					n, err := expansion(s[j:], &w, &b)
					if err != nil {
						return w, 0, err
					}
					j += n
					continue
				}
				b.WriteByte(d)
				j++
			}
			if !closed {
				return w, 0, errParse
			}
			i = j
		case '$':
			if i+1 < len(s) && s[i+1] == '\'' {
				end := strings.IndexByte(s[i+2:], '\'')
				for end >= 0 && s[i+2+end-1] == '\\' {
					next := strings.IndexByte(s[i+2+end+1:], '\'')
					if next < 0 {
						end = -1
						break
					}
					end += next + 1
				}
				if end < 0 {
					return w, 0, errParse
				}
				w.Quoted, w.AnsiC = true, true
				b.WriteString(s[i+2 : i+2+end])
				i += end + 3
				continue
			}
			n, err := expansion(s[i:], &w, &b)
			if err != nil {
				return w, 0, err
			}
			i += n
		case '`':
			n, err := expansion(s[i:], &w, &b)
			if err != nil {
				return w, 0, err
			}
			i += n
		case '*', '?', '[':
			w.Glob = true
			b.WriteByte(c)
			i++
		case '{':
			// Brace expansion like {a,b} or {1..3} can build words.
			if end := strings.IndexByte(s[i:], '}'); end > 0 && strings.ContainsAny(s[i:i+end], ",.") && !strings.ContainsAny(s[i:i+end], " \t\n") {
				w.Glob = true
			}
			b.WriteByte(c)
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	w.Text = b.String()
	return w, i, nil
}

// expansion consumes $VAR, ${...}, $(...), $((...)) or `...` at s[0].
func expansion(s string, w *word, b *strings.Builder) (int, error) {
	if s[0] == '`' {
		end := 1
		for end < len(s) && s[end] != '`' {
			if s[end] == '\\' {
				end++
			}
			end++
		}
		if end >= len(s) {
			return 0, errParse
		}
		w.Dynamic = true
		w.Subs = append(w.Subs, strings.ReplaceAll(s[1:end], "\\`", "`"))
		b.WriteString(s[:end+1])
		return end + 1, nil
	}
	if len(s) < 2 {
		b.WriteByte('$')
		return 1, nil
	}
	switch {
	case s[1] == '(':
		end, err := matchParen(s, 1)
		if err != nil {
			return 0, err
		}
		w.Dynamic = true
		inner := s[2:end]
		if strings.HasPrefix(inner, "(") && strings.HasSuffix(inner, ")") {
			// $(( arithmetic )) — may still hide $(...) inside.
			if strings.Contains(inner, "$(") || strings.Contains(inner, "`") {
				w.Subs = append(w.Subs, inner[1:len(inner)-1])
			}
		} else {
			w.Subs = append(w.Subs, inner)
		}
		b.WriteString(s[:end+1])
		return end + 1, nil
	case s[1] == '{':
		depth, j := 0, 1
		for j < len(s) {
			if s[j] == '{' {
				depth++
			} else if s[j] == '}' {
				depth--
				if depth == 0 {
					break
				}
			}
			j++
		}
		if j >= len(s) {
			return 0, errParse
		}
		w.Dynamic = true
		body := s[2:j]
		if strings.Contains(body, "$(") || strings.Contains(body, "`") {
			w.Subs = append(w.Subs, body)
		}
		b.WriteString(s[:j+1])
		return j + 1, nil
	case s[1] == '_' || s[1] == '@' || s[1] == '*' || s[1] == '#' || s[1] == '?' || s[1] == '!' || s[1] == '$' || s[1] == '-' || (s[1] >= '0' && s[1] <= '9') || isIdent(s[1]):
		j := 2
		if isIdent(s[1]) || s[1] == '_' {
			for j < len(s) && (isIdent(s[j]) || (s[j] >= '0' && s[j] <= '9') || s[j] == '_') {
				j++
			}
		}
		w.Dynamic = true
		b.WriteString(s[:j])
		return j, nil
	}
	b.WriteByte('$')
	return 1, nil
}

func isIdent(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' }

// matchParen returns the index of the ")" closing the "(" at s[open].
func matchParen(s string, open int) (int, error) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return 0, errParse
			}
			i += end + 1
		case '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(s) {
				return 0, errParse
			}
			i = j
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return 0, errParse
}

// parse groups tokens into simple commands. Grouping with ( ) and { } is
// flattened: every simple command is checked on its own.
func parse(s string) ([]simple, error) {
	toks, err := lex(s)
	if err != nil {
		return nil, err
	}
	var out []simple
	cur := simple{}
	pipeline, stage := 0, 0
	piped := false
	flush := func() {
		if len(cur.Words) > 0 || len(cur.Redirects) > 0 {
			cur.Piped = piped
			cur.Pipeline, cur.Stage = pipeline, stage
			out = append(out, cur)
		}
		cur = simple{}
	}
	depth := 0
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch t.op {
		case "":
			cur.Words = append(cur.Words, t.word)
		case "|", "|&":
			if len(cur.Words) == 0 && len(cur.Redirects) == 0 && depth == 0 && len(out) == 0 {
				return nil, errParse
			}
			flush()
			piped = true
			stage++
		case "&&", "||", ";", "&", "\n", ";;":
			flush()
			piped = false
			pipeline++
			stage = 0
		case "(":
			// "name ( )" defines a function.
			if len(cur.Words) > 0 && i+1 < len(toks) && toks[i+1].op == ")" {
				return nil, errFunction
			}
			flush()
			depth++
		case ")":
			flush()
			depth--
			if depth < 0 {
				// "case" patterns use a bare ")"; treat as unparseable.
				return nil, errParse
			}
		default:
			if i+1 >= len(toks) || toks[i+1].op != "" {
				return nil, errParse
			}
			target := toks[i+1].word
			i++
			if t.op == "<<" || t.op == "<<-" || t.op == "<<<" || t.op == "<" || t.op == "<&" && target.Text != "-" && !isDigits(target.Text) {
				cur.Stdin = true
			}
			cur.Redirects = append(cur.Redirects, redirect{Op: t.op, Target: target})
		}
	}
	flush()
	if depth != 0 {
		return nil, errParse
	}
	return out, nil
}

var errFunction = errors.New("function definition")

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// quote renders words back into a shell string for recursive checks.
func quote(words []string) string {
	parts := make([]string, len(words))
	for i, w := range words {
		if w != "" && !strings.ContainsAny(w, " \t\n'\"\\$`;&|()<>*?[]{}#~") {
			parts[i] = w
			continue
		}
		parts[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
	}
	return strings.Join(parts, " ")
}
