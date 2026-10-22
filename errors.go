package req

import (
	"errors"
	"fmt"
)

var (
	// ErrConfiguration reports an invalid Client configuration.
	//
	// Configuration errors are always returned when a request is built or
	// executed. NewWithOptions also returns them immediately.
	ErrConfiguration = errors.New("req: configuration error")

	// ErrNoBaseURL reports that a relative URL was used without a base URL.
	ErrNoBaseURL = errors.New("req: relative URL requires a base URL")

	// ErrResponseTooLarge reports that a response body exceeded the configured
	// limit for the ToJSON, ToBytes, or ToString helpers.
	//
	// Do and Build are not affected by this limit.
	ErrResponseTooLarge = errors.New("req: response body exceeds size limit")

	// ErrErrorBodyDecode reports that a non-2xx response body could not be
	// decoded into the target supplied to Request.ErrorJSON or
	// StatusError.DecodeJSON.
	//
	// The *StatusError and its raw body remain available through errors.As,
	// so a malformed error payload never hides the HTTP status.
	ErrErrorBodyDecode = errors.New("req: decode error response JSON")
)

func configError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrConfiguration, fmt.Sprintf(format, args...))
}

func wrapConfig(err error, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %w", ErrConfiguration, fmt.Sprintf(format, args...), err)
}
