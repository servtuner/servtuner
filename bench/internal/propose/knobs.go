package propose

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// KnobValue is a knob's value: a string, a number or a boolean.
type KnobValue = spec.KnobValue

// Knobs is a knob set: the RunSpec's knobs, by name.
type Knobs map[string]KnobValue

// configIDHexLength is how many hex digits of the knob set's hash name a
// configuration: 8 digits (32 bits) keep ids readable and collisions
// negligible within one session.
const configIDHexLength = 8

// key is a canonical encoding: encoding/json sorts the keys.
func (k Knobs) key() string {
	encoded, _ := json.Marshal(map[string]KnobValue(k)) // knob values are strings, numbers or booleans
	return string(encoded)
}

// id is a short, stable name for the knob set.
func (k Knobs) id() string {
	sum := sha256.Sum256([]byte(k.key()))
	return "c-" + hex.EncodeToString(sum[:])[:configIDHexLength]
}

func (k Knobs) clone() Knobs {
	copied := make(Knobs, len(k))
	for name, value := range k {
		copied[name] = value
	}
	return copied
}

// with returns a copy with one knob set.
func (k Knobs) with(name string, value KnobValue) Knobs {
	copied := k.clone()
	copied[name] = value
	return copied
}

// contains reports whether every knob of sub is set to the same value here.
func (k Knobs) contains(sub Knobs) bool {
	for name, value := range sub {
		have, ok := k[name]
		if !ok || !sameValue(have, value) {
			return false
		}
	}
	return true
}

func (k Knobs) number(name string) (float64, bool) {
	number, ok := k[name].(float64)
	return number, ok
}

func sameValue(a, b KnobValue) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

// fromSpecKnobs copies a RunSpec's knobs (numbers decode as float64).
func fromSpecKnobs(declared spec.RunSpecKnobs) Knobs {
	knobs := make(Knobs, len(declared))
	for name, value := range declared {
		knobs[name] = value
	}
	return knobs
}
