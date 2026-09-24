package eflint

import (
	"errors"
)

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------
//
// These errors are used throughout the eflint package for consistent error
// handling. Use errors.Is() to check for specific error types.

var (
	// ErrInstanceNotFound is returned when the instance does not exist.
	ErrInstanceNotFound = errors.New("instance not found")

	// ErrInstanceNotRunning is returned when an operation requires a running instance,
	// but the instance exists and is not running (e.g., process has exited).
	ErrInstanceNotRunning = errors.New("instance is not running")

	// ErrInstanceAlreadyExists is returned when attempting to start an instance
	// that is already running.
	ErrInstanceAlreadyExists = errors.New("instance already exists")

	// ErrProcessStartFailed is returned when the eFLINT server process fails to start.
	// The wrapped error contains details about the failure.
	ErrProcessStartFailed = errors.New("failed to start eFLINT server process")

	// ErrConnectionFailed is returned when a TCP connection to an eFLINT instance fails.
	// This can occur due to network issues or if the server is not responding.
	ErrConnectionFailed = errors.New("failed to connect to eFLINT server instance")

	// ErrCommandFailed is returned when sending a command to an eFLINT instance fails.
	// This can occur due to write failures or protocol errors.
	ErrCommandFailed = errors.New("failed to send command to eFLINT server instance")

	// ErrStateExportFailed is returned when exporting the eFLINT state fails.
	ErrStateExportFailed = errors.New("failed to export eFLINT state")

	// ErrStateImportFailed is returned when importing an eFLINT state fails.
	ErrStateImportFailed = errors.New("failed to import eFLINT state")

	// ErrInvalidResponse is returned when the eFLINT server returns an invalid
	// or unexpected response format.
	ErrInvalidResponse = errors.New("invalid response from eFLINT server")
)
