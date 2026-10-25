package ftransport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// RequestOptions contains wire-encoding choices that may vary by request
// without changing the connection pool identity.
type RequestOptions struct {
	HeaderOrder       []string
	PseudoHeaderOrder []PseudoHeader
	SensitiveHeaders  []string
}

type requestOptionsContextKey struct{}

// WithRequestOptions clones req, validates options, and attaches an immutable
// copy to the cloned request context. The request body remains the same stream,
// matching http.Request.Clone semantics.
func WithRequestOptions(req *http.Request, options RequestOptions) (*http.Request, error) {
	if req == nil {
		return nil, errors.New("ftransport: nil request")
	}
	if err := options.validate(); err != nil {
		return nil, err
	}
	options = options.clone()
	ctx := context.WithValue(req.Context(), requestOptionsContextKey{}, options)
	return req.Clone(ctx), nil
}

// RequestOptionsFromRequest returns a defensive copy of request options.
func RequestOptionsFromRequest(req *http.Request) (RequestOptions, bool) {
	options, ok := requestOptionsValueFromRequest(req)
	return options.clone(), ok
}

// requestOptionsValueFromRequest returns the immutable value stored by
// WithRequestOptions. Internal callers may inspect its slices but must not
// modify them; the exported accessor above retains its defensive-copy
// contract for callers outside this package.
func requestOptionsValueFromRequest(req *http.Request) (RequestOptions, bool) {
	if req == nil {
		return RequestOptions{}, false
	}
	options, ok := req.Context().Value(requestOptionsContextKey{}).(RequestOptions)
	return options, ok
}

func (o RequestOptions) clone() RequestOptions {
	o.HeaderOrder = slices.Clone(o.HeaderOrder)
	o.PseudoHeaderOrder = slices.Clone(o.PseudoHeaderOrder)
	o.SensitiveHeaders = slices.Clone(o.SensitiveHeaders)
	return o
}

func (o RequestOptions) validate() error {
	if err := validateHeaderOrder("request header order", o.HeaderOrder); err != nil {
		return err
	}
	if err := validatePseudoHeaderOrder(o.PseudoHeaderOrder); err != nil {
		return err
	}
	if err := validateHeaderOrder("sensitive headers", o.SensitiveHeaders); err != nil {
		return err
	}
	return nil
}

func validateHeaderOrder(label string, order []string) error {
	seen := make(map[string]struct{}, len(order))
	for _, name := range order {
		if !validHeaderName(name) {
			return fmt.Errorf("ftransport: %s contains invalid name %q", label, name)
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("ftransport: %s contains duplicate name %q", label, name)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) &&
			!(c >= '0' && c <= '9') &&
			!(c >= 'a' && c <= 'z') &&
			!(c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}
