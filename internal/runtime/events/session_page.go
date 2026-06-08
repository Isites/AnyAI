package runtimeevents

// SessionEventPageRequest describes a bounded read from persisted session
// history. Before is currently the session entry ID at the start of the
// already-loaded window; an empty Before reads the tail page.
type SessionEventPageRequest struct {
	Limit  int
	Before string
}

// SessionEventPage is a bounded projection of session history.
type SessionEventPage struct {
	Events     []EventRecord
	HasMore    bool
	NextBefore string
	Total      int
}
