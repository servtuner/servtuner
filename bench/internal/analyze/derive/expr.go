// Package derive evaluates the derivation graphs behind every recommendation
// and plan. A derived value is a formula over other
// nodes; evaluating it yields the value and a rendered string with the inputs
// substituted, so the explanation the UI shows is the computation itself.
//
// The rendered form parses back: "⌈max(185, 190) × 1.1⌉" is accepted by Parse
// and evaluates to the same value, which is how tests prove a rendered string
// cannot drift from its value.
package derive

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Expr is a parsed formula.
type Expr interface {
	eval(env func(string) (float64, bool)) (float64, error)
	render(env func(string) (float64, bool)) string
	identifiers(into map[string]bool)
}

type number float64

type identifier string

type unary struct {
	operand Expr
}

type binary struct {
	op          rune // + - * /
	left, right Expr
}

type call struct {
	name string
	args []Expr
}

// Parse reads a formula. Besides ASCII (+ - * / and function calls) it reads
// the rendered forms × ÷ ⌈x⌉ ⌊x⌋ and √(x).
func Parse(text string) (Expr, error) {
	p := &parser{lexer: lexer{text: text}}
	p.advance()
	expr, err := p.expression(0)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", text, err)
	}
	if p.token.kind != tokenEnd {
		return nil, fmt.Errorf("parse %q: unexpected %q at %d", text, p.token.text, p.token.position)
	}
	return expr, nil
}

// Identifiers lists the node ids a formula reads.
func Identifiers(expr Expr) []string {
	found := map[string]bool{}
	expr.identifiers(found)
	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}

// Eval evaluates a formula; env returns a node's value.
func Eval(expr Expr, env func(string) (float64, bool)) (float64, error) {
	value, err := expr.eval(env)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, errors.New("the result is not a finite number")
	}
	return value, nil
}

// Render writes a formula with every identifier replaced by its value.
func Render(expr Expr, env func(string) (float64, bool)) string {
	return expr.render(env)
}

// --- evaluation and rendering ----------------------------------------------

func (n number) eval(func(string) (float64, bool)) (float64, error) { return float64(n), nil }
func (n number) render(func(string) (float64, bool)) string         { return FormatNumber(float64(n)) }
func (number) identifiers(map[string]bool)                          {}

func (i identifier) eval(env func(string) (float64, bool)) (float64, error) {
	value, ok := env(string(i))
	if !ok {
		return 0, fmt.Errorf("unknown input %q", string(i))
	}
	return value, nil
}

func (i identifier) render(env func(string) (float64, bool)) string {
	if value, ok := env(string(i)); ok {
		return FormatNumber(value)
	}
	return string(i)
}

func (i identifier) identifiers(into map[string]bool) { into[string(i)] = true }

func (u unary) eval(env func(string) (float64, bool)) (float64, error) {
	value, err := u.operand.eval(env)
	return -value, err
}

func (u unary) render(env func(string) (float64, bool)) string {
	return "-" + wrap(u.operand, precedenceUnary, env)
}

func (u unary) identifiers(into map[string]bool) { u.operand.identifiers(into) }

func (b binary) eval(env func(string) (float64, bool)) (float64, error) {
	left, err := b.left.eval(env)
	if err != nil {
		return 0, err
	}
	right, err := b.right.eval(env)
	if err != nil {
		return 0, err
	}
	switch b.op {
	case '+':
		return left + right, nil
	case '-':
		return left - right, nil
	case '*':
		return left * right, nil
	default:
		if right == 0 {
			return 0, errors.New("division by zero")
		}
		return left / right, nil
	}
}

var renderedOperator = map[rune]string{'+': " + ", '-': " − ", '*': " × ", '/': " ÷ "}

func (b binary) render(env func(string) (float64, bool)) string {
	precedence := binaryPrecedence[b.op]
	// The right operand of - and / needs parentheses at equal precedence:
	// a − (b − c) is not a − b − c.
	return wrap(b.left, precedence, env) + renderedOperator[b.op] + wrap(b.right, precedence+1, env)
}

func (b binary) identifiers(into map[string]bool) {
	b.left.identifiers(into)
	b.right.identifiers(into)
}

func (c call) eval(env func(string) (float64, bool)) (float64, error) {
	fn, ok := functions[c.name]
	if !ok {
		return 0, fmt.Errorf("unknown function %q", c.name)
	}
	if fn.arity >= 0 && len(c.args) != fn.arity {
		return 0, fmt.Errorf("%s takes %d arguments, got %d", c.name, fn.arity, len(c.args))
	}
	if fn.arity < 0 && len(c.args) == 0 {
		return 0, fmt.Errorf("%s needs at least one argument", c.name)
	}
	values := make([]float64, len(c.args))
	for i, arg := range c.args {
		value, err := arg.eval(env)
		if err != nil {
			return 0, err
		}
		values[i] = value
	}
	return fn.apply(values)
}

func (c call) render(env func(string) (float64, bool)) string {
	args := make([]string, len(c.args))
	for i, arg := range c.args {
		args[i] = arg.render(env)
	}
	switch c.name {
	case "ceil":
		return "⌈" + args[0] + "⌉"
	case "floor":
		return "⌊" + args[0] + "⌋"
	case "sqrt":
		return "√(" + args[0] + ")"
	}
	return c.name + "(" + strings.Join(args, ", ") + ")"
}

func (c call) identifiers(into map[string]bool) {
	for _, arg := range c.args {
		arg.identifiers(into)
	}
}

func wrap(expr Expr, minimum int, env func(string) (float64, bool)) string {
	text := expr.render(env)
	if b, ok := expr.(binary); ok && binaryPrecedence[b.op] < minimum {
		return "(" + text + ")"
	}
	if _, ok := expr.(unary); ok && minimum > precedenceUnary {
		return "(" + text + ")"
	}
	return text
}

// FormatNumber writes a value the way rendered formulas show it: integers
// plainly, others with up to six significant digits and no exponent.
func FormatNumber(value float64) string {
	if value == 0 {
		return "0" // and never "-0"
	}
	if value == math.Trunc(value) && math.Abs(value) < 1e15 {
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	text := strconv.FormatFloat(value, 'f', significantDecimals(value), 64)
	if !strings.Contains(text, ".") {
		return text // trailing zeros of a whole number are digits, not padding
	}
	text = strings.TrimRight(text, "0")
	return strings.TrimSuffix(text, ".")
}

// significantDecimals keeps six significant digits after the leading zeros.
func significantDecimals(value float64) int {
	magnitude := math.Floor(math.Log10(math.Abs(value)))
	return max(0, 5-int(magnitude))
}

// --- parsing ----------------------------------------------------------------

const (
	precedenceAdditive       = 1
	precedenceMultiplicative = 2
	precedenceUnary          = 3
)

var binaryPrecedence = map[rune]int{'+': precedenceAdditive, '-': precedenceAdditive, '*': precedenceMultiplicative, '/': precedenceMultiplicative}

type tokenKind int

const (
	tokenEnd tokenKind = iota
	tokenNumber
	tokenIdentifier
	tokenOperator
	tokenOpen
	tokenClose
	tokenComma
	tokenCeilOpen
	tokenCeilClose
	tokenFloorOpen
	tokenFloorClose
	tokenSqrt
)

type token struct {
	kind     tokenKind
	text     string
	position int
	operator rune
}

type lexer struct {
	text     string
	position int
}

// operatorAliases maps the rendered operators onto ASCII.
var operatorAliases = map[rune]rune{'+': '+', '-': '-', '−': '-', '*': '*', '×': '*', '/': '/', '÷': '/'}

var punctuation = map[rune]tokenKind{
	'(': tokenOpen, ')': tokenClose, ',': tokenComma,
	'⌈': tokenCeilOpen, '⌉': tokenCeilClose, '⌊': tokenFloorOpen, '⌋': tokenFloorClose, '√': tokenSqrt,
}

func (l *lexer) next() (token, error) {
	for l.position < len(l.text) {
		r, size := utf8.DecodeRuneInString(l.text[l.position:])
		if !unicode.IsSpace(r) {
			break
		}
		l.position += size
	}
	start := l.position
	if start >= len(l.text) {
		return token{kind: tokenEnd, position: start}, nil
	}
	r, size := utf8.DecodeRuneInString(l.text[start:])
	if op, ok := operatorAliases[r]; ok {
		l.position += size
		return token{kind: tokenOperator, text: string(r), position: start, operator: op}, nil
	}
	if kind, ok := punctuation[r]; ok {
		l.position += size
		return token{kind: kind, text: string(r), position: start}, nil
	}
	if unicode.IsDigit(r) || r == '.' {
		end := start
		for end < len(l.text) && (isDigitByte(l.text[end]) || l.text[end] == '.') {
			end++
		}
		l.position = end
		return token{kind: tokenNumber, text: l.text[start:end], position: start}, nil
	}
	if unicode.IsLetter(r) || r == '_' {
		end := start
		for end < len(l.text) {
			next, width := utf8.DecodeRuneInString(l.text[end:])
			if !unicode.IsLetter(next) && !unicode.IsDigit(next) && next != '_' && next != '.' {
				break
			}
			end += width
		}
		l.position = end
		return token{kind: tokenIdentifier, text: l.text[start:end], position: start}, nil
	}
	return token{}, fmt.Errorf("unexpected %q at %d", r, start)
}

func isDigitByte(b byte) bool { return b >= '0' && b <= '9' }

type parser struct {
	lexer lexer
	token token
	err   error
}

func (p *parser) advance() {
	if p.err != nil {
		return
	}
	p.token, p.err = p.lexer.next()
}

func (p *parser) expect(kind tokenKind, what string) error {
	if p.err != nil {
		return p.err
	}
	if p.token.kind != kind {
		return fmt.Errorf("expected %s at %d, found %q", what, p.token.position, p.token.text)
	}
	p.advance()
	return p.err
}

// expression parses operators of at least the given precedence (precedence
// climbing); all binary operators are left-associative.
func (p *parser) expression(minimum int) (Expr, error) {
	left, err := p.primary()
	if err != nil {
		return nil, err
	}
	for p.err == nil && p.token.kind == tokenOperator && binaryPrecedence[p.token.operator] > minimum {
		op := p.token.operator
		p.advance()
		right, err := p.expression(binaryPrecedence[op])
		if err != nil {
			return nil, err
		}
		left = binary{op: op, left: left, right: right}
	}
	return left, p.err
}

func (p *parser) primary() (Expr, error) {
	if p.err != nil {
		return nil, p.err
	}
	current := p.token
	switch current.kind {
	case tokenNumber:
		p.advance()
		value, err := strconv.ParseFloat(current.text, 64)
		if err != nil {
			return nil, fmt.Errorf("bad number %q", current.text)
		}
		return number(value), nil
	case tokenOperator:
		if current.operator != '-' {
			break
		}
		p.advance()
		operand, err := p.expression(precedenceUnary - 1)
		if err != nil {
			return nil, err
		}
		return unary{operand: operand}, nil
	case tokenOpen:
		p.advance()
		inner, err := p.expression(0)
		if err != nil {
			return nil, err
		}
		return inner, p.expect(tokenClose, "')'")
	case tokenCeilOpen, tokenFloorOpen:
		name, closing := "ceil", tokenCeilClose
		if current.kind == tokenFloorOpen {
			name, closing = "floor", tokenFloorClose
		}
		p.advance()
		inner, err := p.expression(0)
		if err != nil {
			return nil, err
		}
		return call{name: name, args: []Expr{inner}}, p.expect(closing, "a closing bracket")
	case tokenSqrt:
		p.advance()
		if err := p.expect(tokenOpen, "'(' after √"); err != nil {
			return nil, err
		}
		inner, err := p.expression(0)
		if err != nil {
			return nil, err
		}
		return call{name: "sqrt", args: []Expr{inner}}, p.expect(tokenClose, "')'")
	case tokenIdentifier:
		p.advance()
		if p.token.kind != tokenOpen {
			return identifier(current.text), p.err
		}
		if _, known := functions[current.text]; !known {
			return nil, fmt.Errorf("unknown function %q", current.text)
		}
		p.advance()
		var args []Expr
		for p.err == nil && p.token.kind != tokenClose {
			arg, err := p.expression(0)
			if err != nil {
				return nil, err
			}
			args = append(args, arg)
			if p.token.kind != tokenComma {
				break
			}
			p.advance()
		}
		return call{name: current.text, args: args}, p.expect(tokenClose, "')'")
	}
	return nil, fmt.Errorf("unexpected %q at %d", current.text, current.position)
}
