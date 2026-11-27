package derive

import "github.com/servtuner/servtuner/bench/internal/spec"

// SpecNodes writes an evaluated graph in the schema's form
// (recommendations.schema.json, derivationNode).
func (r *Result) SpecNodes() []spec.DerivationNode {
	nodes := make([]spec.DerivationNode, 0, len(r.Nodes))
	for _, node := range r.Nodes {
		nodes = append(nodes, specNode(node))
	}
	return nodes
}

func specNode(node Node) spec.DerivationNode {
	out := spec.DerivationNode{Id: node.ID, Kind: spec.DerivationNodeKind(node.Kind), Label: node.Label, Value: node.Value}
	if node.Label == "" {
		out.Label = node.ID
	}
	out.Unit = optional(node.Unit)
	out.Doc = optional(node.Doc)
	if node.Kind == Derived {
		out.Formula, out.Rendered = optional(node.Formula), optional(node.Rendered)
		out.Inputs = append([]string{}, node.Inputs...)
		if node.IsPinned {
			isPinned := true
			out.IsPinned, out.DerivableValue = &isPinned, node.Derivable
		}
	}
	if node.Kind == Parameter {
		out.Default = node.Default
		if node.Bounds != nil {
			out.Bounds = &spec.DerivationBounds{Min: node.Bounds.Min, Max: node.Bounds.Max}
		}
	}
	if node.Source != nil {
		out.Source = &spec.DerivationNodeSource{StageIndex: node.Source.StageIndex, File: optional(node.Source.File)}
	}
	if node.IsHypothetical {
		isHypothetical := true
		out.IsHypothetical = &isHypothetical
	}
	return out
}

func optional(text string) *string {
	if text == "" {
		return nil
	}
	return &text
}
