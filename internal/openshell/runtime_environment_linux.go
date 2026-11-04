package openshell

import (
	"errors"
	"sort"
	"strings"
)

// captureBoundaryEnvironment retains image/container ENV for the canonical
// workload and its PATH for later SSH exec. Account secrets stay in the
// private bootstrap child_env map; no OPENSHELL_ control channel is inherited.
// Only verified PID 1 calls this with its own actual container environment.
func captureBoundaryEnvironment(inherited []string) ([]string, error) {
	values := make(map[string]string, len(inherited))
	total := 0
	for _, entry := range inherited {
		total += len(entry) + 1
		if total > maxRuntimeChildBytes {
			return nil, errors.New("container environment exceeds private child channel bound")
		}
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsRune(entry, 0) {
			return nil, errors.New("invalid container environment entry")
		}
		if !strings.HasPrefix(key, "OPENSHELL_") {
			values[key] = value
		}
	}
	return orderedEnvironment(values), nil
}

func orderedEnvironment(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func validateBoundaryEnvironment(environment []string) error {
	total, previous := 0, ""
	for _, entry := range environment {
		total += len(entry) + 1
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsRune(entry, 0) || strings.HasPrefix(key, "OPENSHELL_") || key <= previous || total > maxRuntimeChildBytes {
			return errors.New("private child environment is invalid, unordered or contains runtime control state")
		}
		previous = key
	}
	return nil
}

// runtimeBoundaryEnvironment is called only after the sealed, role-specific
// inherited payload has been validated. It never consults re-exec ambient env.
// The supervisor continues to receive only SupervisorEnvironment().
func runtimeBoundaryEnvironment(environment []string) ([]string, error) {
	if err := validateBoundaryEnvironment(environment); err != nil {
		return nil, err
	}
	values := make(map[string]string, len(environment)+3)
	for _, entry := range append(BoundaryEnvironment(), environment...) {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	return orderedEnvironment(values), nil
}
