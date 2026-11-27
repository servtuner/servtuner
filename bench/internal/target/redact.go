package target

import (
	"regexp"
	"sort"
	"strings"
)

// RedactedPlaceholder replaces a secret value wherever a value must stay present
// in a document (CLAUDE.md rule 4: never store secrets in a run).
const RedactedPlaceholder = "[REDACTED]"

// secretNamePattern is the one definition of "this name holds a secret". Over-
// redacting a harmless value costs a little analysis detail; leaking a secret
// into a bundle is permanent, so the pattern is deliberately broad.
var secretNamePattern = regexp.MustCompile(`(key|token|secret|passw(or)?d|credential|auth|bearer)`)

// notSecretNameParts are removed from a name before it is tested. They contain
// "token" but are counts or file names, and vLLM's --max-num-batched-tokens is
// a knob the declared-vs-actual check needs to read.
var notSecretNameParts = []string{"tokens", "tokenizer"}

// urlWithPassword matches a URL carrying credentials, for example
// scheme://user:password@host.
var urlWithPassword = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^/\s:@]+:[^/\s@]+@`)

// IsSecretName reports whether a variable, flag, header or config field name
// says its value is a secret. Every redaction in this package decides through
// this function.
func IsSecretName(name string) bool {
	lowered := strings.ToLower(name)
	for _, part := range notSecretNameParts {
		lowered = strings.ReplaceAll(lowered, part, "")
	}
	return secretNamePattern.MatchString(lowered)
}

// hasEmbeddedCredentials reports whether a string value carries a URL with a
// password, whatever the name of the field holding it.
func hasEmbeddedCredentials(value string) bool {
	return urlWithPassword.MatchString(value)
}

// EnvEntry is one environment variable as stored in a snapshot. A redacted one
// carries no value (schema envVar).
type EnvEntry struct {
	Name       string  `json:"name"`
	IsRedacted bool    `json:"is_redacted"`
	Value      *string `json:"value,omitempty"`
}

// RedactEnv builds the stored form of an environment variable.
//
// isIndirect is true when the value comes from valueFrom (a secretKeyRef, but
// also a configMapKeyRef, fieldRef or resourceFieldRef). A secretKeyRef is
// always redacted. The others are redacted too, because snapshot does not
// resolve them: a stored value would be absent, and "redacted" says so without
// claiming the value was empty.
func RedactEnv(name, value string, isIndirect bool) EnvEntry {
	if isIndirect || IsSecretName(name) || hasEmbeddedCredentials(value) {
		return EnvEntry{Name: name, IsRedacted: true}
	}
	return EnvEntry{Name: name, Value: &value}
}

// RedactArgs returns a copy of container args with secret flag values replaced.
// It handles "--api-key value" and "--api-key=value".
func RedactArgs(args []string) []string {
	redacted := make([]string, 0, len(args))
	redactNext := false
	for _, argument := range args {
		switch {
		case redactNext:
			redactNext = false
			redacted = append(redacted, RedactedPlaceholder)
		case strings.HasPrefix(argument, "-"):
			flagName, _, hasValue := strings.Cut(strings.TrimLeft(argument, "-"), "=")
			switch {
			case IsSecretName(flagName) && hasValue:
				redacted = append(redacted, argument[:strings.Index(argument, "=")+1]+RedactedPlaceholder)
			case IsSecretName(flagName):
				redactNext = true
				redacted = append(redacted, argument)
			case hasEmbeddedCredentials(argument):
				redacted = append(redacted, RedactedPlaceholder)
			default:
				redacted = append(redacted, argument)
			}
		case hasEmbeddedCredentials(argument):
			redacted = append(redacted, RedactedPlaceholder)
		default:
			redacted = append(redacted, argument)
		}
	}
	return redacted
}

// RedactJSON returns a redacted deep copy of a decoded JSON document (the
// result of json.Unmarshal into any), for example Bifrost's config:
//
//   - a string or number under a secret-named field is replaced;
//   - inside a secret-named object or array of objects (Bifrost's provider
//     "keys"), the "value" field is replaced, and so is any secret-named field;
//   - scalars in an array under a secret-named field are replaced;
//   - a string holding a URL with a password is replaced.
//
// Booleans and nulls under secret-named fields stay: they hold no secret and
// say whether, for example, authentication is enabled.
func RedactJSON(document any) any {
	return redactJSONValue(document, false)
}

func redactJSONValue(value any, isUnderSecretName bool) any {
	switch typed := value.(type) {
	case map[string]any:
		copied := make(map[string]any, len(typed))
		for _, fieldName := range sortedKeys(typed) {
			child := typed[fieldName]
			isSecretField := IsSecretName(fieldName) || (isUnderSecretName && fieldName == "value")
			if isSecretField && isRedactableScalar(child) {
				copied[fieldName] = RedactedPlaceholder
				continue
			}
			copied[fieldName] = redactJSONValue(child, isSecretField)
		}
		return copied
	case []any:
		copied := make([]any, len(typed))
		for index, element := range typed {
			if isUnderSecretName && isRedactableScalar(element) {
				copied[index] = RedactedPlaceholder
				continue
			}
			copied[index] = redactJSONValue(element, isUnderSecretName)
		}
		return copied
	case string:
		if hasEmbeddedCredentials(typed) {
			return RedactedPlaceholder
		}
		return typed
	default:
		return value
	}
}

func isRedactableScalar(value any) bool {
	switch value.(type) {
	case string, float64:
		return true
	default:
		return false
	}
}

func sortedKeys[V any](mapping map[string]V) []string {
	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
