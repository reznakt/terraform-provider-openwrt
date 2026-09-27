package ubus

import (
	"errors"
	"fmt"
)

// Status is a ubus status code (libubus enum ubus_msg_status).
type Status int

const (
	StatusOK               Status = 0
	StatusInvalidCommand   Status = 1
	StatusInvalidArgument  Status = 2
	StatusMethodNotFound   Status = 3
	StatusNotFound         Status = 4
	StatusNoData           Status = 5
	StatusPermissionDenied Status = 6
	StatusTimeout          Status = 7
	StatusNotSupported     Status = 8
	StatusUnknownError     Status = 9
	StatusConnectionFailed Status = 10
)

var statusNames = map[Status]string{
	StatusOK:               "ok",
	StatusInvalidCommand:   "invalid command",
	StatusInvalidArgument:  "invalid argument",
	StatusMethodNotFound:   "method not found",
	StatusNotFound:         "not found",
	StatusNoData:           "no data",
	StatusPermissionDenied: "permission denied",
	StatusTimeout:          "timeout",
	StatusNotSupported:     "not supported",
	StatusUnknownError:     "unknown error",
	StatusConnectionFailed: "connection failed",
}

func (s Status) String() string {
	if n, ok := statusNames[s]; ok {
		return n
	}
	return fmt.Sprintf("status %d", int(s))
}

// Error is a non-zero ubus status returned for a call.
type Error struct {
	Object string
	Method string
	Status Status
	cause  error
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("ubus: %s.%s: %s", e.Object, e.Method, e.Status)
	if e.Status == StatusPermissionDenied {
		msg += " (check the rpcd ACL of the provider's login)"
	}
	if e.cause != nil {
		msg += ": " + e.cause.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.cause }

// HasStatus reports whether err is a ubus Error with the given status.
func HasStatus(err error, s Status) bool {
	var ue *Error
	return errors.As(err, &ue) && ue.Status == s
}

// IsNotFound reports whether err means the requested object/section/file does not exist.
func IsNotFound(err error) bool { return HasStatus(err, StatusNotFound) }
