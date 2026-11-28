package target

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// RenderSGLangArgs spells SGLang knobs as server args, through SGLang's
// catalog: the flag and kind of each knob's field. A switch (store_true) is
// its flag when true and nothing when false, SGLang's default; every other
// value is --flag=value, as for vLLM. A name that is not an SGLang knob is an
// error, listing every one.
func RenderSGLangArgs(knobs Knobs) ([]string, error) {
	names := make([]string, 0, len(knobs))
	for name := range knobs {
		names = append(names, name)
	}
	sort.Strings(names)
	catalog := sglangKnobsByName()
	var args []string
	var problems []error
	for _, name := range names {
		flag, known := catalog[name]
		if !known {
			problems = append(problems, sglangKnobProblem(name))
			continue
		}
		kind := knobKinds[flag.Kind]
		if kind == knobSwitch {
			on, ok := knobs[name].(bool)
			if !ok {
				problems = append(problems, wrongType(name, "a boolean", knobs[name]))
			} else if on {
				args = append(args, flag.Flag)
			}
			continue
		}
		rendered, err := renderFlag(name, flag.Flag, kind, knobs[name])
		if err != nil {
			problems = append(problems, err)
			continue
		}
		args = append(args, rendered...)
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return args, nil
}

func sglangKnobProblem(name string) error {
	field, isSGLang := strings.CutPrefix(name, sglangPrefix)
	switch {
	case !isSGLang:
		return fmt.Errorf("knob %q is not an SGLang knob: it has no SGLang flag", name)
	case sglangOwnedFields[field] || IsSecretName(field):
		return fmt.Errorf("knob %q is not a knob: servtuner sets --%s itself, or it holds a secret", name, strings.ReplaceAll(field, "_", "-"))
	default:
		return fmt.Errorf("knob %q is not a known SGLang knob", name)
	}
}
