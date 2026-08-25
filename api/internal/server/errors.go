package server

import "errors"

// errEmptyHost is returned when a connection URL carries no host component.
var errEmptyHost = errors.New("empty host")
