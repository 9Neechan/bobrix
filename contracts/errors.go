package contracts

import "errors"

var (
	ErrMethodNotFound        = errors.New("method not found")
	ErrHandlerNotFound       = errors.New("handler not found")
	ErrInputRequired         = errors.New("input is required")
	ErrNoHealthCheckProvided = errors.New("no healthcheck provided")
)

const (
	ErrCodeServiceNotFound = 1 // "service not found"
	ErrCodeMethodNotFound  = 2 // "method not found"
)