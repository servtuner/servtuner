package spec

// Burst reports whether the stage is a burst probe (is_burst); an absent
// field means an ordinary stage.
func (s Stage) Burst() bool { return s.IsBurst != nil && *s.IsBurst }
