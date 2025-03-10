package goserver

import "errors"

var (
	ErrServerRunning  error = errors.New("server is running")
	ErrPathFormat     error = errors.New("path must start with \"/\"")
	ErrActionNotFound error = errors.New("action not exist")
	ErrActionConflict error = errors.New("action register conflict")
)
