package goserver

import "errors"

var (
	PathFormatError     error = errors.New("path must start with \"/\"")
	ActionNotFoundError error = errors.New("action not exist")
	ActionConflictError error = errors.New("action register conflict")
)
