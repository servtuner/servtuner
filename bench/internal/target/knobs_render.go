package target

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// vllmPrefix is the namespace of the knobs RenderVLLMArgs spells.
const vllmPrefix = "vllm."

// RenderVLLMArgs turns a vLLM knob set into container args: the reverse of
// vllmKnobsFromArgs, driven by the same tables (vllmFlags and
// compilationConfigKnobs), so a rendered flag captures back as the same knob.
//
// Args come in table order, each as one "--flag=value" argument (the long
// spelling), so the output is deterministic and a value can never be read as
// a flag. A boolean true is the bare flag; a false is vLLM's negated spelling
// (falseFlag). Knobs that exist only as a CompilationConfig field are given in
// the dotted form "--compilation-config.<field>=<value>": it sets one field,
// so it composes with any other compilation config the container carries, and
// it needs no JSON quoting. A knob that has a dedicated flag
// (max_cudagraph_capture_size) is rendered through that flag only, because
// vLLM rejects the flag and the config field together as mutually exclusive.
//
// Every problem is returned, each naming its knob: a non-vLLM knob, an unknown
// knob, a value of the wrong type for its kind, and a false vLLM cannot say.
func RenderVLLMArgs(knobs Knobs) ([]string, error) {
	if err := checkRenderableNames(knobs); err != nil {
		return nil, err
	}
	var args []string
	var problems []error
	render := func(knob, flag string, kind knobKind) {
		value, set := knobs[knob]
		if !set {
			return
		}
		rendered, err := renderFlag(knob, flag, kind, value)
		if err != nil {
			problems = append(problems, err)
			return
		}
		args = append(args, rendered...)
	}
	dedicated := map[string]bool{}
	for _, flag := range vllmFlags {
		dedicated[flag.knob] = true
		render(flag.knob, flag.flags[0], flag.kind)
	}
	for _, field := range compilationConfigKnobs {
		if !dedicated[field.knob] {
			render(field.knob, compilationConfigFlags[0]+"."+field.field, field.kind)
		}
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return args, nil
}

// checkRenderableNames rejects, in name order, every knob the tables do not
// know.
func checkRenderableNames(knobs Knobs) error {
	known := map[string]bool{}
	for _, flag := range vllmFlags {
		known[flag.knob] = true
	}
	for _, field := range compilationConfigKnobs {
		known[field.knob] = true
	}
	names := make([]string, 0, len(knobs))
	for name := range knobs {
		names = append(names, name)
	}
	sort.Strings(names)
	var problems []error
	for _, name := range names {
		switch {
		case known[name]:
		case !strings.HasPrefix(name, vllmPrefix):
			problems = append(problems, fmt.Errorf("knob %q is not a vLLM knob: it has no vLLM flag", name))
		case servtunerOwnedFields[strings.TrimPrefix(name, vllmPrefix)]:
			problems = append(problems, fmt.Errorf("knob %q is not a knob: servtuner sets --%s itself", name,
				strings.ReplaceAll(strings.TrimPrefix(name, vllmPrefix), "_", "-")))
		default:
			problems = append(problems, fmt.Errorf("knob %q is not a known vLLM knob", name))
		}
	}
	return errors.Join(problems...)
}

// renderFlag spells one knob. The knob's value must have the Go type its kind
// captures as (float64, bool or string).
func renderFlag(knob, flag string, kind knobKind, value any) ([]string, error) {
	switch kind {
	case knobBoolean, knobSwitch:
		on, ok := value.(bool)
		if !ok {
			return nil, wrongType(knob, "a boolean", value)
		}
		switch {
		case on:
			return []string{flag}, nil
		case kind == knobSwitch:
			return nil, fmt.Errorf("knob %q cannot be false: vLLM's %s has no off spelling, so leave the knob unset", knob, flag)
		}
		return []string{falseFlag(flag)}, nil
	case knobNumber, knobInteger, knobSize:
		number, ok := value.(float64)
		if !ok {
			return nil, wrongType(knob, "a number", value)
		}
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, fmt.Errorf("knob %q: %v is not a finite number", knob, number)
		}
		if kind == knobSize && (number < 0 || number != math.Trunc(number)) {
			return nil, fmt.Errorf("knob %q: a size must be a whole, non-negative number, got %v", knob, number)
		}
		if kind == knobInteger && number != math.Trunc(number) {
			return nil, fmt.Errorf("knob %q: vLLM takes a whole number, got %v", knob, number)
		}
		// 'f' with -1 is the shortest form that reads back exactly: "256", "0.9".
		return []string{flag + "=" + strconv.FormatFloat(number, 'f', -1, 64)}, nil
	default:
		text, ok := value.(string)
		if !ok {
			return nil, wrongType(knob, "a string", value)
		}
		if text == "" || text != strings.TrimSpace(text) {
			return nil, fmt.Errorf("knob %q: %q is empty or has surrounding whitespace", knob, text)
		}
		return []string{flag + "=" + text}, nil
	}
}

func wrongType(knob, want string, value any) error {
	return fmt.Errorf("knob %q needs %s, got %v (%T)", knob, want, value, value)
}

// falseFlag is the spelling that turns a boolean flag off. vLLM's
// EnableDisableAction pairs "--enable-x" with "--disable-x" both ways and
// leaves "--no-x" for a flag with neither prefix; negatedFlags lists the
// "--no-" form first and the paired form last, and the paired form is the one
// vLLM shows in its help.
func falseFlag(flag string) string {
	negated := negatedFlags(flag)
	return negated[len(negated)-1]
}
