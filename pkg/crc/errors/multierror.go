package errors

import (
	"fmt"
	"strings"
	"time"

	"github.com/code-ready/crc/pkg/crc/logging"
)

type MultiError struct {
	Errors []error
}

func (m MultiError) Error() string {
	if len(m.Errors) == 0 {
		return ""
	}
	if len(m.Errors) == 1 {
		return m.Errors[0].Error()
	}

	var aggregatedErrors []string

	count := 1
	current := m.Errors[0].Error()
	for i := 1; i < len(m.Errors); i++ {
		if m.Errors[i].Error() == current {
			count++
			continue
		}
		aggregatedErrors = append(aggregatedErrors, errorWithCount(current, count))
		count = 1
		current = m.Errors[i].Error()
	}
	aggregatedErrors = append(aggregatedErrors, errorWithCount(current, count))

	return strings.Join(aggregatedErrors, "\n")
}

func (m *MultiError) Collect(err error) {
	if err != nil {
		m.Errors = append(m.Errors, err)
	}
}

func errorWithCount(current string, count int) string {
	if count == 1 {
		return current
	}
	return fmt.Sprintf("%s (x%d)", current, count)
}

// RetriableError is an error that can be tried again
type RetriableError struct {
	Err error
}

func (r RetriableError) Error() string {
	return "Temporary error: " + r.Err.Error()
}

// RetryAfter retries a number of attempts, after a delay
func RetryAfter(attempts int, callback func() error, d time.Duration) error {
	m := MultiError{}
	for i := 0; i < attempts; i++ {
		if i > 0 {
			logging.Debugf("retry loop: attempt %d out of %d", i, attempts)
		}
		err := callback()
		if err == nil {
			return nil
		}
		m.Collect(err)
		if _, ok := err.(*RetriableError); !ok {
			logging.Debugf("non-retriable error: %v", err)
			return m
		}
		logging.Debugf("error: %v - sleeping %s", err, d)
		time.Sleep(d)
	}
	logging.Debugf("RetryAfter timeout after %d tries", attempts)
	return m
}
