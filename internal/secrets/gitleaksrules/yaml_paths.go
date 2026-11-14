package gitleaksrules

import (
	"path"
	"strings"

	"github.com/x-mesh/gk/internal/secrets"
)

// yamlKey splits a YAML mapping line into the column of its key, the key,
// and the value, which is empty for a parent key.
func yamlKey(line string) (indent int, key, value string, ok bool) {
	i := 0
	for i < len(line) && line[i] == ' ' {
		i++
	}
	for i+1 < len(line) && line[i] == '-' && line[i+1] == ' ' {
		i += 2
		for i < len(line) && line[i] == ' ' {
			i++
		}
	}
	if i == len(line) || line[i] == '#' {
		return 0, "", "", false
	}
	rest := line[i:]
	end := -1
	if q := rest[0]; q == '"' || q == '\'' {
		c := strings.IndexByte(rest[1:], q)
		if c < 0 || !strings.HasPrefix(strings.TrimLeft(rest[c+2:], " \t"), ":") {
			return 0, "", "", false
		}
		key = rest[1 : c+1]
		end = c + 2 + strings.IndexByte(rest[c+2:], ':')
	} else {
		for j := 0; j < len(rest); j++ {
			if rest[j] == '#' {
				return 0, "", "", false
			}
			if rest[j] == ':' && (j+1 == len(rest) || rest[j+1] == ' ' || rest[j+1] == '\t') {
				end = j
				break
			}
		}
		if end <= 0 {
			return 0, "", "", false
		}
		key = strings.TrimRight(rest[:end], " \t")
	}
	return i, key, strings.TrimLeft(rest[end+1:], " \t"), true
}

func isYAML(p string) bool {
	inner := p[strings.LastIndex(p, secrets.ArchiveSeparator)+1:]
	ext := strings.ToLower(path.Ext(inner))
	return ext == ".yml" || ext == ".yaml"
}

// yamlPaths rewrites each line of a YAML file whose key holds a pass word
// with the keys of its parents in front, so `password:` under
// `spring: datasource:` reads `spring.datasource.password:`. A service
// name is often only in a parent key, where the gk rules do not see it.
// The lines keep their line numbers. A hunk of a patch starts at its first
// added line, so a parent above the hunk is not known.
func yamlPaths(f fileDiff) fileDiff {
	out := fileDiff{path: f.path}
	type parent struct {
		indent int
		key    string
	}
	for _, h := range f.hunks {
		if !strings.Contains(h.lower, "pass") && !strings.Contains(h.lower, "pwd") {
			continue
		}
		var flat hunk
		var parents []parent
		for i, line := range h.lines {
			indent, key, value, ok := yamlKey(line)
			if !ok {
				continue
			}
			for len(parents) > 0 && parents[len(parents)-1].indent >= indent {
				parents = parents[:len(parents)-1]
			}
			if opensBlock(value) {
				parents = append(parents, parent{indent, key})
				continue
			}
			lower := strings.ToLower(key)
			if len(parents) == 0 || !strings.Contains(lower, "pass") && !strings.Contains(lower, "pwd") {
				continue
			}
			var b strings.Builder
			for _, p := range parents {
				b.WriteString(p.key)
				b.WriteByte('.')
			}
			b.WriteString(key)
			b.WriteString(": ")
			b.WriteString(value)
			flat.lines = append(flat.lines, b.String())
			flat.numbers = append(flat.numbers, h.numbers[i])
		}
		if len(flat.lines) > 0 {
			flat.joined = strings.Join(flat.lines, "\n")
			flat.lower = strings.ToLower(flat.joined)
			out.hunks = append(out.hunks, flat)
		}
	}
	return out
}

// opensBlock reports whether the value of a key leaves the key a parent:
// nothing, a comment, or only an anchor (&name) and tags (!!map).
func opensBlock(value string) bool {
	if i := strings.Index(value, " #"); i >= 0 {
		value = value[:i]
	}
	if strings.HasPrefix(value, "#") {
		return true
	}
	for _, f := range strings.Fields(value) {
		if f[0] != '&' && f[0] != '!' {
			return false
		}
	}
	return true
}

// withYAMLPaths adds the hits of the gk rules on the key paths of a YAML
// file. The gitleaks rules do not see these lines, so their findings stay
// as gitleaks reports them. A hit that the plain line already gave is not
// added twice.
func (e *engine) withYAMLPaths(f fileDiff, hs []hit) []hit {
	if e.gkRules == 0 || !isYAML(f.path) {
		return hs
	}
	flat := yamlPaths(f)
	if len(flat.hunks) == 0 {
		return hs
	}
	for _, h := range e.scanRaw(flat, e.rules[len(e.rules)-e.gkRules:], e.rawPasses(flat)) {
		dup := false
		for _, o := range hs {
			if o.Kind == h.Kind && o.FileLine == h.FileLine && o.secret == h.secret {
				dup = true
				break
			}
		}
		if !dup {
			hs = append(hs, h)
		}
	}
	return hs
}
