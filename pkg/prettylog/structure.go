package prettylog

import (
	"log/slog"
	"time"
)

// Structure is the reserved part of a record. Every consumer reads reserved
// keys only through it, so all of them interpret a record the same way: the
// last top-level occurrence of a reserved key wins, and a value of the wrong
// type leaves the key absent (the attribute is then shown as a normal field).
type Structure struct {
	Actor, Executor, Outcome, Step, Parent, State, Err, Reason string
	Took                                                       time.Duration
	Detail, Snapshot, Truncated                                bool
	Repeat                                                     int
	Until, Started                                             time.Time
	Omitted                                                    int64
	Overflow                                                   int
	Kind                                                       Kind
}

// StructureOf parses the reserved attributes of r. Only the last top-level
// occurrence of each reserved key counts; if its value has the wrong type the
// key is absent, whatever earlier occurrences said.
func StructureOf(r slog.Record) Structure {
	var s Structure
	eachAttr(r, func(a slog.Attr, role attrRole) bool {
		if role == roleReserved {
			s.apply(a)
		}
		return true
	})
	s.Kind = kindOf(r.Level, s.Outcome, s.State)
	return s
}

var reservedKeys = map[string]bool{
	KeyActor: true, KeyExecutor: true, KeyOutcome: true, KeyStep: true, KeyParent: true, KeyState: true,
	KeyTook: true, KeyErr: true, KeyDetail: true, KeyReason: true, KeyRepeat: true, KeyUntil: true,
	KeySnapshot: true, KeyStarted: true, KeyOmitted: true, KeyOverflow: true, KeyTruncated: true,
}

// attrRole is what a top-level attribute is to every consumer.
type attrRole int

const (
	roleField      attrRole = iota // an ordinary field, shown and encoded as such
	roleReserved                   // the last occurrence of a reserved key, valid: structure
	roleSuperseded                 // an earlier occurrence of a reserved key: ignored everywhere
)

// eachAttr walks r's top-level attributes with their roles. The last
// occurrence of a reserved key wins; when its value has the wrong type it is
// an ordinary field and the key is absent from the structure.
func eachAttr(r slog.Record, fn func(slog.Attr, attrRole) bool) {
	var count map[string]int
	r.Attrs(func(a slog.Attr) bool {
		if reservedKeys[a.Key] {
			if count == nil {
				count = map[string]int{}
			}
			count[a.Key]++
		}
		return true
	})
	r.Attrs(func(a slog.Attr) bool {
		role := roleField
		if reservedKeys[a.Key] {
			count[a.Key]--
			switch {
			case count[a.Key] > 0:
				role = roleSuperseded
			case validReserved(a):
				role = roleReserved
			}
		}
		return fn(a, role)
	})
}

// validReserved reports whether a reserved attribute's value has its type.
func validReserved(a slog.Attr) bool {
	var s Structure
	return s.apply(a)
}

func (s *Structure) apply(a slog.Attr) bool {
	v := a.Value.Resolve()
	str := func(dst *string) bool {
		if v.Kind() != slog.KindString {
			return false
		}
		*dst = v.String()
		return true
	}
	flag := func(dst *bool) bool {
		if v.Kind() != slog.KindBool {
			return false
		}
		*dst = v.Bool()
		return true
	}
	integer := func() (int64, bool) {
		switch v.Kind() {
		case slog.KindInt64:
			return v.Int64(), true
		case slog.KindUint64:
			if u := v.Uint64(); u <= 1<<62 {
				return int64(u), true
			}
		}
		return 0, false
	}
	moment := func(dst *time.Time) bool {
		if v.Kind() != slog.KindTime {
			return false
		}
		*dst = v.Time()
		return true
	}
	switch a.Key {
	case KeyActor:
		return str(&s.Actor)
	case KeyExecutor:
		return str(&s.Executor)
	case KeyOutcome:
		return str(&s.Outcome)
	case KeyStep:
		return str(&s.Step)
	case KeyParent:
		return str(&s.Parent)
	case KeyState:
		return str(&s.State)
	case KeyErr:
		return str(&s.Err)
	case KeyReason:
		return str(&s.Reason)
	case KeyTook:
		if v.Kind() != slog.KindDuration {
			return false
		}
		s.Took = v.Duration()
		return true
	case KeyDetail:
		return flag(&s.Detail)
	case KeySnapshot:
		return flag(&s.Snapshot)
	case KeyTruncated:
		return flag(&s.Truncated)
	case KeyRepeat:
		n, ok := integer()
		s.Repeat = int(n)
		return ok
	case KeyOverflow:
		n, ok := integer()
		s.Overflow = int(n)
		return ok
	case KeyOmitted:
		n, ok := integer()
		s.Omitted = n
		return ok
	case KeyUntil:
		return moment(&s.Until)
	case KeyStarted:
		return moment(&s.Started)
	}
	return false
}

// transition reports whether the record is a step transition or an outcome.
func (s Structure) transition() bool { return s.State != "" || s.Outcome != "" }

// IsLossy reports whether a transport may drop r: only a step's executor
// output line is lossy. Transitions, outcomes and snapshots never are.
func IsLossy(r slog.Record) bool {
	s := StructureOf(r)
	return s.lossy()
}

func (s Structure) lossy() bool { return s.Detail && !s.transition() && !s.Snapshot }
