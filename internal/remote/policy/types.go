package policy

import "time"

type Decision struct {
	Allow   bool
	Message string
	TTL     time.Duration
}

type Context struct {
	Session   string
	Address   string
	Operation string
	Resources map[string]string
}
