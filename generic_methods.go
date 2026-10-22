//go:build go1.27

package req

// Decode sends r, requires a 2xx status, and decodes the JSON response body
// into T.
//
// It is the generic method counterpart of Request.ToJSON. Go 1.27 introduced
// generic method declarations, so this method is only available when building
// with Go 1.27 or newer. Older toolchains can use the function-style helper
// ToJSON.
//
// A 204 No Content or other empty body returns the zero value of T without an
// error.
//
//	user, err := client.Get(ctx, "/users/1").Decode[User]()
func (r *Request) Decode[T any]() (T, error) {
	return ToJSON[T](r)
}

// DecodePtr is like Decode but returns a pointer, so an empty response can be
// distinguished from a decoded zero value.
//
// A 204 No Content response returns (nil, nil).
//
//	user, err := client.Get(ctx, "/users/1").DecodePtr[User]()
func (r *Request) DecodePtr[T any]() (*T, error) {
	return ToJSONPtr[T](r)
}

// DecodeWithError sends r and decodes either a successful response into T or a
// non-2xx error response into E.
//
// On a non-2xx status the returned error is a *TypedStatusError[E]. It still
// matches *StatusError through errors.As.
//
//	user, err := client.Get(ctx, "/users/1").DecodeWithError[User, APIError]()
//	var apiErr *req.TypedStatusError[APIError]
//	if errors.As(err, &apiErr) {
//	    fmt.Println(apiErr.StatusCode, apiErr.Data.Message)
//	}
//
// A target supplied to Request.ErrorJSON is not used by this method.
func (r *Request) DecodeWithError[T, E any]() (T, error) {
	return ToJSONWithError[T, E](r)
}
