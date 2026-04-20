package fiberuow

import "fmt"

// StatusError marks a transport-driven rollback decision derived from a Fiber
// response status code.
type StatusError struct {
	StatusCode int
}

// Error implements error.
func (e *StatusError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("fiberuow: rollback requested for HTTP status %d", e.StatusCode)
}

// RollbackOn5xx rolls back for HTTP 5xx responses.
func RollbackOn5xx(statusCode int) bool {
	return statusCode >= 500 && statusCode <= 599
}

// RollbackOn4xx5xx rolls back for HTTP 4xx and 5xx responses.
func RollbackOn4xx5xx(statusCode int) bool {
	return statusCode >= 400 && statusCode <= 599
}

// RollbackOnStatusCodes returns a matcher for specific status codes.
func RollbackOnStatusCodes(statusCodes ...int) func(int) bool {
	set := make(map[int]struct{}, len(statusCodes))
	for _, code := range statusCodes {
		set[code] = struct{}{}
	}
	return func(statusCode int) bool {
		_, ok := set[statusCode]
		return ok
	}
}

// RollbackOnStatusRange returns a matcher for an inclusive status range.
func RollbackOnStatusRange(min, max int) func(int) bool {
	return func(statusCode int) bool {
		return statusCode >= min && statusCode <= max
	}
}
