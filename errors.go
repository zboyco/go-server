package goserver

import "errors"

var (
	ErrPathFormat     error = errors.New("path must start with \"/\"")
	ErrActionNotFound error = errors.New("action not exist")
	ErrActionConflict error = errors.New("action register conflict")
)
