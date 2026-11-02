package archive

import (
	"os"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/envelope"
	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

const (
	pageSize = 200
	// maxPages is a loop guard, not a budget: the deadline bounds a run. Paging
	// stops early only when Slack repeats a cursor, and that is reported as an
	// incomplete window, because a walk that stopped halfway is not history.
	maxPages          = 1000
	pendingThreadCap  = 500
	reconcileInterval = 24 * time.Hour
	// DefaultBackfillDays is how far back a workspace is read the first time it is
	// seen. Deeper costs pages Slack ends early when a plan keeps less history;
	// shallower leaves a gap only `--full --since` closes.
	DefaultBackfillDays = 90

	defaultReconcileDays = 7
	defaultMaxThreads    = 10
	defaultDeadline      = 10 * time.Minute
)

// SyncClient is the slice of the API client a sync needs.
type SyncClient interface {
	ListMyChannels() ([]slack.Channel, error)
	History(slack.HistoryParams) (slack.HistoryPage, error)
	RepliesPage(channel, threadTS string, limit int, cursor string) (slack.HistoryPage, error)
	DownloadFile(slack.File) ([]byte, error)
}

// SyncOptions is what one run is asked for.
type SyncOptions struct {
	Full          bool
	BackfillDays  int
	ReconcileDays int
	Deadline      time.Duration
	MaxThreads    int
	// RefreshDays always re-reads this window, even when the cursor is newer.
	// Replies never show up in channel history, so a parent older than the cursor is
	// only seen again when a read reaches back to it; the forum uses this to keep
	// live tasks current.
	RefreshDays int
	// Channels restricts the run to these ids; the forum polls one or two at a time.
	Channels   []string
	Now        time.Time
	OnProgress func(string)
}

// SyncReport is what a run did, in the numbers a reader asks for.
type SyncReport struct {
	TeamID          string
	ChannelsSeen    int
	ChannelsSynced  int
	MessagesAdded   int
	RepliesAdded    int
	ThreadsFetched  int
	DeletionsMarked int
	Errors          []ChannelError
	Truncated       bool
	Skipped         string
	Duration        time.Duration
}

type resolvedOptions struct {
	full          bool
	backfillDays  int
	reconcileDays int
	maxThreads    int
	refreshDays   int
	now           time.Time
}

func resolveOptions(options SyncOptions) resolvedOptions {
	resolved := resolvedOptions{
		full:          options.Full,
		backfillDays:  options.BackfillDays,
		reconcileDays: options.ReconcileDays,
		maxThreads:    options.MaxThreads,
		refreshDays:   options.RefreshDays,
		now:           options.Now,
	}

	if resolved.backfillDays == 0 {
		resolved.backfillDays = DefaultBackfillDays
	}

	if resolved.reconcileDays == 0 {
		resolved.reconcileDays = defaultReconcileDays
	}

	if resolved.maxThreads == 0 {
		resolved.maxThreads = defaultMaxThreads
	}

	if resolved.now.IsZero() {
		resolved.now = time.Now()
	}

	return resolved
}

func slackTS(when time.Time) string {
	return strconv.FormatFloat(float64(when.UnixMilli())/1000, 'f', 6, 64)
}

func daysAgo(now time.Time, days int) string {
	return slackTS(now.Add(-time.Duration(days) * 24 * time.Hour))
}

// olderTS is the older of two Slack ts values, so one history call answers both
// questions.
func olderTS(left, right string) string {
	if right == "" {
		return left
	}

	if numeric(right) < numeric(left) {
		return right
	}

	return left
}

// SyncAccount runs the two passes plus the thread backlog for one workspace.
//
// Incremental (oldest = the newest cached) is cheap but blind to edits and
// deletions, so a second pass re-reads a recent window once a day and marks
// whatever disappeared as deleted instead of erasing it.
func SyncAccount(teamID string, client SyncClient, store *Store, index *Index, options SyncOptions) (SyncReport, error) {
	resolved := resolveOptions(options)

	deadline := time.Now().Add(options.Deadline)
	if options.Deadline == 0 {
		deadline = time.Now().Add(defaultDeadline)
	}

	progress := options.OnProgress
	if progress == nil {
		progress = func(string) {}
	}

	startedAt := time.Now()
	report := SyncReport{TeamID: teamID}

	state, err := store.LoadState()
	if err != nil {
		return report, err
	}

	teamState := state.Teams[teamID]
	if teamState == nil {
		teamState = &TeamState{Channels: map[string]ChannelState{}}
		state.Teams[teamID] = teamState
	}

	if teamState.Channels == nil {
		teamState.Channels = map[string]ChannelState{}
	}

	lock := NewSyncLock(store.Root)

	acquired, err := lock.Acquire()
	if err != nil {
		return report, err
	}

	if !acquired {
		report.Skipped = "locked"
		report.Duration = time.Since(startedAt)

		return report, nil
	}

	defer func() { _ = lock.Release() }()

	channels, err := client.ListMyChannels()
	if err != nil {
		return report, err
	}

	scoped := scopeChannels(channels, options.Channels)
	report.ChannelsSeen = len(scoped)

	// Least recently synced first, so a run that runs out of time covers the
	// channels that need it most.
	sort.SliceStable(scoped, func(left, right int) bool {
		return teamState.Channels[scoped[left].ID].LastSync < teamState.Channels[scoped[right].ID].LastSync
	})

	run := syncRun{
		client: client, store: store, index: index, teamID: teamID,
		state: &state, team: teamState, options: resolved,
		deadline: deadline, progress: progress,
	}

	if err := run.channels(scoped, &report); err != nil {
		return report, err
	}

	teamState.LastRun = resolved.now.UTC().Format("2006-01-02T15:04:05.000Z")

	if err := store.SaveState(state); err != nil {
		return report, err
	}

	report.Duration = time.Since(startedAt)

	return report, nil
}

// syncRun is one workspace's pass over its channels.
type syncRun struct {
	client   SyncClient
	store    *Store
	index    *Index
	teamID   string
	state    *SyncState
	team     *TeamState
	options  resolvedOptions
	deadline time.Time
	progress func(string)
}

func (r *syncRun) channels(scoped []slack.Channel, report *SyncReport) error {
	for _, channel := range scoped {
		if time.Now().After(r.deadline) {
			report.Truncated = true

			break
		}

		if err := r.channel(channel, report); err != nil {
			return err
		}
	}

	return nil
}

func (r *syncRun) channel(channel slack.Channel, report *SyncReport) error {
	outcome, err := runChannel(r.client, r.store, r.index, r.teamID, channel, r.team.Channels[channel.ID], r.options, r.deadline)
	if err != nil {
		entry := r.team.Channels[channel.ID]
		entry.LastError = err.Error()
		r.team.Channels[channel.ID] = entry
		report.Errors = append(report.Errors, ChannelError{Channel: channelLabelOf(channel), Error: err.Error()})

		return nil //nolint:nilerr // one channel's failure is reported, not fatal for the run
	}

	applyOutcome(report, r.team, channel, outcome)

	if outcome.added+outcome.replies > 0 {
		r.progress(channelLabelOf(channel) + ": +" + strconv.Itoa(outcome.added) + " messages, +" +
			strconv.Itoa(outcome.replies) + " replies")
	}

	return r.store.SaveState(*r.state)
}

func scopeChannels(channels []slack.Channel, wanted []string) []slack.Channel {
	if len(wanted) == 0 {
		return channels
	}

	keep := make(map[string]bool, len(wanted))
	for _, id := range wanted {
		keep[id] = true
	}

	scoped := make([]slack.Channel, 0, len(channels))

	for _, channel := range channels {
		if keep[channel.ID] {
			scoped = append(scoped, channel)
		}
	}

	return scoped
}

func channelLabelOf(channel slack.Channel) string {
	if name := ChannelName(channel); name != "" {
		return name
	}

	return channel.ID
}

func applyOutcome(report *SyncReport, teamState *TeamState, channel slack.Channel, outcome channelOutcome) {
	teamState.Channels[channel.ID] = outcome.entry
	report.ChannelsSynced++
	report.MessagesAdded += outcome.added
	report.RepliesAdded += outcome.replies
	report.ThreadsFetched += outcome.threads
	report.DeletionsMarked += outcome.deletions

	if !outcome.complete {
		report.Truncated = true
	}
}

type channelOutcome struct {
	entry     ChannelState
	added     int
	replies   int
	threads   int
	deletions int
	// complete is false when a read stopped before Slack ran out of pages, so
	// nothing downstream guesses from a partial window.
	complete bool
}

// runChannel keeps the error inside the outcome so the loop above stays a loop.
func runChannel(
	client SyncClient,
	store *Store,
	index *Index,
	teamID string,
	channel slack.Channel,
	entry ChannelState,
	options resolvedOptions,
	deadline time.Time,
) (channelOutcome, error) {
	sync := &channelSync{
		client:   client,
		store:    store,
		index:    index,
		teamID:   teamID,
		channel:  channel,
		name:     ChannelName(channel),
		entry:    entry,
		options:  options,
		deadline: deadline,
		pending:  newOrderedSet(entry.PendingThread),
		fetched:  map[string]slack.Message{},
	}

	return sync.run()
}

// channelSync is one channel's run: the cursor, the messages it saw, the thread
// backlog it drained and the state it leaves behind.
type channelSync struct {
	client   SyncClient
	store    *Store
	index    *Index
	teamID   string
	channel  slack.Channel
	name     string
	entry    ChannelState
	options  resolvedOptions
	deadline time.Time

	cached       []ArchivedMessage
	cachedByTS   map[string]ArchivedMessage
	pending      *orderedSet
	records      []ArchivedMessage
	fetched      map[string]slack.Message
	fetchedOrder []string
	envelopes    map[string]string
}

func (s *channelSync) run() (channelOutcome, error) {
	var err error

	s.cached, err = s.store.Read(s.teamID, s.channel.ID)
	if err != nil {
		return channelOutcome{}, err
	}

	s.cachedByTS = make(map[string]ArchivedMessage, len(s.cached))
	for _, record := range s.cached {
		s.cachedByTS[record.TS] = record
	}

	incrementalComplete, reconcileDue, reconcileComplete, err := s.walkHistory()
	if err != nil {
		return channelOutcome{}, err
	}

	parents := s.threadParents()

	replies, err := s.collectThreads(parents)
	if err != nil {
		return channelOutcome{}, err
	}

	stale := s.staleEnvelopes(replies)
	s.envelopes = s.collectEnvelopes(replies, stale)

	before := len(s.records)

	s.appendChanged()

	added := 0

	for _, record := range s.records[before:] {
		if record.DeletedAt == "" && record.ThreadTS == "" {
			added++
		}
	}

	deletions := 0

	if reconcileDue && reconcileComplete {
		deletions = s.markDeletions()
	}

	repliesAdded := s.appendReplies(replies)

	if err := s.persist(); err != nil {
		return channelOutcome{}, err
	}

	return channelOutcome{
		entry:     s.nextEntry(incrementalComplete, reconcileDue, reconcileComplete),
		added:     added,
		replies:   repliesAdded,
		threads:   len(parents),
		deletions: deletions,
		complete:  incrementalComplete && reconcileComplete,
	}, nil
}

// walkHistory runs the two passes: the incremental window and the reconcile one.
func (s *channelSync) walkHistory() (complete, reconcileDue, reconcileComplete bool, err error) {
	incrementalFrom := ""
	if !s.options.full {
		incrementalFrom = s.entry.NewestTS
	}

	refreshFrom := ""
	if !s.options.full && s.options.refreshDays > 0 {
		refreshFrom = daysAgo(s.options.now, s.options.refreshDays)
	}

	// One walk covers both questions: the window that catches edits and new replies
	// is the older of the cursor and the refresh start, so a poll stays one call.
	cursor := incrementalFrom
	if cursor == "" {
		cursor = daysAgo(s.options.now, s.options.backfillDays)
	}

	complete, err = s.collect(olderTS(cursor, refreshFrom))
	if err != nil {
		return false, false, false, err
	}

	reconcileDue = s.options.reconcileDays > 0 &&
		(s.entry.LastReconcile == "" ||
			s.options.now.Sub(parseStamp(s.entry.LastReconcile)) > reconcileInterval)

	// The deletion pass calls whatever the reconcile window did not show gone. A
	// window that was cut short proves nothing, so it is skipped instead of trusted.
	reconcileComplete = true

	if reconcileDue {
		reconcileComplete, err = s.collect(daysAgo(s.options.now, s.options.reconcileDays))
		if err != nil {
			return false, false, false, err
		}
	}

	return complete, reconcileDue, reconcileComplete, nil
}

// collect walks a history window, answering whether it reached the end.
func (s *channelSync) collect(oldest string) (bool, error) {
	cursor := ""

	for range maxPages {
		if time.Now().After(s.deadline) {
			return false, nil
		}

		page, err := s.client.History(slack.HistoryParams{
			Channel: s.channel.ID,
			Limit:   pageSize,
			Oldest:  oldest,
			Cursor:  cursor,
		})
		if err != nil {
			return false, err
		}

		for _, message := range page.Messages {
			if _, seen := s.fetched[message.TS]; !seen {
				s.fetchedOrder = append(s.fetchedOrder, message.TS)
			}

			s.fetched[message.TS] = message
		}

		if !page.HasMore {
			return true, nil
		}

		// Nothing to continue from, or the same page twice: stopping here is the
		// only honest answer, and it must not pass for the end of the channel.
		if page.NextCursor == "" || page.NextCursor == cursor {
			return false, nil
		}

		cursor = page.NextCursor
	}

	return false, nil
}

// threadParents queues every parent whose reply count differs from what is stored,
// and answers the ones this run will drain.
func (s *channelSync) threadParents() []string {
	for _, ts := range s.fetchedOrder {
		message := s.fetched[ts]

		if message.ReplyCount == nil || *message.ReplyCount <= 0 {
			continue
		}

		stored := -1
		if record, ok := s.cachedByTS[ts]; ok && record.ReplyCount != nil {
			stored = *record.ReplyCount
		}

		if stored != *message.ReplyCount {
			s.pending.add(ts)
		}
	}

	parents := s.pending.values()
	if len(parents) > s.options.maxThreads {
		parents = parents[:s.options.maxThreads]
	}

	return parents
}

func (s *channelSync) collectThreads(parents []string) ([]slack.Message, error) {
	collected := make([]slack.Message, 0, len(parents)*4)

	for _, parentTS := range parents {
		if time.Now().After(s.deadline) {
			break
		}

		replies, complete, err := collectThread(s.client, s.channel.ID, parentTS, s.deadline)
		if err != nil {
			return nil, err
		}

		// Slack serves the parent in the thread most of the time, but not always;
		// counting it as a reply would leave every thread one short and queued forever.
		answered := 0

		for _, reply := range replies {
			if reply.TS == parentTS {
				continue
			}

			collected = append(collected, reply)
			answered++
		}

		// Slack can serve a thread a moment before its newest reply is visible.
		// Keeping the parent queued retries it on the next run instead of trusting a
		// half thread forever, which would leave a task stuck one state behind.
		s.pending.remove(parentTS)

		if !complete || answered < s.expectedReplies(parentTS) {
			s.pending.add(parentTS)
		}
	}

	return collected, nil
}

func (s *channelSync) expectedReplies(parentTS string) int {
	if message, ok := s.fetched[parentTS]; ok && message.ReplyCount != nil {
		return *message.ReplyCount
	}

	if record, ok := s.cachedByTS[parentTS]; ok && record.ReplyCount != nil {
		return *record.ReplyCount
	}

	return 0
}

func collectThread(client SyncClient, channelID, parentTS string, deadline time.Time) ([]slack.Message, bool, error) {
	collected := make([]slack.Message, 0, 8)
	seen := map[string]bool{}
	cursor := ""

	for range maxPages {
		if time.Now().After(deadline) {
			return collected, false, nil
		}

		page, err := client.RepliesPage(channelID, parentTS, pageSize, cursor)
		if err != nil {
			return nil, false, err
		}

		for _, message := range page.Messages {
			if !seen[message.TS] {
				seen[message.TS] = true
				collected = append(collected, message)
			}
		}

		if !page.HasMore {
			return collected, true, nil
		}

		if page.NextCursor == "" || page.NextCursor == cursor {
			return collected, false, nil
		}

		cursor = page.NextCursor
	}

	return collected, false, nil
}

// staleEnvelopes is the records written before this build read envelopes from
// files: they still name the file they carried, which is enough to go back for it.
func (s *channelSync) staleEnvelopes(replies []slack.Message) []ArchivedMessage {
	inReplies := make(map[string]bool, len(replies))
	for _, reply := range replies {
		inReplies[reply.TS] = true
	}

	stale := make([]ArchivedMessage, 0, 4)

	for _, record := range s.cached {
		if record.Forum != "" || record.DeletedAt != "" {
			continue
		}

		if _, carried := archivedEnvelopeFile(record.Files); !carried {
			continue
		}

		if _, seen := s.fetched[record.TS]; seen || inReplies[record.TS] {
			continue
		}

		stale = append(stale, record)
	}

	return stale
}

// collectEnvelopes fetches the envelope each carrier names, keeping the bytes next
// to the archive and copying the text into the record, which is what keeps
// `inbox --offline` honest without a download on every read.
func (s *channelSync) collectEnvelopes(replies []slack.Message, stale []ArchivedMessage) map[string]string {
	found := map[string]string{}
	files := NewFileStore(s.store.Root)

	for _, carrier := range s.carriers(replies, stale) {
		if time.Now().After(s.deadline) {
			break
		}

		if carrier.file.ID == "" {
			continue
		}

		if cached, err := files.Find(s.teamID, carrier.file.ID); err == nil && cached != nil {
			if raw, err := readFileText(cached.Path); err == nil {
				found[carrier.ts] = raw

				continue
			}
		}

		bytes, err := s.client.DownloadFile(carrier.file)
		if err != nil {
			// An envelope that will not download is a record the next run can still
			// complete; failing the channel over one attachment is worse than retrying.
			continue
		}

		if _, err := files.Save(s.teamID, carrier.file, bytes); err != nil {
			continue
		}

		found[carrier.ts] = string(bytes)
	}

	return found
}

type envelopeCarrier struct {
	ts   string
	file slack.File
}

func (s *channelSync) carriers(replies []slack.Message, stale []ArchivedMessage) []envelopeCarrier {
	carriers := make([]envelopeCarrier, 0, len(s.fetched)+len(replies))

	for _, ts := range s.fetchedOrder {
		if file, ok := envelopeFileOf(s.fetched[ts].Files); ok {
			carriers = append(carriers, envelopeCarrier{ts: ts, file: file})
		}
	}

	for _, reply := range replies {
		if file, ok := envelopeFileOf(reply.Files); ok {
			carriers = append(carriers, envelopeCarrier{ts: reply.TS, file: file})
		}
	}

	for _, record := range stale {
		if file, ok := archivedEnvelopeFile(record.Files); ok {
			carriers = append(carriers, envelopeCarrier{ts: record.TS, file: file})
		}
	}

	return carriers
}

// archivedEnvelopeFile is the same choice over the archive's shape, which keeps the
// file id a record already stored.
func archivedEnvelopeFile(files []ArchivedFile) (slack.File, bool) {
	carried := make([]envelope.CarriedFile, 0, len(files))

	for _, file := range files {
		carried = append(carried, envelope.CarriedFile{Name: file.Name, Size: file.Size})
	}

	chosen, ok := envelope.FileOf(carried)
	if !ok {
		return slack.File{}, false
	}

	for _, file := range files {
		if file.Name == chosen.Name {
			return slack.File{ID: file.ID, Name: file.Name, Size: file.Size}, true
		}
	}

	return slack.File{}, false
}

func envelopeFileOf(files []slack.File) (slack.File, bool) {
	carried := make([]envelope.CarriedFile, 0, len(files))

	for _, file := range files {
		carried = append(carried, envelope.CarriedFile{Name: file.Name, Size: file.Size})
	}

	chosen, ok := envelope.FileOf(carried)
	if !ok {
		return slack.File{}, false
	}

	for _, file := range files {
		if file.Name == chosen.Name {
			return file, true
		}
	}

	return slack.File{}, false
}

func (s *channelSync) appendChanged() {
	for _, ts := range s.fetchedOrder {
		record := ToRecord(s.channel, s.fetched[ts], s.options.now, "")

		if text, ok := s.envelopes[ts]; ok {
			record.Forum = text
		}

		cachedRecord, known := s.cachedByTS[ts]

		if !Changed(pick(cachedRecord, known), record) {
			continue
		}

		s.records = append(s.records, record)
	}
}

func (s *channelSync) markDeletions() int {
	windowStart := numeric(daysAgo(s.options.now, s.options.reconcileDays))
	stamp := s.options.now.UTC().Format("2006-01-02T15:04:05.000Z")

	deletions := 0

	for _, record := range s.cached {
		if record.DeletedAt != "" || record.ThreadTS != "" {
			continue
		}

		if numeric(record.TS) < windowStart {
			continue
		}

		if _, seen := s.fetched[record.TS]; seen {
			continue
		}

		s.records = append(s.records, ArchivedMessage{
			TS:          record.TS,
			Chan:        s.channel.ID,
			ChannelName: s.name,
			DeletedAt:   stamp,
			SyncedAt:    stamp,
		})

		deletions++
	}

	return deletions
}

func (s *channelSync) appendReplies(replies []slack.Message) int {
	added := 0

	for _, message := range replies {
		record := ToRecord(s.channel, message, s.options.now, "")

		if text, ok := s.envelopes[message.TS]; ok {
			record.Forum = text
		}

		cachedRecord, known := s.cachedByTS[message.TS]

		if !Changed(pick(cachedRecord, known), record) {
			continue
		}

		s.records = append(s.records, record)
		added++
	}

	return added
}

func (s *channelSync) persist() error {
	if len(s.records) > 0 {
		if err := s.store.Append(s.teamID, s.channel.ID, s.records); err != nil {
			return err
		}
	}

	// Everything the run saw goes to the index, not only what changed: a read tool
	// can have appended a record whose text already matched, and comparing against
	// the JSONL alone would leave that record unsearchable forever.
	return s.index.Upsert(s.teamID, s.indexable())
}

func (s *channelSync) indexable() []ArchivedMessage {
	byTS := make(map[string]ArchivedMessage, len(s.records)+len(s.fetched))

	for _, record := range s.records {
		byTS[record.TS] = record
	}

	for _, ts := range s.fetchedOrder {
		if _, ok := byTS[ts]; !ok {
			byTS[ts] = ToRecord(s.channel, s.fetched[ts], s.options.now, "")
		}
	}

	out := make([]ArchivedMessage, 0, len(byTS))

	for _, ts := range sortedKeys(byTS) {
		out = append(out, byTS[ts])
	}

	return out
}

func (s *channelSync) nextEntry(incrementalComplete, reconcileDue, reconcileComplete bool) ChannelState {
	next := s.entry

	timestamps := make([]string, 0, len(s.cached)+len(s.records))

	for _, record := range s.cached {
		timestamps = append(timestamps, record.TS)
	}

	for _, record := range s.records {
		if record.DeletedAt == "" {
			timestamps = append(timestamps, record.TS)
		}
	}

	slices.SortFunc(timestamps, func(left, right string) int {
		switch {
		case numeric(left) < numeric(right):
			return -1
		case numeric(left) > numeric(right):
			return 1
		default:
			return 0
		}
	})

	// A read that stopped early leaves the cursor where it was. Stepping over the
	// part it never fetched is how a gap becomes permanent.
	if incrementalComplete {
		next.LastSync = s.options.now.UTC().Format("2006-01-02T15:04:05.000Z")

		if len(timestamps) > 0 {
			next.NewestTS = timestamps[len(timestamps)-1]
		}
	}

	if len(timestamps) > 0 {
		next.OldestTS = timestamps[0]
	}

	if reconcileDue && reconcileComplete {
		next.LastReconcile = s.options.now.UTC().Format("2006-01-02T15:04:05.000Z")
	}

	next.PendingThread = nil

	if s.pending.len() > 0 {
		queued := s.pending.values()
		if len(queued) > pendingThreadCap {
			queued = queued[:pendingThreadCap]
		}

		next.PendingThread = queued
	}

	return next
}

// orderedSet is a set whose iteration order is the order things were added, which
// is what decides which threads a capped run drains first.
type orderedSet struct {
	seen  map[string]bool
	order []string
}

func newOrderedSet(values []string) *orderedSet {
	set := &orderedSet{seen: make(map[string]bool, len(values)), order: make([]string, 0, len(values))}

	for _, value := range values {
		set.add(value)
	}

	return set
}

func (s *orderedSet) add(value string) {
	if s.seen[value] {
		return
	}

	s.seen[value] = true
	s.order = append(s.order, value)
}

func (s *orderedSet) remove(value string) {
	if !s.seen[value] {
		return
	}

	delete(s.seen, value)

	for index, existing := range s.order {
		if existing == value {
			s.order = append(s.order[:index], s.order[index+1:]...)

			break
		}
	}
}

// values answers a copy: callers iterate it while removing from the set, and a
// shared backing array would shift the elements under the loop.
func (s *orderedSet) values() []string {
	out := make([]string, len(s.order))
	copy(out, s.order)

	return out
}

func (s *orderedSet) len() int {
	return len(s.order)
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))

	for key := range values {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

func pick(record ArchivedMessage, known bool) *ArchivedMessage {
	if !known {
		return nil
	}

	return &record
}

func parseStamp(value string) time.Time {
	when, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}

	return when
}

func readFileText(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the path is the archive's own file store
	if err != nil {
		return "", err
	}

	return string(raw), nil
}
