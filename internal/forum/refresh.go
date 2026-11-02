package forum

import (
	"sync"
)

// RefreshOutcome is what asking for one channel produced.
type RefreshOutcome struct {
	Team     string
	Channel  string
	Messages int
	Replies  int
	Locked   bool
	Errors   []string
}

// Refresher runs at most one refresh per channel at a time: ten agents reading the
// same stale channel cost one `conversations.history` call, which is the whole
// reason the daemon exists.
type Refresher struct {
	// Run is the work, supplied by the caller because only it knows how to build a
	// client for a workspace.
	Run func(teamID, channelID string) RefreshOutcome

	mutex    sync.Mutex
	inFlight map[string]*refreshCall
}

type refreshCall struct {
	done    chan struct{}
	outcome RefreshOutcome
}

// Channel hands back the run already under way for this channel, or starts one.
func (r *Refresher) Channel(teamID, channelID string) RefreshOutcome {
	key := teamID + ":" + channelID

	r.mutex.Lock()

	if r.inFlight == nil {
		r.inFlight = map[string]*refreshCall{}
	}

	if running, started := r.inFlight[key]; started {
		r.mutex.Unlock()

		<-running.done

		return running.outcome
	}

	call := &refreshCall{done: make(chan struct{})}
	r.inFlight[key] = call
	r.mutex.Unlock()

	call.outcome = r.Run(teamID, channelID)

	r.mutex.Lock()
	// The entry has to be gone by the time the run settles, or the next reader would
	// join a finished call and never refresh.
	delete(r.inFlight, key)
	r.mutex.Unlock()

	close(call.done)

	return call.outcome
}
