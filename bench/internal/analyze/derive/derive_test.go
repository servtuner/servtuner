package derive

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
)

func env(values map[string]float64) func(string) (float64, bool) {
	return func(id string) (float64, bool) {
		value, ok := values[id]
		return value, ok
	}
}

func evaluate(t *testing.T, formula string, values map[string]float64) float64 {
	t.Helper()
	expr, err := Parse(formula)
	if err != nil {
		t.Fatal(err)
	}
	value, err := Eval(expr, env(values))
	if err != nil {
		t.Fatalf("%s: %v", formula, err)
	}
	return value
}

func TestArithmeticFollowsPrecedenceAndAssociativity(t *testing.T) {
	for formula, want := range map[string]float64{
		"1 + 2 * 3":           7,
		"(1 + 2) * 3":         9,
		"10 - 4 - 3":          3,
		"100 / 10 / 5":        2,
		"-2 * 3":              -6,
		"2 - -3":              5,
		"max(1, 5, 3)":        5,
		"min(4, 2)":           2,
		"clamp(12, 0, 10)":    10,
		"roundup(0.31, 0.05)": 0.35,
		"roundup(0.30, 0.05)": 0.3,
		"⌈2.1⌉ × 3":           9,
		"⌊2.9⌋ ÷ 2":           1,
		"√(16) − 1":           3,
		"x.y * 2":             8,
	} {
		if got := evaluate(t, formula, map[string]float64{"x.y": 4}); math.Abs(got-want) > 1e-12 {
			t.Errorf("%s = %v, want %v", formula, got, want)
		}
	}
}

func TestBadFormulasAreRejected(t *testing.T) {
	for _, formula := range []string{"", "1 +", "(1", "max(1,", "nope(1)", "1 $ 2", "⌈1", "√1", "1 2"} {
		if _, err := Parse(formula); err == nil {
			t.Errorf("%q parsed", formula)
		}
	}
	for formula, values := range map[string]map[string]float64{
		"1 / zero":         {"zero": 0},
		"missing + 1":      {},
		"sqrt(-1)":         {},
		"clamp(1, 5, 2)":   {},
		"ceil(1, 2)":       {},
		"poisson_q(1, 10)": {},
	} {
		expr, err := Parse(formula)
		if err != nil {
			t.Fatalf("%s: %v", formula, err)
		}
		if _, err := Eval(expr, env(values)); err == nil {
			t.Errorf("%s evaluated", formula)
		}
	}
}

// Rounding up must not turn float error into a whole extra unit: the worked
// worked example gives ⌈190 × 1.1⌉ = 209, not 210.
func TestRoundingIgnoresFloatingPointError(t *testing.T) {
	if got := evaluate(t, "ceil(190 * 1.1)", nil); got != 209 {
		t.Errorf("ceil(190 × 1.1) = %v", got)
	}
	if got := evaluate(t, "roundup(0.1 + 0.2, 0.1)", nil); got != 0.3 {
		t.Errorf("roundup = %v", got)
	}
	if got := evaluate(t, "roundup(0.26, 0.05)", nil); got != 0.3 {
		t.Errorf("roundup to 50m = %v, want exactly 0.3", got)
	}
}

// Known values: P(N ≤ q − 1) < p ≤ P(N ≤ q).
func TestPoissonQuantile(t *testing.T) {
	for _, c := range []struct{ p, mean, want float64 }{
		{0.99, 1, 4}, {0.99, 10, 18}, {0.99, 20, 31}, {0.5, 0, 0}, {0.99, 160, 190},
	} {
		got, err := PoissonQuantile(c.p, c.mean)
		if err != nil || got != c.want {
			t.Errorf("poisson_q(%v, %v) = %v, %v; want %v", c.p, c.mean, got, err, c.want)
		}
	}
	exact, _ := PoissonQuantile(0.99, poissonExactLimit)
	approximate, _ := PoissonQuantile(0.99, poissonExactLimit+1)
	if math.Abs(approximate-exact) > 2 {
		t.Errorf("the normal approximation jumps at the switch: %v then %v", exact, approximate)
	}
}

// Textbook values: c = 1 is ρ; c = 2, a = 1 is 1/3; a ≥ c always waits.
func TestErlangC(t *testing.T) {
	for _, c := range []struct{ servers, load, want float64 }{
		{1, 0.5, 0.5}, {2, 1, 1.0 / 3}, {10, 8, 0.4092}, {5, 5, 1}, {3, 0, 0},
	} {
		got, err := ErlangC(c.servers, c.load)
		if err != nil || math.Abs(got-c.want) > 1e-4 {
			t.Errorf("erlang_c(%v, %v) = %v, %v; want %v", c.servers, c.load, got, err, c.want)
		}
	}
	for _, bad := range [][2]float64{{0, 1}, {2.5, 1}, {2, -1}} {
		if _, err := ErlangC(bad[0], bad[1]); err == nil {
			t.Errorf("erlang_c(%v, %v) accepted", bad[0], bad[1])
		}
	}
}

// randomExpr builds a formula over a, b and c with every operator and function.
func randomExpr(r *rand.Rand, depth int) string {
	if depth == 0 || r.IntN(4) == 0 {
		switch r.IntN(3) {
		case 0:
			return []string{"a", "b", "c"}[r.IntN(3)]
		case 1:
			return FormatNumber(float64(r.IntN(50) + 1))
		default:
			return FormatNumber(float64(r.IntN(1000)+1) / 100)
		}
	}
	left, right := randomExpr(r, depth-1), randomExpr(r, depth-1)
	switch r.IntN(9) {
	case 0:
		return left + " + " + right
	case 1:
		return left + " - (" + right + ")" // a − (b − c) must keep its parentheses
	case 2:
		return left + " * " + right
	case 3:
		return "(" + left + ") / (" + right + " / 2 + 100)"
	case 4:
		return "(" + left + ") * (" + right + ")"
	case 5:
		return "max(" + left + ", " + right + ")"
	case 6:
		return "ceil(" + left + ")"
	case 7:
		return "-(" + left + ")"
	default:
		return "min(" + left + ", " + right + ", 7)"
	}
}

// The rendered string of a formula is itself a formula with the same value
// (the derivation's principles). Rendering rounds to six significant
// digits, so values agree to that precision.
func TestRenderedFormulasReevaluateToTheirValue(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	// c is large and fractional: whole-number renderings must keep their zeros.
	values := map[string]float64{"a": 3.25, "b": 160, "c": 121371570.34}
	for i := range 2000 {
		formula := randomExpr(r, 4)
		expr, err := Parse(formula)
		if err != nil {
			t.Fatalf("%s: %v", formula, err)
		}
		want, err := Eval(expr, env(values))
		if err != nil {
			continue // a division or ceiling can still overflow; the property is about renderings
		}
		rendered := Render(expr, env(values))
		got := evaluate(t, rendered, nil)
		if math.Abs(got-want) > 1e-4*math.Max(1, math.Abs(want)) {
			t.Fatalf("case %d: %s = %v, but its rendering %s = %v", i, formula, want, rendered, got)
		}
	}
}

func TestFormatNumber(t *testing.T) {
	for value, want := range map[float64]string{
		121371570.34: "121371570", 1000000.6: "1000001", 2500.4: "2500.4",
		209: "209", 1.1: "1.1", math.Copysign(0, -1): "0", 0.123456789: "0.123457", 189.4296: "189.43", -2.5: "-2.5", 1e6: "1000000", 0.0005: "0.0005",
	} {
		if got := FormatNumber(value); got != want {
			t.Errorf("FormatNumber(%v) = %q, want %q", value, got, want)
		}
	}
}

// workedExample is the Bifrost concurrency sizing example.
func workedExample() *Graph {
	g := NewGraph()
	stage := 3
	g.AddMeasured("lambda", "Arrival rate", "requests/s", 20, &Source{StageIndex: &stage})
	g.AddMeasured("W_mean", "Mean E2E", "s", 8, &Source{StageIndex: &stage})
	g.AddMeasured("W_p99", "p99 E2E", "s", 12, &Source{StageIndex: &stage})
	g.AddMeasured("C_emp", "p99 of in-flight L(t)", "requests", 185, &Source{StageIndex: &stage, File: "charts/inflight.json"})
	low, high := 1.0, 2.0
	g.AddParameter("safety", "Safety factor", "", 1.1, &Bounds{Min: &low, Max: &high}, "queueing model")
	g.AddDerived("C_pois", "Poisson p99 in flight", "requests", "poisson_q(0.99, lambda * W_mean)")
	g.AddDerived("C_cons", "Conservative bound", "requests", "lambda * W_p99")
	g.AddDerived("bifrost.concurrency", "Recommended C", "requests", "clamp(ceil(max(C_emp, C_pois) * safety), C_emp, C_cons)")
	g.AddDerived("rho", "Utilization", "", "lambda * W_mean / bifrost.concurrency")
	return g
}

func TestTheWorkedExampleOfTheQueueingModel(t *testing.T) {
	result, err := workedExample().Evaluate()
	if err != nil {
		t.Fatal(err)
	}
	concurrency, _ := result.Node("bifrost.concurrency")
	if concurrency.Value != 209 {
		t.Errorf("C = %v, want 209", concurrency.Value)
	}
	if want := "clamp(⌈max(185, 190) × 1.1⌉, 185, 240) = 209"; concurrency.Rendered != want {
		t.Errorf("rendered %q, want %q", concurrency.Rendered, want)
	}
	if rho, _ := result.Value("rho"); math.Abs(rho-160.0/209) > 1e-12 {
		t.Errorf("rho = %v", rho)
	}
	if strings.Join(concurrency.Inputs, ",") != "C_cons,C_emp,C_pois,safety" {
		t.Errorf("inputs = %v", concurrency.Inputs)
	}
	for _, node := range result.Nodes {
		if node.Kind != Derived {
			continue
		}
		formula, _, _ := strings.Cut(node.Rendered, " = ")
		if got := evaluate(t, formula, nil); math.Abs(got-node.Value) > 1e-4*math.Max(1, node.Value) {
			t.Errorf("%s: rendered %q evaluates to %v, not %v", node.ID, node.Rendered, got, node.Value)
		}
	}
}

func TestOverridesChangeTheGraphWithoutMutatingIt(t *testing.T) {
	g := workedExample()
	result, err := g.Evaluate(Override{Node: "safety", Kind: SetParameter, Value: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := result.Value("bifrost.concurrency"); c != 240 { // ⌈190 × 1.5⌉ = 285, clamped to C_cons
		t.Errorf("C with safety 1.5 = %v", c)
	}
	baseline, _ := g.Evaluate()
	if c, _ := baseline.Value("bifrost.concurrency"); c != 209 {
		t.Errorf("the override leaked into the graph: C = %v", c)
	}
}

func TestPinningADerivedValueKeepsWhatTheFormulaWouldGive(t *testing.T) {
	result, err := workedExample().Evaluate(Override{Node: "bifrost.concurrency", Kind: Pin, Value: 250})
	if err != nil {
		t.Fatal(err)
	}
	node, _ := result.Node("bifrost.concurrency")
	if !node.IsPinned || node.Value != 250 || node.Derivable == nil || *node.Derivable != 209 {
		t.Errorf("pinned node = %+v", node)
	}
	if rho, _ := result.Value("rho"); math.Abs(rho-160.0/250) > 1e-12 {
		t.Errorf("rho follows the pinned value: %v", rho)
	}
}

func TestAHypotheticalInputMarksEverythingDownstream(t *testing.T) {
	result, err := workedExample().Evaluate(Override{Node: "W_mean", Kind: Assume, Value: 10})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"W_mean": true, "C_pois": true, "bifrost.concurrency": true, "rho": true, "C_cons": false, "C_emp": false} {
		if node, _ := result.Node(id); node.IsHypothetical != want {
			t.Errorf("%s hypothetical = %v, want %v", id, node.IsHypothetical, want)
		}
	}
	if node, _ := result.Node("W_mean"); node.Kind != Hypothetical {
		t.Errorf("W_mean kind = %s", node.Kind)
	}
}

func TestOverridesThatDoNotFitAreRefused(t *testing.T) {
	for name, override := range map[string]Override{
		"parameter out of bounds":  {Node: "safety", Kind: SetParameter, Value: 3},
		"pin on a measured node":   {Node: "C_emp", Kind: Pin, Value: 1},
		"assume on a derived node": {Node: "rho", Kind: Assume, Value: 1},
		"set on a derived node":    {Node: "rho", Kind: SetParameter, Value: 1},
		"unknown node":             {Node: "nope", Kind: SetParameter, Value: 1},
	} {
		if _, err := workedExample().Evaluate(override); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestGraphErrors(t *testing.T) {
	cases := map[string]func(*Graph){
		"cycle": func(g *Graph) {
			g.AddDerived("a", "", "", "b + 1")
			g.AddDerived("b", "", "", "a + 1")
		},
		"unknown input": func(g *Graph) { g.AddDerived("a", "", "", "missing * 2") },
		"duplicate id": func(g *Graph) {
			g.AddMeasured("a", "", "", 1, nil)
			g.AddMeasured("a", "", "", 2, nil)
		},
		"bad formula":           func(g *Graph) { g.AddDerived("a", "", "", "1 +") },
		"default out of bounds": func(g *Graph) { one := 1.0; g.AddParameter("p", "", "", 2, &Bounds{Max: &one}, "") },
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			g := NewGraph()
			build(g)
			if _, err := g.Evaluate(); err == nil {
				t.Error("evaluated")
			} else if name == "cycle" && !strings.Contains(err.Error(), "→") {
				t.Errorf("the cycle is not shown: %v", err)
			}
		})
	}
}

func ExampleGraph_Evaluate() {
	result, _ := workedExample().Evaluate()
	node, _ := result.Node("bifrost.concurrency")
	fmt.Println(node.Rendered)
	// Output: clamp(⌈max(185, 190) × 1.1⌉, 185, 240) = 209
}
