package loadtest

import (
	"errors"
	"fmt"
	"regexp"
	"sync"
)

// IdentifierPattern is the adapter and engine id grammar.
// Ids are not limited to builtin-get and k6-wrapper. Registration is the
// support check; a string that merely matches this pattern is not supported.
var IdentifierPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

var (
	// ErrUnsupportedAdapter means the adapter id is not registered.
	ErrUnsupportedAdapter = errors.New("unsupported load adapter")
	// ErrMissingCapability means the request needs a capability the adapter does not provide.
	ErrMissingCapability = errors.New("required load capability is not provided")
	// ErrIncompatibleLatency means the request's latency semantics do not match the adapter.
	ErrIncompatibleLatency = errors.New("latency semantics are not supported")
	// ErrInvalidRequest means the request is not a valid load for this contract.
	ErrInvalidRequest = errors.New("load request is invalid")
)

// AdapterSpec is one registered adapter and the engine it wraps.
type AdapterSpec struct {
	Adapter          ComponentIdentity
	Engine           ComponentIdentity
	Capabilities     []Capability
	LatencySemantics LatencySemantics
	ContractVersion  string
}

var (
	registryMu sync.Mutex
	registry   = map[string]AdapterSpec{}
)

// Register adds an adapter id. A second registration of the same id fails.
// This is an in-process table, not a plugin loader.
func Register(spec AdapterSpec) error {
	if err := validateSpec(spec); err != nil {
		return err
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[spec.Adapter.ID]; exists {
		return fmt.Errorf("load adapter %q is already registered", spec.Adapter.ID)
	}
	registry[spec.Adapter.ID] = cloneSpec(spec)
	return nil
}

// Lookup returns a copy of the registered spec.
func Lookup(id string) (AdapterSpec, bool) {
	registryMu.Lock()
	defer registryMu.Unlock()
	spec, ok := registry[id]
	if !ok {
		return AdapterSpec{}, false
	}
	return cloneSpec(spec), true
}

func unregister(id string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	delete(registry, id)
}

func validateSpec(spec AdapterSpec) error {
	if err := validateIdentity("adapter", spec.Adapter); err != nil {
		return err
	}
	if err := validateIdentity("engine", spec.Engine); err != nil {
		return err
	}
	if spec.ContractVersion != ContractVersion {
		return fmt.Errorf("adapter %q contract version %q is not %s", spec.Adapter.ID, spec.ContractVersion, ContractVersion)
	}
	if !spec.LatencySemantics.Known() || spec.LatencySemantics == LatencyUnknown {
		return fmt.Errorf("adapter %q must declare known latency semantics", spec.Adapter.ID)
	}
	seen := make(map[Capability]struct{}, len(spec.Capabilities))
	claimsLatency := false
	for _, capability := range spec.Capabilities {
		if !capability.Known() {
			return fmt.Errorf("adapter %q claims unknown capability %q", spec.Adapter.ID, capability)
		}
		if _, duplicate := seen[capability]; duplicate {
			return fmt.Errorf("adapter %q claims capability %q twice", spec.Adapter.ID, capability)
		}
		seen[capability] = struct{}{}
		if capability == CapabilityLatencyUntilHeaders {
			claimsLatency = true
		}
	}
	if claimsLatency && spec.LatencySemantics != LatencyUntilResponseHeaders {
		return fmt.Errorf("adapter %q claims header latency but declares %s", spec.Adapter.ID, spec.LatencySemantics)
	}
	return nil
}

func validateIdentity(kind string, id ComponentIdentity) error {
	if !IdentifierPattern.MatchString(id.ID) {
		return fmt.Errorf("%s id %q is not a valid identifier", kind, id.ID)
	}
	if id.Version == "" {
		return fmt.Errorf("%s %q requires a version", kind, id.ID)
	}
	return nil
}

func cloneSpec(spec AdapterSpec) AdapterSpec {
	spec.Capabilities = append([]Capability(nil), spec.Capabilities...)
	return spec
}

// HasCapability reports whether spec claims capability.
func HasCapability(spec AdapterSpec, capability Capability) bool {
	for _, candidate := range spec.Capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}
