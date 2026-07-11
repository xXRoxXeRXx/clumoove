package scheduler

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// parser is the standard cron expression parser (minute, hour, dom, month, dow)
var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// ValidateCronExpression checks if a cron expression is valid
func ValidateCronExpression(expr string) error {
	_, err := parser.Parse(expr)
	if err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}
	return nil
}

// NextRun calculates the next execution time from now
func NextRun(expr string) (time.Time, error) {
	schedule, err := parser.Parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	return schedule.Next(time.Now()), nil
}

// NextRunFrom calculates the next execution time from a given time
func NextRunFrom(expr string, from time.Time) (time.Time, error) {
	schedule, err := parser.Parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	return schedule.Next(from), nil
}
