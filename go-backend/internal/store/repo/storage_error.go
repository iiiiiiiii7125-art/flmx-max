package repo

import "errors"

// StorageError marks an error returned verbatim from the storage layer
// (gorm / SQLite / PostgreSQL driver text, e.g. "no such table" or
// "database is locked"). Its Error() text is identical to the wrapped
// error, so existing callers that log or compare text are unaffected —
// but handlers must not return it to API callers unfiltered:
// handler.respondInternalIfRepoError and handler.batchFailureErr
// recognise this marker and substitute the generic "Internal error"
// text (RAW291 / R2-F4-01 family).
//
// The marker lives in the repo package (rather than the handler-side
// repoError) for exactly one case: validateIDsExistTx. Its gorm
// passthrough and its fixed missing-IDs text leave through the same
// return, and handler code at the call site cannot tell them apart —
// so the storage error is marked at its source, inside the repo.
// All other repo callers keep the handler-side markRepoError pattern
// (mark at the handler helper's h.repo.* return point).
type StorageError struct{ Err error }

func (e *StorageError) Error() string { return e.Err.Error() }
func (e *StorageError) Unwrap() error { return e.Err }

// MarkStorageError wraps err as a StorageError. It is idempotent on
// nil and on an already-marked error.
func MarkStorageError(err error) error {
	if err == nil {
		return nil
	}
	var se *StorageError
	if errors.As(err, &se) {
		return err
	}
	return &StorageError{Err: err}
}
