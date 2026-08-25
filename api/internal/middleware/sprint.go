package middleware

import "fmt"

// sprint renders a recovered panic value for logging without formatting
// directives supplied by the value itself.
func sprint(v any) string {
	return fmt.Sprintf("%v", v)
}
