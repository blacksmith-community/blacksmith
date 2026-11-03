// Package graft provides the Parser for operator expressions.
package graft

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/fivetwenty-io/graft/pkg/graft/interfaces"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// Note: Precedence constants and Associativity are defined in operator_registry.go

// tokenStream is the minimal tokenizer surface Parser.tokenize consumes. It
// exists so tests can substitute a stub that simulates a non-advancing
// scanner arm without constructing pathological AdvancedTokenizer input.
type tokenStream interface {
	HasMore() bool
	NextToken() *interfaces.Token
	Position() int
}

// Parser parses operator expressions.
type Parser struct {
	tokenizer tokenStream
	tokens    []*interfaces.Token
	pos       int
	input     string
	phase     OperatorPhase

	// opcallPos is the token index at which a bare identifier may open an
	// operator call — the "primary operator position". It replaces the
	// literal constant 1 that parseIdentifierOrOperator used to compare
	// against. ParseOpcall sets it once to 1 (the first token after "((").
	// parseParenthesized and parseNestedOperator each save and restore it
	// around their own inner parse, so every parenthesized group — however
	// deeply nested — gets its own marker.
	opcallPos int

	// nestingDepth counts currently-open "(" / "((" groups. parseParenthesized
	// and parseNestedOperator increment it on entry and decrement it on exit;
	// exceeding maxNestingDepth is a hard parse error.
	nestingDepth int

	// engine, when non-nil, is consulted before DefaultRegistry for every
	// operator-name lookup this parser makes (operatorFor). It is set once
	// at construction (NewParserForEngine) and never reassigned — nested
	// parses reuse the same Parser instance rather than constructing a new
	// one, so the engine threads through recursion for free.
	engine Engine
}

// maxNestingDepth bounds "(" / "((" nesting depth.
const maxNestingDepth = 64

// enterNesting increments the parser's paren-nesting counter and reports an
// error if it now exceeds maxNestingDepth. Callers that succeed must defer
// exitNesting to restore the counter on every return path.
func (p *Parser) enterNesting() error {
	p.nestingDepth++
	if p.nestingDepth > maxNestingDepth {
		return fmt.Errorf("expression nesting too deep (max %d)", maxNestingDepth)
	}
	return nil
}

// exitNesting decrements the parser's paren-nesting counter.
func (p *Parser) exitNesting() {
	p.nestingDepth--
}

// tokenAt returns the token at absolute index idx without moving p.pos, or a
// TokenEOF sentinel if that index falls outside the token stream.
func (p *Parser) tokenAt(idx int) interfaces.Token {
	if idx < 0 || idx >= len(p.tokens) {
		return interfaces.Token{Type: interfaces.TokenEOF}
	}
	return *p.tokens[idx]
}

// NewParser creates a new parser for the given input.
func NewParser(input string, phase OperatorPhase) *Parser {
	opts := interfaces.TokenizerOptions{
		RecognizeReferencePaths: true,
		AllowEnvironmentVars:    true,
		TrackPositions:          true,
		AllowUnicode:            true,
	}
	return &Parser{
		tokenizer: interfaces.NewAdvancedTokenizer(input, opts),
		input:     input,
		phase:     phase,
	}
}

// NewParserForEngine creates a new Parser whose operator-name lookups
// (operatorFor) prefer the given engine's local registry over
// DefaultRegistry. A nil engine behaves exactly like NewParser.
func NewParserForEngine(input string, phase OperatorPhase, e Engine) *Parser {
	p := NewParser(input, phase)
	p.engine = e
	return p
}

// operatorFor resolves an operator name through this parser's engine (if
// any), falling back to the process-global DefaultRegistry.
func (p *Parser) operatorFor(name string) Operator {
	return OperatorForEngine(p.engine, name)
}

// tokenize converts input to tokens.
//
// A progress assertion guards against a tokenizer scanner arm that returns a
// token without consuming any input: without it, Parser.tokenize would loop
// forever appending identical tokens (this happened for lone `=`, `&`, `|`,
// and `$` before that bug was fixed; see interfaces/tokenizer.go). Any future
// regression of the same shape now fails loudly instead of hanging.
func (p *Parser) tokenize() error {
	p.tokens = nil
	p.pos = 0

	for p.tokenizer.HasMore() {
		before := p.tokenizer.Position()
		tok := p.tokenizer.NextToken()
		p.tokens = append(p.tokens, tok)
		if tok.Type == interfaces.TokenEOF {
			break
		}
		if p.tokenizer.Position() <= before {
			return fmt.Errorf("tokenizer made no progress at offset %d", before)
		}
	}
	return nil
}

// current returns the current token.
func (p *Parser) current() interfaces.Token {
	if p.pos >= len(p.tokens) {
		return interfaces.Token{Type: interfaces.TokenEOF}
	}
	return *p.tokens[p.pos]
}

// advance moves to the next token.
func (p *Parser) advance() {
	if p.pos < len(p.tokens) {
		p.pos++
	}
}

// expectRightParen checks that the current token closes a parenthesized
// group and advances past it. Every place the grammar demands a specific
// token demands this one; a second such token would generalize this back
// into an expect(tokenType).
func (p *Parser) expectRightParen() error {
	if p.current().Type != interfaces.TokenRightParen {
		return fmt.Errorf("expected %v, got %v at position %d",
			interfaces.TokenRightParen, p.current().Type, p.current().Pos.Offset)
	}
	p.advance()
	return nil
}

// ParseOpcall parses a complete operator call expression (( ... )).
func (p *Parser) ParseOpcall() (*Opcall, error) {
	if err := p.tokenize(); err != nil {
		return nil, err
	}

	// Expect ((
	if p.current().Type != interfaces.TokenOperatorStart {
		return nil, fmt.Errorf("expected '((' at start of operator expression")
	}
	p.advance()
	p.opcallPos = p.pos // the token right after "((" is the primary operator position

	// Check what kind of expression this is
	expr, err := p.parseExpression()
	if err != nil {
		return nil, err
	}

	// Expect ))
	if p.current().Type != interfaces.TokenOperatorEnd {
		return nil, fmt.Errorf("expected '))' at end of operator expression, got %v", p.current().Type)
	}

	// Convert the parsed expression to an Opcall
	return p.exprToOpcall(expr)
}

// parseExpression parses an expression with precedence climbing.
func (p *Parser) parseExpression() (*Expr, error) {
	return p.parseExprWithPrecedence(PrecedenceLowest)
}

// parseExprWithPrecedence implements the precedence climbing algorithm.
func (p *Parser) parseExprWithPrecedence(minPrec Precedence) (*Expr, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}

	return p.continueExprWithPrecedence(left, minPrec)
}

// continueExprWithPrecedence runs the precedence-climbing loop starting
// from an already-parsed left operand, instead of calling parsePrimary
// itself first. parseExprWithPrecedence is the common case (left comes
// from its own parsePrimary call); parseNestedOperator's fallback is the
// other, reusing an already-parsed inner-group value as the left operand
// of whatever expression continues at the enclosing group's level.
//
//nolint:gocyclo // precedence climbing requires handling multiple operator types
func (p *Parser) continueExprWithPrecedence(left *Expr, minPrec Precedence) (*Expr, error) {
	var err error
	for {
		tok := p.current()

		// Check for end conditions
		if tok.Type == interfaces.TokenEOF ||
			tok.Type == interfaces.TokenOperatorEnd ||
			tok.Type == interfaces.TokenRightParen ||
			tok.Type == interfaces.TokenComma ||
			tok.Type == interfaces.TokenColon {
			break
		}

		// Handle ternary operator
		if tok.Type == interfaces.TokenQuestion {
			if int(PrecedenceTernary) < int(minPrec) {
				break
			}
			left, err = p.parseTernary(left)
			if err != nil {
				return nil, err
			}
			continue
		}

		// Handle logical OR (special fallback semantics)
		if tok.Type == interfaces.TokenOr {
			if int(PrecedenceLogicalOr) < int(minPrec) {
				break
			}
			left, err = p.parseLogicalOr(left)
			if err != nil {
				return nil, err
			}
			continue
		}

		// Handle binary operators
		prec, assoc, exprType := p.getBinaryOpInfo(tok.Type)
		if prec == PrecedenceLowest {
			// Not a binary operator
			break
		}

		if int(prec) < int(minPrec) {
			break
		}

		p.advance() // consume operator

		// Calculate next precedence for right associativity
		nextMinPrec := prec
		if assoc == LeftAssociative {
			nextMinPrec++
		}

		right, err := p.parseOperand(nextMinPrec)
		if err != nil {
			return nil, err
		}

		left = &Expr{
			Type:  exprType,
			Left:  left,
			Right: right,
		}
	}

	return left, nil
}

// parseOperand parses an operand of the general expression grammar — a
// binary operator's right side, a ternary branch, a logical-OR fallback, or
// a unary operand — at the given minimum precedence. If the operand opens an
// operator call under identifierOpensOpcallAt's two-token rule, that name
// claims the call-opening position right there, exactly like the first token
// after "((": `(( grab a && grab b ))` must evaluate both grabs, not degrade
// the second one to a bare reference.
//
// Everything else is left untouched: opcallPos is only advanced when the
// two-token rule fires, so the A6 lookahead rule for unregistered names —
// scoped to a group's own first token — keeps governing every other operand
// exactly as it does today (e.g. plain "(( a && b ))" still resolves both as
// references), and a document key that merely shares a registered operator's
// name stays a reference in operand position (`(( left == type ))` with a
// "type:" key).
func (p *Parser) parseOperand(minPrec Precedence) (*Expr, error) {
	return p.withOpcallEligibility(func() (*Expr, error) {
		return p.parseExprWithPrecedence(minPrec)
	})
}

// withOpcallEligibility runs parseFn with p.opcallPos temporarily set to
// p.pos when the token there satisfies identifierOpensOpcallAt's
// two-token rule, restoring the prior value once parseFn returns.
// parseOperand (a general operand — a binary operator's right side, a
// ternary branch, a unary operand) and parseOperatorCall's own "||"
// right-hand side (a fallback value inside a bare operator call's
// space-separated argument list, e.g. the "grab fallback" in
// "(( grab (concat ...) || grab fallback ))") both need this exact
// eligibility check and override: without it, an operator identifier at
// either position falls through to being parsed as a bare reference
// instead of opening its own call, no matter how many arguments it
// itself takes — the two-token rule is what makes `(( grab a && grab b
// ))` evaluate both grabs, and skipping it for the second call site left
// a bare (non-"@target") operator fallback broken both at the top level
// and, doubly so, when nested inside another call's own argument.
func (p *Parser) withOpcallEligibility(parseFn func() (*Expr, error)) (*Expr, error) {
	if p.identifierOpensOpcallAt(p.pos) {
		saved := p.opcallPos
		p.opcallPos = p.pos
		expr, err := parseFn()
		p.opcallPos = saved
		return expr, err
	}
	return parseFn()
}

// identifierOpensOpcallAt reports whether the token at index idx opens an
// operator call rather than naming a plain reference, using the spec's
// two-token rule (cluster A2 §2.3–§2.4). Both conditions must hold:
//
//  1. the token is an IDENTIFIER whose name resolves through OperatorFor to
//     a non-NullOperator; and
//  2. the token after it can actually begin an argument — it is not ")",
//     "))", ",", ":", ".", EOF, or any infix operator token (isBinaryOperator
//     already covers "?").
//
// Condition 2 is what keeps a document key that happens to share an
// operator's name — "type", "sort", "keys", "empty" are all ordinary
// manifest keys — resolving as a reference wherever it sits at the end of an
// expression, a ternary branch, or a unary operand. Without it, `(( left ==
// type ))` would parse "type" as a zero-argument (( type )) call and fail.
func (p *Parser) identifierOpensOpcallAt(idx int) bool {
	if idx < 0 || idx >= len(p.tokens) {
		return false
	}
	tok := *p.tokens[idx]
	if tok.Type != interfaces.TokenIdentifier {
		return false
	}
	if _, isNull := p.operatorFor(tok.Literal).(NullOperator); isNull {
		return false
	}

	next := p.tokenAt(idx + 1)
	switch next.Type {
	case interfaces.TokenRightParen, interfaces.TokenOperatorEnd, interfaces.TokenComma,
		interfaces.TokenColon, interfaces.TokenDot, interfaces.TokenEOF:
		return false
	default:
		return !p.isBinaryOperator(next.Type)
	}
}

// parseTernary handles condition ? trueExpr : falseExpr.
func (p *Parser) parseTernary(condition *Expr) (*Expr, error) {
	p.advance() // consume ?

	trueExpr, err := p.parseOperand(PrecedenceOr) // Parse at higher precedence
	if err != nil {
		return nil, err
	}

	if p.current().Type != interfaces.TokenColon {
		return nil, fmt.Errorf("expected ':' in ternary expression, got %v", p.current().Type)
	}
	p.advance() // consume :

	falseExpr, err := p.parseOperand(PrecedenceTernary) // Ternary is right-associative
	if err != nil {
		return nil, err
	}

	// Return as an operator call to ?:
	return &Expr{
		Type:     OperatorCall,
		Operator: "?:",
		Call: &Opcall{
			src: p.input,
			op:  p.operatorFor("?:"),
			args: []*Expr{
				condition,
				trueExpr,
				falseExpr,
			},
		},
	}, nil
}

// parseLogicalOr handles left || right with fallback semantics.
func (p *Parser) parseLogicalOr(left *Expr) (*Expr, error) {
	p.advance() // consume ||

	right, err := p.parseOperand(PrecedenceAnd) // Next higher precedence
	if err != nil {
		return nil, err
	}

	return &Expr{
		Type:  LogicalOr,
		Left:  left,
		Right: right,
	}, nil
}

// parsePrimary parses primary expressions (literals, references, etc.)
func (p *Parser) parsePrimary() (*Expr, error) {
	tok := p.current()

	switch tok.Type {
	case interfaces.TokenInteger:
		return p.parseInteger()

	case interfaces.TokenFloat:
		return p.parseFloat()

	case interfaces.TokenString, interfaces.TokenRawString:
		return p.parseString()

	case interfaces.TokenBoolean:
		return p.parseBoolean()

	case interfaces.TokenNull:
		return p.parseNull()

	case interfaces.TokenIdentifier:
		return p.parseIdentifierOrOperator()

	case interfaces.TokenReference:
		return p.parseReference()

	case interfaces.TokenEnvironment:
		return p.parseEnvironment()

	case interfaces.TokenLeftParen:
		return p.parseParenthesized()

	case interfaces.TokenNot:
		return p.parseUnary()

	case interfaces.TokenMinus:
		// Could be unary minus or just negative number
		return p.parseUnaryMinus()

	case interfaces.TokenOperatorStart:
		// Nested operator call
		return p.parseNestedOperator()

	case interfaces.TokenAt:
		// Target reference like @something
		return p.parseTarget()

	case interfaces.TokenEOF, interfaces.TokenInvalid, interfaces.TokenOperatorEnd,
		interfaces.TokenPlus, interfaces.TokenStar, interfaces.TokenSlash, interfaces.TokenPercent,
		interfaces.TokenEqual, interfaces.TokenNotEqual, interfaces.TokenLess, interfaces.TokenGreater,
		interfaces.TokenLessEqual, interfaces.TokenGreaterEqual, interfaces.TokenAnd, interfaces.TokenOr,
		interfaces.TokenQuestion, interfaces.TokenColon, interfaces.TokenRightParen,
		interfaces.TokenLeftBracket, interfaces.TokenRightBracket, interfaces.TokenLeftBrace, interfaces.TokenRightBrace,
		interfaces.TokenComma, interfaces.TokenDot, interfaces.TokenPipe,
		interfaces.TokenIf, interfaces.TokenElif, interfaces.TokenElse, interfaces.TokenFi,
		interfaces.TokenFor, interfaces.TokenIn, interfaces.TokenDone, interfaces.TokenWhile,
		interfaces.TokenCase, interfaces.TokenWhen, interfaces.TokenDefault, interfaces.TokenEsac,
		interfaces.TokenOn, interfaces.TokenBefore, interfaces.TokenAfter, interfaces.TokenRange, interfaces.TokenOperatorName:
		return nil, fmt.Errorf("unexpected token: %v (%s) at position %d",
			tok.Type, tok.Literal, tok.Pos.Offset)
	}
	return nil, fmt.Errorf("unexpected token: %v (%s) at position %d",
		tok.Type, tok.Literal, tok.Pos.Offset)
}

// parseInteger parses an integer literal.
func (p *Parser) parseInteger() (*Expr, error) {
	tok := p.current()
	p.advance()

	val, err := strconv.ParseInt(tok.Literal, 10, 64)
	if err != nil {
		// If int64 overflows, try parsing as float64
		if floatVal, floatErr := strconv.ParseFloat(tok.Literal, 64); floatErr == nil {
			return &Expr{
				Type:    Literal,
				Literal: floatVal,
			}, nil
		}
		return nil, fmt.Errorf("invalid integer: %s", tok.Literal)
	}

	return &Expr{
		Type:    Literal,
		Literal: val,
	}, nil
}

// parseFloat parses a float literal.
func (p *Parser) parseFloat() (*Expr, error) {
	tok := p.current()
	p.advance()

	val, err := strconv.ParseFloat(tok.Literal, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid float: %s", tok.Literal)
	}

	return &Expr{
		Type:    Literal,
		Literal: val,
	}, nil
}

// parseString parses a string literal.
func (p *Parser) parseString() (*Expr, error) {
	tok := p.current()
	p.advance()

	// Remove quotes and process escape sequences
	raw := tok.Literal
	if len(raw) >= 2 {
		quote := raw[0]
		if raw[len(raw)-1] == quote {
			raw = raw[1 : len(raw)-1]
		}
	}

	// Process escape sequences
	raw = processEscapes(raw)

	return &Expr{
		Type:    Literal,
		Literal: raw,
	}, nil
}

// parseBoolean parses a boolean literal.
func (p *Parser) parseBoolean() (*Expr, error) {
	tok := p.current()
	p.advance()

	val := strings.EqualFold(tok.Literal, literalTrue)

	return &Expr{
		Type:    Literal,
		Literal: val,
	}, nil
}

// parseNull parses a null literal.
func (p *Parser) parseNull() (*Expr, error) {
	p.advance()
	return &Expr{
		Type:    Literal,
		Literal: nil,
	}, nil
}

// parseIdentifierOrOperator parses an identifier which may be an operator name.
//
//nolint:gocyclo // primary-vs-operand position and known-vs-unknown operator dispatch inherently branch a lot; see the A6 lookahead added inline for why each branch exists
func (p *Parser) parseIdentifierOrOperator() (*Expr, error) {
	tok := p.current()
	name := tok.Literal

	// Check if this is a known operator (regardless of phase - let evaluator filter by phase)
	op := p.operatorFor(name)
	_, isNullOperator := op.(NullOperator)
	isKnownOperator := !isNullOperator

	// Store current position to check if we're at the operator-call-opening
	// position — the token right after "((" (or, for a later stage, the
	// token right after a nested "(" / "((").
	isPrimaryPosition := p.pos == p.opcallPos

	if isKnownOperator {
		// It's a known operator - but only parse it as an operator call if:
		// 1. It's followed by explicit parentheses: ips(...)
		// 2. It's at the operator-call-opening position
		//
		// Otherwise, treat it as a reference. This is important because
		// identifiers like "ips" could be either a reference to a YAML key
		// or an operator call. In argument context, prefer treating as reference.

		p.advance()
		nextTok := p.current()

		// If followed by function-call parens, definitely an operator call
		if nextTok.Type == interfaces.TokenLeftParen {
			p.pos-- // back up one position
			return p.parseOperatorCall(name)
		}

		// If followed by @, it is a targeted operator call — op@target.
		// This matters in argument
		// position: "(( vault@a "x" || vault@b "x" ))" puts the second
		// targeted call on the right of ||, where the identifier would
		// otherwise fall through to the reference branch and leave the
		// "@target" tokens orphaned.
		if nextTok.Type == interfaces.TokenAt {
			p.pos-- // back up one position
			return p.parseOperatorCall(name)
		}

		// If followed by a glued ":modifier" (op:nocache), it is a modified
		// operator call — same argument-position rationale as @target
		// above: a modified call on the right of || would otherwise fall
		// through to the reference branch as a bare identifier, stranding
		// the ":modifier" tokens and failing the parse before the modifier
		// grammar (and its unknown-modifier error) is ever reached.
		// Whether the surrounding operator then does anything useful with
		// the right operand is that operator's own || semantics (grab, for
		// one, treats the operand's result as a path to resolve).
		if nextTok.Type == interfaces.TokenColon && p.modifierFollowsAt(p.pos-1) {
			p.pos-- // back up one position
			return p.parseOperatorCall(name)
		}

		// If this was the token at opcallPos, it's the primary operator.
		// Always treat the primary operator as an operator call.
		if p.pos == p.opcallPos+1 { // one past opcallPos, i.e. right after consuming the operator name
			p.pos-- // back up one position
			return p.parseOperatorCall(name)
		}

		// In argument position: only treat as operator call if followed by explicit parens
		// (which was checked above). Otherwise treat as reference.
		if nextTok.Type == interfaces.TokenDot {
			return p.parseReferencePath(name)
		}

		// Just a reference
		cursor, err := tree.ParseCursor(name)
		if err != nil {
			return nil, fmt.Errorf("invalid reference: %s", name)
		}

		return &Expr{
			Type:      Reference,
			Reference: cursor,
		}, nil
	}

	// Unknown operator (NullOperator) - at primary position, still treat as operator call
	// This allows unknown operators to be parsed and left unevaluated
	if isPrimaryPosition {
		// A bare identifier at the call-opening
		// position that names no registered operator is normally parsed as
		// an operator call — the NullOperator literal pass-through (B-1,
		// B-2). But when the token immediately following it is an infix
		// operator, "?", or ".", the identifier is in operand position
		// instead: parse it as a reference so expressions like
		// `env == "production"` and `flag && other` work. Every other
		// following token — "))", ")", ",", EOF, a literal, another
		// identifier, "(" — keeps today's behavior verbatim (§3.3): this is
		// the load-bearing backward-compat constraint that keeps `(( a ))`
		// alone passing through as literal text for defer / multi-pass
		// genesis templating.
		if p.nextTokenPlacesIdentifierInOperandPosition() {
			p.advance() // consume the identifier
			if p.current().Type == interfaces.TokenDot {
				return p.parseReferencePath(name)
			}
			cursor, err := tree.ParseCursor(name)
			if err != nil {
				return nil, fmt.Errorf("invalid reference: %s", name)
			}
			return &Expr{
				Type:      Reference,
				Reference: cursor,
			}, nil
		}

		p.advance()
		nextTok := p.current()

		// If followed by function-call parens, definitely an operator call
		if nextTok.Type == interfaces.TokenLeftParen {
			p.pos-- // back up one position
			return p.parseOperatorCall(name)
		}

		// At primary position - parse as operator call (even if unknown)
		if p.pos == p.opcallPos+1 {
			p.pos-- // back up one position
			return p.parseOperatorCall(name)
		}

		// Check for reference path
		if nextTok.Type == interfaces.TokenDot {
			return p.parseReferencePath(name)
		}

		// Treat as reference
		cursor, err := tree.ParseCursor(name)
		if err != nil {
			return nil, fmt.Errorf("invalid reference: %s", name)
		}

		return &Expr{
			Type:      Reference,
			Reference: cursor,
		}, nil
	}

	// Unknown operator not at primary position - treat as reference
	p.advance()
	if p.current().Type == interfaces.TokenDot {
		return p.parseReferencePath(name)
	}

	// Just an identifier - treat as reference
	cursor, err := tree.ParseCursor(name)
	if err != nil {
		return nil, fmt.Errorf("invalid reference: %s", name)
	}

	return &Expr{
		Type:      Reference,
		Reference: cursor,
	}, nil
}

// nextTokenPlacesIdentifierInOperandPosition reports whether the token
// immediately following the current identifier is one that places the
// identifier in operand position rather than call-opening position: an
// infix binary operator (+ - * / % == != < <= > >= && ||), "?", or "."
// It peeks one token ahead without consuming
// anything; p.pos must still be at the identifier itself when called.
func (p *Parser) nextTokenPlacesIdentifierInOperandPosition() bool {
	if p.pos+1 >= len(p.tokens) {
		return false
	}
	switch p.tokens[p.pos+1].Type {
	case interfaces.TokenPlus, interfaces.TokenMinus, interfaces.TokenStar, interfaces.TokenSlash, interfaces.TokenPercent,
		interfaces.TokenEqual, interfaces.TokenNotEqual, interfaces.TokenLess, interfaces.TokenLessEqual,
		interfaces.TokenGreater, interfaces.TokenGreaterEqual, interfaces.TokenAnd, interfaces.TokenOr,
		interfaces.TokenQuestion, interfaces.TokenDot:
		return true
	case interfaces.TokenEOF, interfaces.TokenInvalid, interfaces.TokenOperatorStart, interfaces.TokenOperatorEnd,
		interfaces.TokenInteger, interfaces.TokenFloat, interfaces.TokenString, interfaces.TokenRawString,
		interfaces.TokenBoolean, interfaces.TokenNull, interfaces.TokenIdentifier, interfaces.TokenReference, interfaces.TokenEnvironment,
		interfaces.TokenNot, interfaces.TokenColon,
		interfaces.TokenLeftParen, interfaces.TokenRightParen, interfaces.TokenLeftBracket, interfaces.TokenRightBracket,
		interfaces.TokenLeftBrace, interfaces.TokenRightBrace, interfaces.TokenComma, interfaces.TokenAt, interfaces.TokenPipe,
		interfaces.TokenIf, interfaces.TokenElif, interfaces.TokenElse, interfaces.TokenFi,
		interfaces.TokenFor, interfaces.TokenIn, interfaces.TokenDone, interfaces.TokenWhile,
		interfaces.TokenCase, interfaces.TokenWhen, interfaces.TokenDefault, interfaces.TokenEsac,
		interfaces.TokenOn, interfaces.TokenBefore, interfaces.TokenAfter, interfaces.TokenRange, interfaces.TokenOperatorName:
		return false
	}
	return false
}

// parseOperatorCall parses an operator call with arguments.
//
//nolint:gocyclo // operator call parsing supports multiple argument styles
func (p *Parser) parseOperatorCall(opName string) (*Expr, error) {
	p.advance() // consume operator name

	op := p.operatorFor(opName)

	// Check for :modifier (name:modifier, written without whitespace).
	// The grammar is name[:modifier][@target]; only "nocache" is a valid
	// modifier, and an unknown one is a loud parse error rather than a
	// silent pass-through. A colon that is not glued to both the operator
	// name and an identifier (e.g. the ternary "cond ? a : b", or a spaced
	// "grab : nocache") is left alone for the stages below, which reject
	// it exactly as they did before this syntax existed. spruce has no
	// modifier syntax and leaves any (( op:anything ... )) string as
	// unresolved literal text; graft has always hard-errored on those
	// inputs instead, so this addition changes only inputs that previously
	// failed to parse.
	noCache := false
	for p.modifierFollowsAt(p.pos - 1) {
		modifier := p.tokenAt(p.pos + 1).Literal
		if modifier != "nocache" {
			return nil, fmt.Errorf("unknown operator modifier ':%s' for %s operator (only :nocache is supported)", modifier, opName)
		}
		noCache = true
		p.advance() // consume ':'
		p.advance() // consume modifier name
	}

	// "(( calc * 2 ))" must parse identically to
	// "(( calc "* 2" ))" — op_calc.go's leading-operator branch already
	// handles the string form; the parser's ordinary argument loop can
	// never produce it, because it breaks on the first binary operator
	// token. Scoped to "calc" alone.
	if opName == "calc" {
		if expr, ok, err := p.tryParseCalcRawSubstring(op); err != nil {
			return nil, err
		} else if ok {
			return expr, nil
		}
	}

	// Check for target (@target)
	var target string
	if p.current().Type == interfaces.TokenAt {
		p.advance()
		if p.current().Type != interfaces.TokenIdentifier {
			return nil, fmt.Errorf("expected target name after @")
		}
		target = p.current().Literal
		p.advance()
	}

	// Parse arguments
	var args []*Expr

	// Check for function-style: op(arg1, arg2). A "(" that itself opens an
	// operator call is not function-call syntax — it is the first
	// space-separated argument, which is how nearly every documented nested
	// call is written: "(( base64 (file \"x\") ))", "(( file (concat ... ) ))",
	// "(( grab (concat ... ) ))" (the `arguments` rule, where a `primary`
	// may be a parenthesized group).
	//
	// This decision is a heuristic, not a certainty: the token right after
	// "(" not looking like it opens its own operator call (identifierOpensOpcallAt)
	// is also exactly what a parenthesized non-call expression used as the
	// first space-separated argument looks like — "(flag ? \"a\" : \"b\")",
	// "((grab a) + (grab b))". Rather than trying to tell the two apart
	// up front, this block always runs first when the shape matches (it
	// is a strict subset of "one parenthesized value", true for both
	// interpretations), and the space-separated loop below always runs
	// after it, unconditionally: for genuine "op(a, b)" function-call
	// syntax, the token right after this block's closing ")" is always a
	// terminator (")", "))", EOF, "?", ":"), so that loop's own condition
	// makes it a no-op immediately, changing nothing. When this block
	// instead consumed a single parenthesized argument that was meant to
	// be followed by more space-separated arguments, the token after it is
	// NOT a terminator, and the loop picks up exactly where the group left
	// off, appending to the same args slice.
	if p.current().Type == interfaces.TokenLeftParen && !p.identifierOpensOpcallAt(p.pos+1) {
		p.advance() // consume (
		for p.current().Type != interfaces.TokenRightParen && p.current().Type != interfaces.TokenEOF {
			arg, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			args = append(args, arg)

			if p.current().Type == interfaces.TokenComma {
				p.advance()
			} else if p.current().Type != interfaces.TokenRightParen {
				break
			}
		}
		if err := p.expectRightParen(); err != nil {
			return nil, err
		}
	}
	// Space-separated arguments until )), a bare ")" (closing an
	// operator call opened by a single "("), or
	// another operator.
	for p.current().Type != interfaces.TokenOperatorEnd &&
		p.current().Type != interfaces.TokenRightParen &&
		p.current().Type != interfaces.TokenEOF &&
		p.current().Type != interfaces.TokenQuestion &&
		p.current().Type != interfaces.TokenColon {
		// A '-' immediately followed by a number (no space possible
		// between them — the tokenizer always splits "-5" into a
		// TokenMinus and a TokenInteger/TokenFloat pair; see
		// parseUnaryMinus) is a negative-literal argument, not the
		// infix-subtraction case isBinaryOperator's blanket check
		// below assumes: each space-separated argument here is
		// parsed as one standalone primary via parsePrimary, never
		// through the full precedence-climbing expression parser, so
		// there is no "previous operand" for a '-' at this position
		// to subtract from in the first place — spruce parity: `((
		// ips net -5 ))` is a documented negative-offset form.
		// parsePrimary already dispatches TokenMinus to
		// parseUnaryMinus, which builds the negative literal.
		nextIsNumber := p.current().Type == interfaces.TokenMinus &&
			func() bool {
				next := p.tokenAt(p.pos + 1)
				return next.Type == interfaces.TokenInteger || next.Type == interfaces.TokenFloat
			}()

		// Check for binary operators that would end the argument list
		// Handle || as a LogicalOr marker for fallback expressions
		if p.isBinaryOperator(p.current().Type) && !nextIsNumber {
			if p.current().Type == interfaces.TokenOr {
				// Capture || and the fallback value as a LogicalOr expression
				// This handles (( grab this || "that" )) and (( concat a || b  c || d ))
				p.advance() // consume ||

				// Parse the right side of the || (the fallback value).
				// withOpcallEligibility lets a bare operator identifier
				// here (no "@target", no explicit "(") open its own
				// call under the same two-token rule parseOperand
				// applies elsewhere — see its doc comment.
				right, err := p.withOpcallEligibility(p.parsePrimary)
				if err != nil {
					return nil, err
				}

				// Wrap the existing args and the fallback into a LogicalOr structure
				// by creating a LogicalOr expression with left = last arg, right = fallback
				if len(args) > 0 {
					lastArg := args[len(args)-1]
					args[len(args)-1] = &Expr{
						Type:  LogicalOr,
						Left:  lastArg,
						Right: right,
					}
				}
				// Handle optional comma after the LogicalOr expression
				if p.current().Type == interfaces.TokenComma {
					p.advance()
				}
				continue
			}
			break
		}

		arg, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)

		// Optional comma
		if p.current().Type == interfaces.TokenComma {
			p.advance()
		}
	}

	expr := &Expr{
		Type:     OperatorCall,
		Operator: opName,
		Target:   target,
		Call: &Opcall{
			src:     p.input,
			op:      op,
			args:    args,
			name:    opName,
			target:  target,
			noCache: noCache,
		},
	}
	if noCache {
		expr.SetModifier("nocache")
	}
	return expr, nil
}

// modifierFollowsAt reports whether the token at nameIdx (an operator
// name) is immediately followed by a ':' glued to it and an identifier
// glued to the ':' — the shape of the `name:modifier` syntax. Offsets
// come from the scanner, so any whitespace between the three tokens
// breaks adjacency and this returns false, leaving the colon to be
// rejected by the ordinary argument parsing exactly as it was before
// modifiers existed.
func (p *Parser) modifierFollowsAt(nameIdx int) bool {
	name := p.tokenAt(nameIdx)
	colon := p.tokenAt(nameIdx + 1)
	mod := p.tokenAt(nameIdx + 2)
	return colon.Type == interfaces.TokenColon &&
		mod.Type == interfaces.TokenIdentifier &&
		colon.Pos.Offset == name.End.Offset &&
		mod.Pos.Offset == colon.End.Offset
}

// isCalcLeadingOperatorToken reports whether tok is one of the tokens that
// can open op_calc.go's leading-operator value-modification form: * / + -
// %, or the undocumented-but-accepted ^. "^" has no
// dedicated TokenType — the tokenizer's catch-all arm scans any character it
// does not otherwise recognize as a single-rune TokenInvalid, advancing
// past it — so it is identified by literal text instead of token type.
func isCalcLeadingOperatorToken(tok interfaces.Token) bool {
	switch tok.Type {
	case interfaces.TokenStar, interfaces.TokenSlash, interfaces.TokenPlus,
		interfaces.TokenMinus, interfaces.TokenPercent:
		return true
	case interfaces.TokenInvalid:
		return tok.Literal == "^"
	default:
		return false
	}
}

// tryParseCalcRawSubstring implements the calc raw-substring capture. If the token
// immediately after "calc" is a leading calc operator (isCalcLeadingOperatorToken),
// the raw input from that token's own start offset up to (not including)
// the calc call's closing "))" is captured verbatim, trimmed, and returned
// as a single string-literal argument — "(( calc * 2 ))" parses identically
// to "(( calc "* 2" ))", reusing op_calc.go's existing string-form branch
// untouched. The token cursor is left pointing at that "))" (not past it),
// exactly where the ordinary argument loop would leave it, for the normal
// enclosing ParseOpcall/parseNestedOperator expect() call to consume.
//
// Returns ok=false (falling through to ordinary argument parsing) when the
// next token is not a leading calc operator — e.g. "(( calc "1 + 2" ))" or
// "(( calc(a, b) ))" both leave the raw-capture path alone.
func (p *Parser) tryParseCalcRawSubstring(op Operator) (*Expr, bool, error) {
	tok := p.current()
	if !isCalcLeadingOperatorToken(tok) {
		return nil, false, nil
	}

	closeIdx := -1
	for i := p.pos; i < len(p.tokens); i++ {
		if p.tokens[i].Type == interfaces.TokenOperatorEnd {
			closeIdx = i
			break
		}
	}
	if closeIdx == -1 {
		return nil, false, fmt.Errorf("expected '))' to close calc expression")
	}

	raw := strings.TrimSpace(p.input[tok.Pos.Offset:p.tokens[closeIdx].Pos.Offset])
	p.pos = closeIdx

	return &Expr{
		Type:     OperatorCall,
		Operator: "calc",
		Call: &Opcall{
			src:  p.input,
			op:   op,
			name: "calc",
			args: []*Expr{{Type: Literal, Literal: raw}},
		},
	}, true, nil
}

// parseReferencePath parses a dotted reference path like foo.bar.baz.
func (p *Parser) parseReferencePath(start string) (*Expr, error) {
	// Pre-allocate for small path (most paths have 2-4 segments)
	parts := make([]string, 0, 4)
	parts = append(parts, start)

	for p.current().Type == interfaces.TokenDot {
		p.advance() // consume .

		if p.current().Type != interfaces.TokenIdentifier && p.current().Type != interfaces.TokenInteger {
			return nil, fmt.Errorf("expected identifier or index after '.'")
		}

		parts = append(parts, p.current().Literal)
		p.advance()
	}

	path := strings.Join(parts, ".")
	cursor, err := tree.ParseCursor(path)
	if err != nil {
		return nil, fmt.Errorf("invalid reference path: %s", path)
	}

	return &Expr{
		Type:      Reference,
		Reference: cursor,
	}, nil
}

// parseReference parses a reference token.
func (p *Parser) parseReference() (*Expr, error) {
	tok := p.current()
	p.advance()

	cursor, err := tree.ParseCursor(tok.Literal)
	if err != nil {
		return nil, fmt.Errorf("invalid reference: %s", tok.Literal)
	}

	return &Expr{
		Type:           Reference,
		Reference:      cursor,
		BracketedNodes: tree.BracketsOf(tok.Literal),
	}, nil
}

// parseEnvironment parses an environment variable reference.
func (p *Parser) parseEnvironment() (*Expr, error) {
	tok := p.current()
	p.advance()

	name := tok.Literal
	name = strings.TrimPrefix(name, "$")
	if strings.HasPrefix(name, "{") && strings.HasSuffix(name, "}") {
		name = name[1 : len(name)-1]
	}

	return &Expr{
		Type: EnvVar,
		Name: name,
	}, nil
}

// parseParenthesized parses a "(" group, which is either a plain arithmetic
// grouping (today's sole behavior) or an operator
// call opened by a bare "(" — e.g. "(grab a)" as an operand inside a larger
// expression. parenOpensOpcall decides which with a two-token lookahead
// (§2.3–§2.4); the two arms share everything except whether opcallPos is
// updated so the enclosed identifier is eligible to open an operator call.
func (p *Parser) parseParenthesized() (*Expr, error) {
	p.advance() // consume (

	if err := p.enterNesting(); err != nil {
		return nil, err
	}
	defer p.exitNesting()

	if p.parenOpensOpcall() {
		savedOpcallPos := p.opcallPos
		p.opcallPos = p.pos
		expr, err := p.parseExpression()
		p.opcallPos = savedOpcallPos
		if err != nil {
			return nil, err
		}
		if err := p.expectRightParen(); err != nil {
			return nil, fmt.Errorf("expected ')' to close parenthesized expression")
		}
		return expr, nil
	}

	expr, err := p.parseExpression()
	if err != nil {
		return nil, err
	}

	if err := p.expectRightParen(); err != nil {
		return nil, fmt.Errorf("expected ')' to close parenthesized expression")
	}

	return expr, nil
}

// parenOpensOpcall reports whether the "(" group about to be parsed opens an
// operator call rather than an arithmetic grouping. p.pos must point at
// the token immediately after the just-consumed
// "(". The decision is identifierOpensOpcallAt's two-token rule, shared with
// parseOperand so a "(" group and a bare operand classify identically.
// Otherwise the group is ordinary grouping — today's behavior, unchanged.
func (p *Parser) parenOpensOpcall() bool {
	return p.identifierOpensOpcallAt(p.pos)
}

// parseUnary parses a unary NOT expression.
func (p *Parser) parseUnary() (*Expr, error) {
	p.advance() // consume !

	operand, err := p.parseOperand(PrecedenceUnary)
	if err != nil {
		return nil, err
	}

	return &Expr{
		Type: Negate,
		Left: operand,
	}, nil
}

// parseUnaryMinus handles unary minus or negative numbers.
func (p *Parser) parseUnaryMinus() (*Expr, error) {
	p.advance() // consume -

	// Check if followed by a number
	if p.current().Type == interfaces.TokenInteger {
		tok := p.current()
		p.advance()
		val, err := strconv.ParseInt("-"+tok.Literal, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid integer: -%s", tok.Literal)
		}
		return &Expr{
			Type:    Literal,
			Literal: val,
		}, nil
	}

	if p.current().Type == interfaces.TokenFloat {
		tok := p.current()
		p.advance()
		val, err := strconv.ParseFloat("-"+tok.Literal, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid float: -%s", tok.Literal)
		}
		return &Expr{
			Type:    Literal,
			Literal: val,
		}, nil
	}

	// Otherwise it's a unary minus on an expression
	operand, err := p.parseOperand(PrecedenceUnary)
	if err != nil {
		return nil, err
	}

	// For unary minus, we can represent as 0 - operand or use a special type
	// For now, use Subtraction with 0 as left
	return &Expr{
		Type: Subtraction,
		Left: &Expr{
			Type:    Literal,
			Literal: int64(0),
		},
		Right: operand,
	}, nil
}

// parseNestedOperator parses a "((" the tokenizer combined from two
// adjacent '(' characters (see scanParentheses). Usually this is a
// genuine nested marker used as an operand — e.g. the prune-default idiom
// "grab X || (( prune ))", where the double parens let a zero-argument
// bare operator claim the primary-call position regardless of what
// follows it. But the tokenizer combines any two adjacent '(' regardless
// of context, so the same "((" also appears where two ordinary,
// independent grouping parens just happen to sit back to back, e.g.
// "((A && B) || C)".
//
// The two shapes are told apart after parsing: a genuine marker's content
// is immediately followed by "))". If a single ')' appears instead, the
// "((" was really two separate opens — that ')' closes the inner one, and
// the value just parsed becomes the left operand of whatever expression
// continues at the outer group's level, up to its own closing ')'.
func (p *Parser) parseNestedOperator() (*Expr, error) {
	p.advance() // consume ((

	if err := p.enterNesting(); err != nil {
		return nil, err
	}
	defer p.exitNesting()

	savedOpcallPos := p.opcallPos
	p.opcallPos = p.pos
	expr, err := p.parseExpression()
	p.opcallPos = savedOpcallPos
	if err != nil {
		return nil, err
	}

	if p.current().Type == interfaces.TokenOperatorEnd {
		p.advance()
		return expr, nil
	}

	if p.current().Type == interfaces.TokenRightParen {
		p.advance() // consume the inner group's own ')'

		outer, err := p.continueExprWithPrecedence(expr, PrecedenceLowest)
		if err != nil {
			return nil, err
		}

		if err := p.expectRightParen(); err != nil {
			return nil, fmt.Errorf("expected ')' to close parenthesized expression")
		}

		return outer, nil
	}

	return nil, fmt.Errorf("expected '))' to close nested operator")
}

// parseTarget parses a target reference @something.
func (p *Parser) parseTarget() (*Expr, error) {
	p.advance() // consume @

	if p.current().Type != interfaces.TokenIdentifier {
		// This is where form (a) — "(( vault prod@\"path:key\" ))", the
		// target written as a bare argument followed by "@" — falls through
		// to a parse error: "prod" parses as an ordinary argument, then "@"
		// starts a new primary expression here, and whatever follows it
		// (typically a string literal) is never a valid target identifier.
		// Spec cluster A7 §7.2 redirects this to the supported form (b).
		return nil, fmt.Errorf(
			`vault target must be written as (( vault@<target> "path:key" )), not (( vault <target>@"path:key" ))`,
		)
	}

	name := p.current().Literal
	p.advance()

	return &Expr{
		Type: Reference,
		Name: name,
	}, nil
}

// getBinaryOpInfo returns precedence, associativity, and expr type for binary operators.
//
//nolint:unparam // associativity returned for future right-associative operator support
func (p *Parser) getBinaryOpInfo(tokenType interfaces.TokenType) (Precedence, Associativity, ExprType) {
	switch tokenType {
	case interfaces.TokenAnd:
		return PrecedenceAnd, LeftAssociative, LogicalAnd
	case interfaces.TokenPlus:
		return PrecedenceAdditive, LeftAssociative, Addition
	case interfaces.TokenMinus:
		return PrecedenceAdditive, LeftAssociative, Subtraction
	case interfaces.TokenStar:
		return PrecedenceMultiplicative, LeftAssociative, Multiplication
	case interfaces.TokenSlash:
		return PrecedenceMultiplicative, LeftAssociative, Division
	case interfaces.TokenPercent:
		return PrecedenceMultiplicative, LeftAssociative, Modulo
	case interfaces.TokenEqual:
		return PrecedenceEquality, LeftAssociative, Equal
	case interfaces.TokenNotEqual:
		return PrecedenceEquality, LeftAssociative, NotEqual
	case interfaces.TokenLess:
		return PrecedenceComparison, LeftAssociative, LessThan
	case interfaces.TokenGreater:
		return PrecedenceComparison, LeftAssociative, GreaterThan
	case interfaces.TokenLessEqual:
		return PrecedenceComparison, LeftAssociative, LessThanOrEqual
	case interfaces.TokenGreaterEqual:
		return PrecedenceComparison, LeftAssociative, GreaterThanOrEqual
	case interfaces.TokenEOF, interfaces.TokenInvalid, interfaces.TokenOperatorStart, interfaces.TokenOperatorEnd,
		interfaces.TokenInteger, interfaces.TokenFloat, interfaces.TokenString, interfaces.TokenRawString,
		interfaces.TokenBoolean, interfaces.TokenNull, interfaces.TokenIdentifier, interfaces.TokenReference, interfaces.TokenEnvironment,
		interfaces.TokenOr, interfaces.TokenNot, interfaces.TokenQuestion, interfaces.TokenColon,
		interfaces.TokenLeftParen, interfaces.TokenRightParen, interfaces.TokenLeftBracket, interfaces.TokenRightBracket,
		interfaces.TokenLeftBrace, interfaces.TokenRightBrace, interfaces.TokenComma, interfaces.TokenDot, interfaces.TokenAt, interfaces.TokenPipe,
		interfaces.TokenIf, interfaces.TokenElif, interfaces.TokenElse, interfaces.TokenFi,
		interfaces.TokenFor, interfaces.TokenIn, interfaces.TokenDone, interfaces.TokenWhile,
		interfaces.TokenCase, interfaces.TokenWhen, interfaces.TokenDefault, interfaces.TokenEsac,
		interfaces.TokenOn, interfaces.TokenBefore, interfaces.TokenAfter, interfaces.TokenRange, interfaces.TokenOperatorName:
		return PrecedenceLowest, LeftAssociative, Literal
	}
	return PrecedenceLowest, LeftAssociative, Literal
}

// isBinaryOperator checks if a token type is a binary operator.
func (p *Parser) isBinaryOperator(tokenType interfaces.TokenType) bool {
	switch tokenType {
	case interfaces.TokenPlus, interfaces.TokenMinus,
		interfaces.TokenStar, interfaces.TokenSlash, interfaces.TokenPercent,
		interfaces.TokenEqual, interfaces.TokenNotEqual,
		interfaces.TokenLess, interfaces.TokenGreater,
		interfaces.TokenLessEqual, interfaces.TokenGreaterEqual,
		interfaces.TokenAnd, interfaces.TokenOr,
		interfaces.TokenQuestion:
		return true
	case interfaces.TokenEOF, interfaces.TokenInvalid, interfaces.TokenOperatorStart, interfaces.TokenOperatorEnd,
		interfaces.TokenInteger, interfaces.TokenFloat, interfaces.TokenString, interfaces.TokenRawString,
		interfaces.TokenBoolean, interfaces.TokenNull, interfaces.TokenIdentifier, interfaces.TokenReference, interfaces.TokenEnvironment,
		interfaces.TokenNot, interfaces.TokenColon,
		interfaces.TokenLeftParen, interfaces.TokenRightParen, interfaces.TokenLeftBracket, interfaces.TokenRightBracket,
		interfaces.TokenLeftBrace, interfaces.TokenRightBrace, interfaces.TokenComma, interfaces.TokenDot, interfaces.TokenAt, interfaces.TokenPipe,
		interfaces.TokenIf, interfaces.TokenElif, interfaces.TokenElse, interfaces.TokenFi,
		interfaces.TokenFor, interfaces.TokenIn, interfaces.TokenDone, interfaces.TokenWhile,
		interfaces.TokenCase, interfaces.TokenWhen, interfaces.TokenDefault, interfaces.TokenEsac,
		interfaces.TokenOn, interfaces.TokenBefore, interfaces.TokenAfter, interfaces.TokenRange, interfaces.TokenOperatorName:
		return false
	}
	return false
}

// exprToOpcall converts a parsed expression to an Opcall.
func (p *Parser) exprToOpcall(expr *Expr) (*Opcall, error) {
	if expr == nil {
		return nil, fmt.Errorf("nil expression")
	}

	// If it's already an operator call, extract the Opcall
	if expr.Type == OperatorCall && expr.Call != nil {
		return expr.Call, nil
	}

	// For other expression types, wrap in an appropriate operator
	// Reference expressions just return the reference value
	if expr.Type == Reference {
		// Create a "grab" operator call for references
		op := p.operatorFor("grab")
		if _, ok := op.(NullOperator); ok {
			return nil, fmt.Errorf("grab operator not found")
		}
		return &Opcall{
			src:  p.input,
			op:   op,
			args: []*Expr{expr},
		}, nil
	}

	// For other expression types (arithmetic, logical, including those with
	// nested operator calls), wrap in exprOperator for evaluation
	return &Opcall{
		src:  p.input,
		op:   &exprOperator{expr: expr},
		args: []*Expr{expr},
	}, nil
}

// exprOperator is a synthetic operator for evaluating expressions.
type exprOperator struct {
	expr *Expr
}

func (e *exprOperator) Setup() error {
	return nil
}

func (e *exprOperator) Phase() OperatorPhase {
	return EvalPhase
}

func (e *exprOperator) Dependencies(ev *Evaluator, args []*Expr, locs []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	if e.expr != nil {
		return e.expr.Dependencies(ev, locs)
	}
	return auto
}

func (e *exprOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	if len(args) == 0 || args[0] == nil {
		return nil, fmt.Errorf("no expression to evaluate")
	}

	// Set evaluator on the expression before evaluating
	// This is needed for nested operator calls within the expression
	args[0].SetEvaluator(ev)

	val, err := args[0].Evaluate(ev.Tree)
	if err != nil {
		return nil, err
	}

	return &Response{
		Type:  Replace,
		Value: val,
	}, nil
}

// processEscapes processes escape sequences in a string.
func processEscapes(s string) string {
	result := strings.Builder{}
	i := 0
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				result.WriteByte('\n')
				i += 2
			case 'r':
				result.WriteByte('\r')
				i += 2
			case 't':
				result.WriteByte('\t')
				i += 2
			case '\\':
				result.WriteByte('\\')
				i += 2
			case '"':
				result.WriteByte('"')
				i += 2
			case '\'':
				result.WriteByte('\'')
				i += 2
			default:
				result.WriteByte(s[i])
				i++
			}
		} else {
			result.WriteByte(s[i])
			i++
		}
	}
	return result.String()
}

// ParseOpcallWithParser parses an operator call using the new Parser.
func ParseOpcallWithParser(phase OperatorPhase, src string) (*Opcall, error) {
	return ParseOpcallWithParserForEngine(nil, phase, src)
}

// ParseOpcallWithParserForEngine parses an operator call using the new
// Parser, resolving operator names against e's local registry before
// falling back to DefaultRegistry. A nil engine behaves identically to
// ParseOpcallWithParser.
func ParseOpcallWithParserForEngine(e Engine, phase OperatorPhase, src string) (*Opcall, error) {
	// Quick check - must start with (( and end with ))
	src = strings.TrimSpace(src)
	if !strings.HasPrefix(src, "((") || !strings.HasSuffix(src, "))") {
		return nil, nil
	}

	inner := strings.TrimSuffix(strings.TrimPrefix(src, "(("), "))")
	if tightBoshPlaceholderRe.MatchString(inner) && !isRegisteredOperatorName(e, inner) {
		// A whitespace-free BOSH/CredHub variable placeholder like
		// ((cf_admin_password)), ((var.subkey)), ((/absolute.path)), or
		// genesis's ((genesis-entombed/name--key--sha1)). Spruce never
		// treats these as operator calls; they must survive a merge
		// byte-for-byte for a later tool to interpolate. A tight
		// registered name like ((prune)) is still an operator call, as
		// it is in spruce.
		return nil, nil
	}

	parser := NewParserForEngine(src, phase, e)
	opcall, err := parser.ParseOpcall()
	if err != nil {
		if !startsWithRegisteredOperator(e, inner) {
			// Spruce only reports operator errors for expressions that
			// begin with a registered operator name; anything else that
			// fails to parse is not an operator call at all and flows
			// through as literal text (BOSH placeholders with shapes the
			// expression grammar rejects, multi-token free text, etc.).
			return nil, nil
		}
		// For debugging
		if os.Getenv("GRAFT_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Parser error for '%s': %v\n", src, err)
		}
		return nil, err
	}

	if isBoshPlaceholder(opcall) {
		return nil, nil
	}

	return opcall, nil
}

// tightBoshPlaceholderRe matches the BOSH variable-placeholder grammar: a
// whitespace-free run of name characters (letters, digits, underscore,
// dot, slash, dash), optionally prefixed with "!". Note it is matched
// against the raw inner text of "(( ... ))" without trimming, so any
// interior or edge whitespace disqualifies the shape.
var tightBoshPlaceholderRe = regexp.MustCompile(`^!?[a-zA-Z0-9_./-]+$`)

// leadingOperatorNameRe captures a leading operator-name-shaped token.
var leadingOperatorNameRe = regexp.MustCompile(`^\s*([a-zA-Z][a-zA-Z0-9_-]*)`)

// isRegisteredOperatorName reports whether name (exactly, in full) is a
// registered operator in e's registry or the global default registry.
func isRegisteredOperatorName(e Engine, name string) bool {
	if _, isNull := OperatorForEngine(e, name).(NullOperator); isNull {
		return false
	}
	return true
}

// startsWithRegisteredOperator reports whether the inner text of a
// "(( ... ))" expression begins with a registered operator name — the
// shape spruce commits to parsing as an operator call (and reports
// errors for) rather than passing through as literal text.
func startsWithRegisteredOperator(e Engine, inner string) bool {
	m := leadingOperatorNameRe.FindStringSubmatch(inner)
	if m == nil {
		return false
	}
	return isRegisteredOperatorName(e, m[1])
}

// isBoshPlaceholder reports whether a parsed top-level opcall is really a
// BOSH/CredHub-style variable placeholder that spruce would never treat as
// an operator call: a bare reference like ((var.subkey)), or a lone
// unregistered name like ((var)). Both shapes must flow through a merge
// byte-for-byte (BOSH's placeholder grammar allows no interior whitespace,
// so even re-rendering with normalized spacing corrupts them) for a later
// tool — the BOSH director, CredHub, genesis — to interpolate. Spruce only
// evaluates (( <registered-op> [args...] )); this restores that boundary
// without touching operand-position reference semantics inside larger
// expressions.
func isBoshPlaceholder(opcall *Opcall) bool {
	if opcall == nil {
		return false
	}

	// exprToOpcall's synthetic grab wrap for a whole-expression bare
	// reference: no operator name as written, exactly one Reference arg. A
	// genuine (( grab x )) carries name "grab".
	if opcall.name == "" && len(opcall.args) == 1 &&
		opcall.args[0] != nil && opcall.args[0].Type == Reference {
		return true
	}

	// A lone unregistered name, e.g. ((cf_admin_password)): parsed as an
	// operator call with no arguments whose operator resolved to the
	// NullOperator placeholder. With arguments it stays an opcall (the
	// pinned B-2 behavior for (( bogus foo ))).
	if _, isNull := opcall.op.(NullOperator); isNull && len(opcall.args) == 0 {
		return true
	}

	return false
}
