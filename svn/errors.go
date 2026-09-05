package svn

import (
	"errors"
	"fmt"
)

//go:generate go run ./internal/generrors -header ../testdata/svn_error_codes.h -output errors_codes.go

type Code int32

func (code Code) String() string {
	if definition, ok := errorDefinitions[code]; ok {
		return definition.name
	}
	return fmt.Sprintf("SVN_ERR_%d", code)
}

func (code Code) Error() string {
	if definition, ok := errorDefinitions[code]; ok {
		return definition.message
	}
	return code.String()
}

func (code Code) DefaultMessage() string { return code.Error() }

type Error struct {
	Code    Code
	Message string
	Child   error
}

func NewError(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

func Wrap(code Code, message string, child error) error {
	if child == nil {
		return nil
	}
	return &Error{Code: code, Message: message, Child: child}
}

func Trace(err error) error {
	if err == nil {
		return nil
	}
	var svnError *Error
	if errors.As(err, &svnError) {
		return &Error{Code: svnError.Code, Child: err}
	}
	return err
}

func (err *Error) Error() string {
	if err == nil {
		return "<nil>"
	}
	message := err.Message
	if message == "" {
		message = err.Code.DefaultMessage()
	}
	if message == "" && err.Child != nil {
		return err.Child.Error()
	}
	return message
}

func (err *Error) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Child
}

func (err *Error) Is(target error) bool {
	if err == nil || target == nil {
		return false
	}
	switch target := target.(type) {
	case Code:
		return err.Code == target
	case *Error:
		return target != nil && err.Code == target.Code
	default:
		return false
	}
}

type errorDefinition struct {
	name    string
	message string
}

func ErrorCode(err error) (Code, bool) {
	var svnError *Error
	if errors.As(err, &svnError) {
		return svnError.Code, true
	}
	var code Code
	if errors.As(err, &code) {
		return code, true
	}
	return 0, false
}
