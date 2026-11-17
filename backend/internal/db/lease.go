package db

import "errors"

// ErrInvalidLease is returned by a fenced write that is given a Lease with no
// item ID or no granted epoch. Claims always grant an epoch of 1 or more, so
// such a lease never belongs to a real claim.
var ErrInvalidLease = errors.New("invalid lease")

func (l Lease) validate() error {
	if l.ID == "" || l.Epoch < 1 {
		return ErrInvalidLease
	}
	return nil
}

// Lease identifies one granted claim on a work item. Claim functions bump the
// item's claim_epoch; every later write that must only succeed for the current
// holder takes a Lease, so a worker whose claim expired cannot overwrite the
// result of the worker that took over. A write without a Lease does not compile.
type Lease struct {
	ID    string
	Epoch int64
}
