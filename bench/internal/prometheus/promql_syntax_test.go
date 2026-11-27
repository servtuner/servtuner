package prometheus

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// This file is a stand-in for the PromQL parser. github.com/prometheus/prometheus
// is a heavy dependency servtuner avoids, so
// the tests check what they can without it: strings terminate, (), [] and {}
// balance and nest, every range or subquery bracket holds a duration (and a
// duration step for subqueries), and the query has no leftover template text.

var (
	durationPattern = `([0-9]+(ms|s|m|h|d|w|y))+`
	rangePattern    = regexp.MustCompile(`^` + durationPattern + `$`)
	subqueryPattern = regexp.MustCompile(`^` + durationPattern + `:(` + durationPattern + `)?$`)
)

// checkPromQLShape returns a description of the first structural problem of a
// rendered query, or "" when it looks well formed.
func checkPromQLShape(query string) string {
	if strings.TrimSpace(query) == "" {
		return "empty query"
	}
	if strings.Contains(query, "<<") || strings.Contains(query, ">>") || strings.Contains(query, "<no value>") {
		return "unrendered template text"
	}
	var stack []rune
	closers := map[rune]rune{')': '(', ']': '[', '}': '{'}
	runes := []rune(query)
	bracketStart := -1
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch r {
		case '"', '\'', '`':
			end := closingQuote(runes, i)
			if end < 0 {
				return fmt.Sprintf("unterminated string starting at %d", i)
			}
			i = end
		case '(', '[', '{':
			stack = append(stack, r)
			if r == '[' {
				bracketStart = i + 1
			}
		case ')', ']', '}':
			if len(stack) == 0 || stack[len(stack)-1] != closers[r] {
				return fmt.Sprintf("unbalanced %q at %d", r, i)
			}
			stack = stack[:len(stack)-1]
			if r == ']' {
				if problem := checkBracket(string(runes[bracketStart:i])); problem != "" {
					return problem
				}
			}
		}
	}
	if len(stack) != 0 {
		return fmt.Sprintf("unclosed %q", stack[len(stack)-1])
	}
	return ""
}

func closingQuote(runes []rune, start int) int {
	quote := runes[start]
	for i := start + 1; i < len(runes); i++ {
		if runes[i] == '\\' && quote != '`' {
			i++
			continue
		}
		if runes[i] == quote {
			return i
		}
	}
	return -1
}

func checkBracket(content string) string {
	if rangePattern.MatchString(content) || subqueryPattern.MatchString(content) {
		return ""
	}
	return fmt.Sprintf("range or subquery bracket [%s] is not a duration", content)
}

var selectorBlock = regexp.MustCompile(`([A-Za-z_:][A-Za-z0-9_:]*)?\{([^}]*)\}`)

// selectorBlocks returns the metric name (possibly empty) and the matcher text of
// every vector selector in the query.
func selectorBlocks(query string) [][2]string {
	var blocks [][2]string
	for _, match := range selectorBlock.FindAllStringSubmatch(query, -1) {
		blocks = append(blocks, [2]string{match[1], match[2]})
	}
	return blocks
}

// The stand-in parser must be able to fail, or the catalog tests prove nothing.
func TestShapeCheckerCatchesBrokenQueries(t *testing.T) {
	bad := map[string]string{
		"unbalanced paren":   `sum(rate(x{a="b"}[1m])`,
		"unbalanced brace":   `x{a="b"`,
		"crossed brackets":   `sum(x[1m)]`,
		"unterminated":       `x{a="b}`,
		"template left over": `x{<<.Selector>>}`,
		"bad range":          `rate(x[1 minute])`,
		"bad subquery":       `max_over_time(x[5m:abc])`,
		"empty":              ` `,
	}
	for name, query := range bad {
		if checkPromQLShape(query) == "" {
			t.Errorf("%s: %q was accepted", name, query)
		}
	}
	good := []string{
		`max_over_time((sum by (pod) (rate(x{a="b)",c=~"d|e"}[1m])))[2m:15s])`,
		`quantile_over_time(0.95, x[1m30s])`,
		`max_over_time(x[5m:])`,
		`a / (b > 0) or c`,
	}
	for _, query := range good {
		if problem := checkPromQLShape(query); problem != "" {
			t.Errorf("%q rejected: %s", query, problem)
		}
	}
}
