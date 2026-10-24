package ftransport

import (
	"errors"
	"fmt"
)

var (
	// ErrTransportClosed reports use of a Transport after its full Close or an
	// active operation interrupted by that Close.
	ErrTransportClosed = errors.New("ftransport: transport closed")

	// ErrInvalidConfig classifies constructor failures caused by invalid or
	// internally inconsistent configuration.
	ErrInvalidConfig = errors.New("ftransport: invalid configuration")

	// ErrUnsupportedFeature classifies valid contract fields whose wire
	// implementation is intentionally not available in the current phase.
	ErrUnsupportedFeature = errors.New("ftransport: unsupported feature")

	// ErrInvalidDialResult classifies custom dialer contract violations.
	ErrInvalidDialResult = errors.New("ftransport: invalid dial result")
)

// ConfigError identifies the configuration field that prevented construction.
type ConfigError struct {
	Field string
	Err   error
}

func (e *ConfigError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("%v: %v", ErrInvalidConfig, e.Err)
	}
	return fmt.Sprintf("%v: %s: %v", ErrInvalidConfig, e.Field, e.Err)
}

func (e *ConfigError) Unwrap() []error {
	return []error{ErrInvalidConfig, e.Err}
}

// UnsupportedFeatureError names a feature that is not yet implemented.
type UnsupportedFeatureError struct {
	Feature string
}

func (e *UnsupportedFeatureError) Error() string {
	return fmt.Sprintf("%v: %s", ErrUnsupportedFeature, e.Feature)
}

func (e *UnsupportedFeatureError) Unwrap() error { return ErrUnsupportedFeature }

// OperationError adds a stable operation and protocol classification while
// preserving the original error for errors.Is and errors.As.
type OperationError struct {
	Operation string
	Protocol  Protocol
	Err       error
}

func (e *OperationError) Error() string {
	if e.Protocol == 0 {
		return fmt.Sprintf("ftransport: %s: %v", e.Operation, e.Err)
	}
	return fmt.Sprintf("ftransport: %s %s: %v", e.Protocol, e.Operation, e.Err)
}

func (e *OperationError) Unwrap() error { return e.Err }
