// Package codecity builds a Wettel-style CodeCity model of a source tree:
// directories become districts, files become buildings, and software metrics
// (methods, fields, lines of code) drive height, footprint, and color.
//
// It is deterministic and needs no LLM or agent. Metrics are regex heuristics,
// deliberately conservative: a missed method is preferred over an invented one.
package codecity

import (
	"path"
	"regexp"
	"strings"
)

var sourceExts = map[string]string{
	".py": "python", ".pyi": "python",
	".js": "javascript", ".jsx": "javascript", ".mjs": "javascript", ".cjs": "javascript",
	".ts": "typescript", ".tsx": "typescript",
	".java": "java", ".kt": "kotlin", ".kts": "kotlin",
	".go": "go", ".rs": "rust", ".rb": "ruby", ".php": "php",
	".cs": "csharp", ".swift": "swift", ".scala": "scala",
	".c": "c", ".h": "c", ".cc": "cpp", ".cpp": "cpp", ".cxx": "cpp", ".hpp": "cpp",
	".m": "objc", ".mm": "objcpp",
	".vue": "vue", ".svelte": "svelte", ".dart": "dart",
	".ex": "elixir", ".exs": "elixir", ".erl": "erlang",
	".hs": "haskell", ".lua": "lua", ".r": "r", ".jl": "julia",
	".groovy": "groovy", ".zig": "zig", ".nim": "nim",
	".ml": "ocaml", ".mli": "ocaml", ".fs": "fsharp", ".fsx": "fsharp",
	".sol": "solidity", ".clj": "clojure", ".cljs": "clojure",
}

// Language returns the language for a file name, or "" when it is not source.
func Language(name string) string {
	return sourceExts[strings.ToLower(path.Ext(name))]
}

func re(s string) *regexp.Regexp { return regexp.MustCompile(s) }

var funcRes = map[string]*regexp.Regexp{
	"python": re(`(?m)^[ \t]*(?:async[ \t]+)?def[ \t]+(\w+)[ \t]*\(`),
	"javascript": re(`(?m)(?:(?:export[ \t]+)?(?:async[ \t]+)?function[ \t]+\*?[ \t]*(\w+)` +
		`|(?:(?:public|private|protected|static|async)[ \t]+)+(\w+)[ \t]*\(` +
		`|(\w+)[ \t]*\([^)]*\)[ \t]*\{` +
		`|(?:const|let|var)[ \t]+(\w+)[ \t]*=[ \t]*(?:async[ \t]+)?\([^)]*\)[ \t]*=>)`),
	"typescript": re(`(?m)(?:(?:export[ \t]+)?(?:async[ \t]+)?function[ \t]+\*?[ \t]*(\w+)` +
		`|(?:(?:public|private|protected|static|async|override)[ \t]+)+(\w+)[ \t]*\(` +
		`|(?:const|let)[ \t]+(\w+)[ \t]*=[ \t]*(?:async[ \t]+)?(?:\([^)]*\)|\w+)[ \t]*=>)`),
	"java": re(`(?m)^[ \t]*(?:(?:public|private|protected|static|final|synchronized|native|abstract|default)[ \t]+)*` +
		`[\w.<>,\[\]]+[ \t]+(\w+)[ \t]*\([^;]*\)[ \t]*(?:throws[ \t]+[\w., \t]+)?[ \t]*(?:\{|;)`),
	"kotlin": re(`(?m)\b(?:fun|constructor)[ \t]+(?:<\w+>[ \t]+)?(\w+)[ \t]*\(`),
	"go":     re(`(?m)^func[ \t]+(?:\([^)]+\)[ \t]+)?(\w+)[ \t]*\(`),
	"rust":   re(`(?m)^[ \t]*(?:pub(?:\([^)]+\))?[ \t]+)?(?:async[ \t]+)?fn[ \t]+(\w+)`),
	"ruby":   re(`(?m)^[ \t]*def[ \t]+(?:self\.)?(\w+[?=!]?)`),
	"php":    re(`(?m)\bfunction[ \t]+(\w+)[ \t]*\(`),
	"csharp": re(`(?m)^[ \t]*(?:(?:public|private|protected|internal|static|async|override|virtual|sealed)[ \t]+)+` +
		`[\w.<>,\[\]]+[ \t]+(\w+)[ \t]*\(`),
	"swift":   re(`(?m)\bfunc[ \t]+(\w+)[ \t]*\(`),
	"scala":   re(`(?m)\bdef[ \t]+(\w+)`),
	"cpp":     re(`(?m)^[ \t]*(?:[\w:~<>\*&]+)[ \t]+(\w+)[ \t]*\([^;]*\)[ \t]*(?:const)?[ \t]*\{`),
	"c":       re(`(?m)^[ \t]*[\w\s\*]+\s+(\w+)[ \t]*\([^;]*\)[ \t]*\{`),
	"dart":    re(`(?m)^[ \t]*(?:[\w<>\?]+)[ \t]+(\w+)[ \t]*\([^;]*\)[ \t]*\{`),
	"elixir":  re(`(?m)^[ \t]*def(?:p)?[ \t]+(\w+)`),
	"haskell": re(`(?m)^(\w+)[ \t]+::`),
	"lua":     re(`(?m)\bfunction[ \t]+([\w.:]+)`),
}

var classRes = map[string]*regexp.Regexp{
	"python":     re(`(?m)^[ \t]*class[ \t]+(\w+)`),
	"javascript": re(`(?m)\bclass[ \t]+(\w+)`),
	"typescript": re(`(?m)\b(?:export[ \t]+)?(?:abstract[ \t]+)?(?:class|interface|enum|type)[ \t]+(\w+)`),
	"java":       re(`(?m)\b(?:class|interface|enum|record)[ \t]+(\w+)`),
	"kotlin":     re(`(?m)\b(?:class|interface|object|enum class|data class)[ \t]+(\w+)`),
	"go":         re(`(?m)^type[ \t]+(\w+)[ \t]+struct`),
	"rust":       re(`(?m)\b(?:pub[ \t]+)?(?:struct|enum|trait|impl)[ \t]+(?:<[^>]+>[ \t]+)?(\w+)`),
	"ruby":       re(`(?m)^[ \t]*(?:class|module)[ \t]+(\w+)`),
	"php":        re(`(?m)\b(?:class|interface|trait|enum)[ \t]+(\w+)`),
	"csharp":     re(`(?m)\b(?:class|interface|struct|record|enum)[ \t]+(\w+)`),
	"swift":      re(`(?m)\b(?:class|struct|enum|protocol|actor)[ \t]+(\w+)`),
	"scala":      re(`(?m)\b(?:class|object|trait|enum)[ \t]+(\w+)`),
	"cpp":        re(`(?m)\b(?:class|struct)[ \t]+(\w+)`),
	"c":          re(`(?m)\b(?:struct|enum)[ \t]+(\w+)`),
	"dart":       re(`(?m)\b(?:class|mixin|enum|extension)[ \t]+(\w+)`),
}

var fieldRes = map[string]*regexp.Regexp{
	"python":     re(`self\.(\w+)[ \t]*=`),
	"javascript": re(`(?:this\.(\w+)|(?:public|private|protected)?[ \t]*(\w+)[ \t]*[;=])`),
	"typescript": re(`(?:(?:public|private|protected|readonly|static)[ \t]+)+(\w+)[ \t]*[?:]|this\.(\w+)[ \t]*=`),
	"java": re(`(?m)^[ \t]*(?:(?:public|private|protected|static|final|volatile|transient)[ \t]+)+` +
		`[\w.<>,\[\]]+[ \t]+(\w+)[ \t]*[;=]`),
	"kotlin": re(`(?m)\b(?:val|var)[ \t]+(\w+)`),
	"go":     re(`(?m)^[ \t]*(\w+)[ \t]+[\w\*\[\]]+`),
	"rust":   re(`(?m)^[ \t]*(?:pub(?:\([^)]+\))?[ \t]+)?(\w+)[ \t]*:`),
	"ruby":   re(`(?:@(\w+)|attr_(?:reader|writer|accessor)[ \t]+[:\w, ]+)`),
	"php":    re(`\b(?:public|private|protected)[ \t]+(?:static[ \t]+)?(?:\?\w+[ \t]+)?\$(\w+)`),
	"csharp": re(`(?m)^[ \t]*(?:(?:public|private|protected|internal|static|readonly)[ \t]+)+` +
		`[\w.<>,\[\]]+[ \t]+(\w+)[ \t]*[{;=]`),
	"swift": re(`(?m)\b(?:let|var)[ \t]+(\w+)`),
	"scala": re(`(?m)\b(?:val|var)[ \t]+(\w+)`),
	"cpp":   re(`(?m)^[ \t]*(?:[\w:<>\*&]+)[ \t]+(\w+)[ \t]*;`),
	"dart":  re(`(?m)^[ \t]*(?:final|const|late)?[ \t]*[\w<>\?]+[ \t]+(\w+)[ \t]*[;=]`),
}

var (
	// RE2 has no \b around non-word tokens like && and ||, so match them separately.
	complexityWordRe = re(`\b(?:if|else if|elif|for|while|case|catch|except|when|match|guard)\b`)
	complexityOpRe   = re(`&&|\|\||\?`)
	commentLineRe    = re(`^[ \t]*(?://|#|--|/\*|\*|<!--)`)

	blockCommentRe = re(`(?s)/\*.*?\*/`)
	lineCommentRe  = re(`(?m)//.*$`)
	hashCommentRe  = re(`(?m)#.*$`)
	tripleStrRe    = re(`(?s)"""[\s\S]*?"""|'''[\s\S]*?'''`)
	backtickRe     = re("`(?:\\\\.|[^`\\\\])*`")
	dqStrRe        = re(`"(?:\\.|[^"\\])*"`)
	sqStrRe        = re(`'(?:\\.|[^'\\])*'`)
)

var notNames = map[string]bool{"if": true, "for": true, "while": true, "switch": true, "catch": true, "return": true, "new": true}

// stripNoise removes comments and string bodies so regex counts stay sane.
func stripNoise(text, lang string) string {
	switch lang {
	case "python", "ruby", "elixir", "r":
		text = hashCommentRe.ReplaceAllString(text, "")
	default:
		text = blockCommentRe.ReplaceAllString(text, "")
		text = lineCommentRe.ReplaceAllString(text, "")
	}
	text = tripleStrRe.ReplaceAllString(text, "")
	text = backtickRe.ReplaceAllString(text, "")
	text = dqStrRe.ReplaceAllString(text, `""`)
	text = sqStrRe.ReplaceAllString(text, "''")
	return text
}

func uniqueMatches(p *regexp.Regexp, text string) []string {
	if p == nil {
		return nil
	}
	var names []string
	seen := map[string]bool{}
	for _, m := range p.FindAllStringSubmatch(text, -1) {
		name := ""
		for _, g := range m[1:] {
			if g != "" {
				name = g
				break
			}
		}
		if name == "" || seen[name] || notNames[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

func complexity(text string) int {
	return len(complexityWordRe.FindAllString(text, -1)) + len(complexityOpRe.FindAllString(text, -1))
}

type classSpan struct {
	Name       string
	NOM, NOA   int
	LOC        int
	Complexity int
}

// fileMetrics are the raw measurements for one source file.
type fileMetrics struct {
	LOC, NCLOC, NOM, NOA, Classes, Complexity int
	ClassNames                                []string
	Spans                                     []classSpan
}

func measure(text, lang string) fileMetrics {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if text == "" {
		lines = nil
	}
	loc, blank, comments := len(lines), 0, 0
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			blank++
		} else if commentLineRe.MatchString(ln) {
			comments++
		}
	}
	ncloc := max(0, loc-blank-comments)
	cleaned := stripNoise(text, lang)
	classes := uniqueMatches(classRes[lang], cleaned)
	funcs := uniqueMatches(funcRes[lang], cleaned)
	fields := uniqueMatches(fieldRes[lang], cleaned)

	m := fileMetrics{
		LOC: loc, NCLOC: ncloc, NOA: len(fields), Classes: len(classes),
		Complexity: complexity(cleaned), ClassNames: classes,
	}
	m.NOM = len(funcs)
	if m.NOM == 0 && ncloc > 0 {
		m.NOM = 1
	}
	if cre := classRes[lang]; cre != nil {
		pos := cre.FindAllStringSubmatchIndex(text, -1)
		for i, p := range pos {
			end := len(text)
			if i+1 < len(pos) {
				end = pos[i+1][0]
			}
			chunk := text[p[0]:end]
			cc := stripNoise(chunk, lang)
			m.Spans = append(m.Spans, classSpan{
				Name:       text[p[2]:p[3]],
				NOM:        max(1, len(uniqueMatches(funcRes[lang], cc))),
				NOA:        len(uniqueMatches(fieldRes[lang], cc)),
				LOC:        strings.Count(chunk, "\n") + 1,
				Complexity: complexity(cc),
			})
		}
	}
	return m
}
