// Package trigger evaluates a quota predicate and execs the matching
// command. The expression grammar is intentionally tiny:
//
//	expr := and  ('||' and)*
//	and  := cmp  ('&&' cmp )*
//	cmp  := atom OP atom         OP ∈ {<, <=, >, >=, ==, !=}
//	atom := ident | number | bool
//	ident := <provider>.<field>  e.g. claude.long_pct, codex.short_pct
//
// Anything fancier belongs in the user's `run` command, not here.
package trigger

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter"
)

// Context is the set of identifiers an expression can reference.
// Keys: "claude.short_pct", "claude.long_pct", "claude.extra_usage",
// "claude.ok", and the codex.* analogues.
type Context map[string]any

// BuildContext turns a list of (provider, snapshot, fetchErr) tuples
// into an evaluation context. Missing or errored providers get *.ok =
// false and *_pct = +Inf so that `<` predicates naturally exclude them.
type Source struct {
	Provider string
	Snap     *adapter.UsageSnapshot
	Err      error
}

func BuildContext(sources []Source) Context {
	ctx := Context{}
	// Pre-seed known providers with safe defaults so a predicate
	// referencing a provider that didn't load still evaluates without
	// "unknown identifier" errors.
	for _, p := range []string{"claude", "codex"} {
		ctx[p+".ok"] = false
		ctx[p+".short_pct"] = math.Inf(1)
		ctx[p+".long_pct"] = math.Inf(1)
		ctx[p+".extra_usage"] = false
	}
	for _, s := range sources {
		ok := s.Err == nil && s.Snap != nil
		ctx[s.Provider+".ok"] = ok
		if !ok {
			continue
		}
		if w, has := s.Snap.Windows["short"]; has {
			ctx[s.Provider+".short_pct"] = w.UsedPercent * 100
		}
		if w, has := s.Snap.Windows["long"]; has {
			ctx[s.Provider+".long_pct"] = w.UsedPercent * 100
		}
		ctx[s.Provider+".extra_usage"] = s.Snap.ExtraUsageEnabled
	}
	return ctx
}

// Eval parses and evaluates the expression against ctx.
func Eval(expr string, ctx Context) (bool, error) {
	p := &parser{src: expr, ctx: ctx}
	p.skipSpace()
	v, err := p.parseOr()
	if err != nil {
		return false, err
	}
	p.skipSpace()
	if p.pos != len(p.src) {
		return false, fmt.Errorf("unexpected trailing input at offset %d: %q", p.pos, p.src[p.pos:])
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("expression must evaluate to bool, got %T", v)
	}
	return b, nil
}

// Result reports what happened to one trigger in a tick.
type Result struct {
	Name     string
	Allowed  bool   // when-expr was true
	ExecErr  error  // exec failure (timeout, non-zero exit, etc.)
	ParseErr error  // when-expr parse / type error
	DryRun   bool
	Duration time.Duration
	Stdout   string // truncated
	Stderr   string // truncated
}

// Run evaluates `when`. If true and not dry-run, execs `run` with the
// trigger's timeout and env merged on top of the parent env. Stdout +
// stderr are captured to logs (caller decides how to surface).
func Run(ctx context.Context, name, when string, runArgv []string, timeout time.Duration, env map[string]string, evalCtx Context, dryRun bool) Result {
	r := Result{Name: name, DryRun: dryRun}
	allow, err := Eval(when, evalCtx)
	if err != nil {
		r.ParseErr = err
		return r
	}
	r.Allowed = allow
	if !allow || dryRun {
		return r
	}
	if len(runArgv) == 0 {
		r.ExecErr = fmt.Errorf("empty run argv")
		return r
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, runArgv[0], runArgv[1:]...)
	cmd.Env = mergedEnv(env)
	var outBuf, errBuf truncBuf
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	start := time.Now()
	err = cmd.Run()
	r.Duration = time.Since(start)
	r.Stdout = outBuf.String()
	r.Stderr = errBuf.String()
	if err != nil {
		r.ExecErr = err
	}
	return r
}

func mergedEnv(extra map[string]string) []string {
	if len(extra) == 0 {
		return os.Environ()
	}
	parent := os.Environ()
	out := make([]string, 0, len(parent)+len(extra))
	overridden := make(map[string]struct{}, len(extra))
	for k := range extra {
		overridden[k] = struct{}{}
	}
	for _, kv := range parent {
		eq := strings.IndexByte(kv, '=')
		if eq > 0 {
			if _, ok := overridden[kv[:eq]]; ok {
				continue
			}
		}
		out = append(out, kv)
	}
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

// truncBuf is a write target that drops bytes past the limit so a
// runaway command can't blow up gleaner's memory. The captured text is
// only used for logging.
type truncBuf struct {
	buf []byte
}

const truncLimit = 64 * 1024

func (b *truncBuf) Write(p []byte) (int, error) {
	avail := truncLimit - len(b.buf)
	if avail <= 0 {
		return len(p), nil
	}
	if len(p) > avail {
		b.buf = append(b.buf, p[:avail]...)
	} else {
		b.buf = append(b.buf, p...)
	}
	return len(p), nil
}

func (b *truncBuf) String() string { return string(b.buf) }

// --- parser ---------------------------------------------------------

type parser struct {
	src string
	pos int
	ctx Context
}

func (p *parser) skipSpace() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t' || p.src[p.pos] == '\n') {
		p.pos++
	}
}

func (p *parser) consume(s string) bool {
	p.skipSpace()
	if strings.HasPrefix(p.src[p.pos:], s) {
		p.pos += len(s)
		return true
	}
	return false
}

func (p *parser) parseOr() (any, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.consume("||") {
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		lb, ok1 := left.(bool)
		rb, ok2 := right.(bool)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("|| requires bool operands")
		}
		left = lb || rb
	}
	return left, nil
}

func (p *parser) parseAnd() (any, error) {
	left, err := p.parseCmp()
	if err != nil {
		return nil, err
	}
	for p.consume("&&") {
		right, err := p.parseCmp()
		if err != nil {
			return nil, err
		}
		lb, ok1 := left.(bool)
		rb, ok2 := right.(bool)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("&& requires bool operands")
		}
		left = lb && rb
	}
	return left, nil
}

// parseCmp parses `atom OP atom`. A bare atom is allowed only when
// it's already a bool (e.g. `claude.ok`).
func (p *parser) parseCmp() (any, error) {
	left, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	op := p.peekOp()
	if op == "" {
		return left, nil
	}
	p.pos += len(op)
	right, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	return applyOp(op, left, right)
}

func (p *parser) peekOp() string {
	p.skipSpace()
	rest := p.src[p.pos:]
	for _, o := range []string{"<=", ">=", "==", "!=", "<", ">"} {
		if strings.HasPrefix(rest, o) {
			return o
		}
	}
	return ""
}

func (p *parser) parseAtom() (any, error) {
	p.skipSpace()
	if p.pos >= len(p.src) {
		return nil, fmt.Errorf("unexpected end of expression")
	}
	ch := p.src[p.pos]
	// number
	if ch == '-' || ch == '.' || (ch >= '0' && ch <= '9') {
		return p.parseNumber()
	}
	// identifier or bool literal
	if isIdentStart(ch) {
		return p.parseIdent()
	}
	return nil, fmt.Errorf("unexpected character %q at offset %d", ch, p.pos)
}

func (p *parser) parseNumber() (float64, error) {
	start := p.pos
	if p.src[p.pos] == '-' {
		p.pos++
	}
	for p.pos < len(p.src) {
		ch := p.src[p.pos]
		if (ch >= '0' && ch <= '9') || ch == '.' {
			p.pos++
			continue
		}
		break
	}
	tok := p.src[start:p.pos]
	v, err := strconv.ParseFloat(tok, 64)
	if err != nil {
		return 0, fmt.Errorf("bad number %q: %w", tok, err)
	}
	return v, nil
}

func (p *parser) parseIdent() (any, error) {
	start := p.pos
	for p.pos < len(p.src) && isIdentPart(p.src[p.pos]) {
		p.pos++
	}
	tok := p.src[start:p.pos]
	switch tok {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	v, ok := p.ctx[tok]
	if !ok {
		return nil, fmt.Errorf("unknown identifier %q", tok)
	}
	return v, nil
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '.'
}

func applyOp(op string, l, r any) (bool, error) {
	lf, lok := toFloat(l)
	rf, rok := toFloat(r)
	if lok && rok {
		switch op {
		case "<":
			return lf < rf, nil
		case "<=":
			return lf <= rf, nil
		case ">":
			return lf > rf, nil
		case ">=":
			return lf >= rf, nil
		case "==":
			return lf == rf, nil
		case "!=":
			return lf != rf, nil
		}
	}
	// bool equality
	if op == "==" || op == "!=" {
		lb, lbok := l.(bool)
		rb, rbok := r.(bool)
		if lbok && rbok {
			eq := lb == rb
			if op == "==" {
				return eq, nil
			}
			return !eq, nil
		}
	}
	return false, fmt.Errorf("operator %s: incompatible operands (%T, %T)", op, l, r)
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}
