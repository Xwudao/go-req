package req

import "errors"

// ToJSON sends r, requires a 2xx status, and decodes the response body into T.
//
// It is the function-style counterpart of Request.Decode. Generic methods
// require Go 1.27, so this function remains available on older toolchains and
// for callers that prefer function-style APIs. An empty body, including a 204
// No Content response, returns the zero value of T without an error.
//
// user, err := req.ToJSON[User](client.Get(ctx, "/users/1"))
func ToJSON[T any](r *Request) (T, error) {
	var value T
	if r == nil {
		return value, errors.New("req: nil request")
	}
	if err := r.ToJSON(&value); err != nil {
		return value, err
	}
	return value, nil
}

// ToJSONPtr is like ToJSON but returns a pointer so an empty response can be
// distinguished from a decoded zero value. It is the function-style counterpart
// of Request.DecodePtr.
//
// A 204 No Content response returns (nil, nil).
func ToJSONPtr[T any](r *Request) (*T, error) {
	if r == nil {
		return nil, errors.New("req: nil request")
	}
	data, err := r.responseBytes()
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	value := new(T)
	if err := decodeJSONResponse(data, value, r.jsonUseNumber, r.jsonStrict); err != nil {
		return nil, err
	}
	return value, nil
}

// ToJSONWithError sends r and decodes either a successful response into T or a
// non-2xx error response into E. It is the function-style counterpart of
// Request.DecodeWithError.
//
// On success it behaves like ToJSON. On a non-2xx status the returned error is
// a *TypedStatusError[E], which embeds *StatusError fields and exposes the
// decoded error payload as Data. It still matches *StatusError through
// errors.As, and decoding failures match ErrErrorBodyDecode.
//
//	type APIError struct {
//	    Code    int    `json:"code"`
//	    Message string `json:"message"`
//	}
//
//	user, err := req.ToJSONWithError[User, APIError](client.Get(ctx, "/users/1"))
//	var apiErr *req.TypedStatusError[APIError]
//	if errors.As(err, &apiErr) {
//	    fmt.Println(apiErr.StatusCode, apiErr.Data.Message)
//	}
//
// A target supplied to Request.ErrorJSON is not used by this function.
func ToJSONWithError[T, E any](r *Request) (T, error) {
	var value T
	if r == nil {
		return value, errors.New("req: nil request")
	}

	data, statusErr, readErr := r.readResponse()
	if statusErr != nil {
		var apiErr E
		var decodeErr error
		if len(statusErr.Body) > 0 {
			decodeErr = decodeErrorJSON(statusErr.Body, &apiErr, r.jsonUseNumber)
		}
		return value, &TypedStatusError[E]{
			StatusError: *statusErr,
			Data:        apiErr,
			DecodeErr:   decodeErr,
			ReadErr:     readErr,
		}
	}
	if readErr != nil {
		return value, readErr
	}
	if len(data) == 0 {
		return value, nil
	}
	if err := decodeJSONResponse(data, &value, r.jsonUseNumber, r.jsonStrict); err != nil {
		return value, err
	}
	return value, nil
}

// TypedStatusError is returned by ToJSONWithError for non-2xx responses.
//
// StatusError is embedded, so StatusCode, Status, Header, Body, BodyTruncated,
// Error, and DecodeJSON are available directly. Data holds the payload decoded
// from the error body; it is the zero value of E when the body is empty or
// cannot be decoded.
type TypedStatusError[T any] struct {
	StatusError
	Data T

	// DecodeErr is set when the error body could not be decoded into Data.
	DecodeErr error

	// ReadErr is set when reading the bounded error body failed.
	ReadErr error
}

// Unwrap exposes the embedded StatusError and any read or decode failure.
// errors.As(err, &statusErr) therefore continues to work with *StatusError.
func (e *TypedStatusError[T]) Unwrap() []error {
	errs := []error{&e.StatusError}
	if e.ReadErr != nil {
		errs = append(errs, e.ReadErr)
	}
	if e.DecodeErr != nil {
		errs = append(errs, e.DecodeErr)
	}
	return errs
}
