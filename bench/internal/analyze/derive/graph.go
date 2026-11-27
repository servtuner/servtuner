package derive

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Kind is what a node is ("The derivation graph").
type Kind string

const (
	// Measured comes from the run; never edited, only re-sourced.
	Measured Kind = "measured"
	// Parameter is a tunable input with a default and bounds.
	Parameter Kind = "parameter"
	// Derived is a formula over other nodes; it may be pinned to a value.
	Derived Kind = "derived"
	// Hypothetical is a measured node replaced by an assumed value. Every node
	// downstream of one is hypothetical too.
	Hypothetical Kind = "hypothetical"
)

// Bounds limit a parameter. Either side may be absent.
type Bounds struct {
	Min *float64
	Max *float64
}

// Source says where a measured value came from.
type Source struct {
	StageIndex *int
	File       string
}

// Node is one value of a derivation graph.
type Node struct {
	ID    string
	Kind  Kind
	Label string
	Unit  string
	Value float64
	// Derived only.
	Formula  string
	Inputs   []string
	Rendered string
	// IsPinned is set when a derived value was overridden instead of derived;
	// Derivable then holds what the formula gives.
	IsPinned  bool
	Derivable *float64
	// Parameter only.
	Default *float64
	Bounds  *Bounds
	Doc     string
	// Measured only.
	Source *Source
	// IsHypothetical is set on a hypothetical node and on everything that
	// depends on one.
	IsHypothetical bool
}

// Graph is a set of nodes. Build it with the Add methods, then Evaluate.
type Graph struct {
	nodes   map[string]*Node
	parsed  map[string]Expr
	ordered []string // insertion order, for stable output
	err     error
}

// NewGraph returns an empty graph.
func NewGraph() *Graph {
	return &Graph{nodes: map[string]*Node{}, parsed: map[string]Expr{}}
}

func (g *Graph) add(node *Node) {
	if g.err != nil {
		return
	}
	if node.ID == "" {
		g.err = errors.New("a node has no id")
		return
	}
	if _, exists := g.nodes[node.ID]; exists {
		g.err = fmt.Errorf("node %q is defined twice", node.ID)
		return
	}
	g.nodes[node.ID] = node
	g.ordered = append(g.ordered, node.ID)
}

// AddMeasured adds a value from the run.
func (g *Graph) AddMeasured(id, label, unit string, value float64, source *Source) {
	g.add(&Node{ID: id, Kind: Measured, Label: label, Unit: unit, Value: value, Source: source})
}

// AddParameter adds a tunable input at its default.
func (g *Graph) AddParameter(id, label, unit string, value float64, bounds *Bounds, doc string) {
	if bounds != nil && !bounds.contains(value) {
		g.err = errors.Join(g.err, fmt.Errorf("parameter %q default %s is outside its bounds", id, FormatNumber(value)))
		return
	}
	defaultValue := value
	g.add(&Node{ID: id, Kind: Parameter, Label: label, Unit: unit, Value: value, Default: &defaultValue, Bounds: bounds, Doc: doc})
}

// AddDerived adds a formula over other nodes.
func (g *Graph) AddDerived(id, label, unit, formula string) {
	expr, err := Parse(formula)
	if err != nil {
		g.err = errors.Join(g.err, fmt.Errorf("node %q: %w", id, err))
		return
	}
	g.parsed[id] = expr
	g.add(&Node{ID: id, Kind: Derived, Label: label, Unit: unit, Formula: formula, Inputs: Identifiers(expr)})
}

func (b *Bounds) contains(value float64) bool {
	return (b.Min == nil || value >= *b.Min) && (b.Max == nil || value <= *b.Max)
}

// OverrideKind is what an override does to its node.
type OverrideKind string

const (
	// SetParameter changes a parameter within its bounds.
	SetParameter OverrideKind = "parameter"
	// Pin fixes a derived node's value; its consequences are evaluated instead.
	Pin OverrideKind = "pin"
	// Assume replaces a measured value with a hypothetical one.
	Assume OverrideKind = "hypothetical"
)

// Override is one change a scenario makes.
type Override struct {
	Node  string
	Kind  OverrideKind
	Value float64
}

// Result is an evaluated graph: every node with its value and rendering, in
// the order the nodes were added.
type Result struct {
	Nodes []Node
	index map[string]int
}

// Node returns an evaluated node by id.
func (r *Result) Node(id string) (Node, bool) {
	i, ok := r.index[id]
	if !ok {
		return Node{}, false
	}
	return r.Nodes[i], true
}

// Value returns an evaluated node's value.
func (r *Result) Value(id string) (float64, bool) {
	node, ok := r.Node(id)
	return node.Value, ok
}

// Evaluate computes every derived node, applying the overrides. The graph
// itself is not changed, so it can be evaluated again with other overrides.
func (g *Graph) Evaluate(overrides ...Override) (*Result, error) {
	if g.err != nil {
		return nil, g.err
	}
	nodes := make(map[string]*Node, len(g.nodes))
	for id, node := range g.nodes {
		copied := *node
		nodes[id] = &copied
	}
	pinned, err := applyOverrides(nodes, overrides)
	if err != nil {
		return nil, err
	}
	order, err := g.topologicalOrder()
	if err != nil {
		return nil, err
	}
	env := func(id string) (float64, bool) {
		node, ok := nodes[id]
		if !ok {
			return 0, false
		}
		return node.Value, true
	}
	for _, id := range order {
		node := nodes[id]
		if node.Kind != Derived {
			continue
		}
		expr := g.parsed[id]
		value, err := Eval(expr, env)
		if err != nil {
			return nil, fmt.Errorf("node %q (%s): %w", id, node.Formula, err)
		}
		node.Rendered = Render(expr, env) + " = " + FormatNumber(value)
		for _, input := range node.Inputs {
			node.IsHypothetical = node.IsHypothetical || nodes[input].IsHypothetical
		}
		if pin, isPinned := pinned[id]; isPinned {
			derivable := value
			node.IsPinned, node.Derivable, node.Value = true, &derivable, pin
			node.Rendered = "pinned at " + FormatNumber(pin) + "; derived: " + node.Rendered
			continue
		}
		node.Value = value
	}
	result := &Result{index: map[string]int{}}
	for i, id := range g.ordered {
		result.Nodes = append(result.Nodes, *nodes[id])
		result.index[id] = i
	}
	return result, nil
}

func applyOverrides(nodes map[string]*Node, overrides []Override) (map[string]float64, error) {
	pinned := map[string]float64{}
	var problems []error
	for _, override := range overrides {
		node, ok := nodes[override.Node]
		if !ok {
			problems = append(problems, fmt.Errorf("override of unknown node %q", override.Node))
			continue
		}
		switch {
		case override.Kind == SetParameter && node.Kind == Parameter:
			if node.Bounds != nil && !node.Bounds.contains(override.Value) {
				problems = append(problems, fmt.Errorf("%s = %s is outside its bounds", override.Node, FormatNumber(override.Value)))
				continue
			}
			node.Value = override.Value
		case override.Kind == Pin && node.Kind == Derived:
			pinned[override.Node] = override.Value
		case override.Kind == Assume && node.Kind == Measured:
			node.Kind, node.Value, node.IsHypothetical = Hypothetical, override.Value, true
		default:
			problems = append(problems, fmt.Errorf("a %s override does not apply to %s node %q", override.Kind, node.Kind, override.Node))
		}
	}
	return pinned, errors.Join(problems...)
}

// topologicalOrder orders nodes so every input comes before its users, and
// reports unknown inputs and cycles.
func (g *Graph) topologicalOrder() ([]string, error) {
	const (
		unvisited = iota
		visiting
		done
	)
	state := map[string]int{}
	var order []string
	var visit func(id string, path []string) error
	visit = func(id string, path []string) error {
		switch state[id] {
		case done:
			return nil
		case visiting:
			return fmt.Errorf("cycle: %s", strings.Join(append(path, id), " → "))
		}
		state[id] = visiting
		for _, input := range g.nodes[id].Inputs {
			if _, known := g.nodes[input]; !known {
				return fmt.Errorf("node %q reads unknown node %q", id, input)
			}
			if err := visit(input, append(path, id)); err != nil {
				return err
			}
		}
		state[id] = done
		order = append(order, id)
		return nil
	}
	ids := append([]string(nil), g.ordered...)
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id, nil); err != nil {
			return nil, err
		}
	}
	return order, nil
}
