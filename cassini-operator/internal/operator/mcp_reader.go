package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"cassini-operator/internal/operator/appapi"
	"cassini-operator/internal/operator/mcpserver"
)

// The MCP server's read service (D-813). mcpserver owns the protocol and the
// tool contracts and imports nothing from here; this adapter answers its
// Reader interface from the same read functions the published routes call, so
// a tool and the route behind the matching CLI verb cannot disagree about
// what a caller may read, or about how they are refused.

// mcpURLPath is where the server answers. appinfo/info.xml declares it
// `^mcp\/?$`, POST, USER.
const mcpURLPath = "/mcp"

// mcpReader implements mcpserver.Reader.
type mcpReader struct {
	exapp  ExAppConfig
	client *http.Client
	search searchDeps
	// annotations is nil wherever marks cannot be served; tags and annotations
	// then answer that the tag index is unavailable.
	annotations *annotationService
	logger      *log.Logger
}

var _ mcpserver.Reader = (*mcpReader)(nil)

// newMCPHandler returns the MCP endpoint, or nil wherever it could not answer
// as anyone: with no AppAPI identity (a standalone operator on a static token
// cannot say who is calling, and refusing beats guessing — D-821 owns that
// caller) or with no Nextcloud Files archive to read. A route that always
// fails is worse than a route that is not there.
func newMCPHandler(exapp ExAppConfig, search searchDeps, annotations *annotationService, logger *log.Logger) http.Handler {
	if !exapp.appAPIActive() || exapp.PublishSink != publishSinkNextcloudFiles {
		return nil
	}
	reader := &mcpReader{
		exapp: exapp,
		// As the published routes read: a hung Nextcloud is bounded on
		// headers, the rest by the request's own context.
		client:      &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: ncFilesProxyHeadersTTL}},
		search:      search,
		annotations: annotations,
		logger:      logger,
	}
	return mcpserver.NewHandler(reader, appapi.UserID, logger)
}

func (m *mcpReader) Rooms(ctx context.Context, caller string) (mcpserver.RoomsResult, error) {
	response, err := m.exapp.meetingsRooms(ctx, m.client, caller, m.search, m.logger)
	if err != nil {
		return mcpserver.RoomsResult{}, mcpRefusal(err)
	}
	result := mcpserver.RoomsResult{
		Version: response.Version, Source: response.Source,
		Skipped: response.Skipped, Unattributed: response.Unattributed,
		Rooms: make([]mcpserver.Room, 0, len(response.Rooms)),
	}
	for _, room := range response.Rooms {
		result.Rooms = append(result.Rooms, mcpserver.Room{
			Room: room.Selector, RoomID: room.RoomID,
			RoomName: room.RoomName, RoomDisplayName: room.RoomDisplayName,
			Meetings: room.Meetings, Latest: room.Latest, Earliest: room.Earliest,
		})
	}
	return result, nil
}

func (m *mcpReader) List(ctx context.Context, caller string, in mcpserver.ListInput) (mcpserver.ListResult, error) {
	filter, err := newMeetingsListFilter(in.From, in.To, in.Room, in.Tag)
	if err != nil {
		// Every filter error is the caller's to fix, and says how.
		return mcpserver.ListResult{}, mcpRefusal(readFail(http.StatusBadRequest, err.Error()))
	}
	response, err := m.exapp.meetingsList(ctx, m.client, caller, filter, m.search, m.logger)
	if err != nil {
		return mcpserver.ListResult{}, mcpRefusal(err)
	}
	// The CLI's document: newest first, entries with no id skipped and
	// counted, each entry otherwise exactly as published.
	items, skipped := orderListedMeetings(response.Meetings)
	result := mcpserver.ListResult{
		Version:  response.Version,
		Source:   ncFilesSourceValue,
		Skipped:  skipped,
		Meetings: make([]map[string]any, 0, len(items)),
	}
	if response.Filter != nil {
		result.Filter = &mcpserver.ListFilter{
			From: response.Filter.From, To: response.Filter.To,
			Room: response.Filter.Room, Tag: response.Filter.Tag,
		}
	}
	if response.Excluded != nil {
		result.Excluded = response.Excluded.Total
		result.ExcludedUndated = response.Excluded.Undated
	}
	for _, item := range items {
		var entry map[string]any
		if err := json.Unmarshal(item.raw, &entry); err != nil {
			// orderListedMeetings already decoded it, so this cannot happen
			// for an object; a non-object entry has no id and was skipped.
			result.Skipped++
			continue
		}
		result.Meetings = append(result.Meetings, entry)
	}
	return result, nil
}

func (m *mcpReader) Tags(ctx context.Context, caller string) (mcpserver.TagsResult, error) {
	if m.annotations == nil {
		return mcpserver.TagsResult{}, mcpRefusal(readFail(http.StatusServiceUnavailable, tagIndexUnavailableMessage))
	}
	vocabulary, err := m.annotations.tagVocabulary(ctx, caller)
	if err != nil {
		return mcpserver.TagsResult{}, mcpRefusal(err)
	}
	result := mcpserver.TagsResult{
		Tags:     make([]mcpserver.Tag, 0, len(vocabulary.Tags)),
		Meetings: make([]mcpserver.MeetingTags, 0, len(vocabulary.Meetings)),
		Coverage: mcpserver.TagCoverage{Visible: vocabulary.Coverage.Visible, Indexed: vocabulary.Coverage.Indexed},
	}
	for _, tag := range vocabulary.Tags {
		result.Tags = append(result.Tags, mcpserver.Tag{
			TagID: tag.TagID, Namespace: tag.Namespace, Label: tag.Label,
			Meetings: tag.Meetings, Marks: tag.Marks, Color: tag.Color, Icon: tag.Icon,
		})
	}
	for _, meeting := range vocabulary.Meetings {
		tags := make([]mcpserver.MeetingTag, 0, len(meeting.Tags))
		for _, tag := range meeting.Tags {
			tags = append(tags, mcpserver.MeetingTag{
				TagID: tag.TagID, Whole: tag.Whole, Stretches: tag.Stretches, Color: tag.Color, Icon: tag.Icon,
			})
		}
		result.Meetings = append(result.Meetings, mcpserver.MeetingTags{MeetingID: meeting.MeetingID, Tags: tags})
	}
	return result, nil
}

func (m *mcpReader) Annotations(ctx context.Context, caller string, in mcpserver.AnnotationsInput) (mcpserver.AnnotationsResult, error) {
	if !isPlainMeetingID(in.ID) {
		return mcpserver.AnnotationsResult{}, mcpRefusal(readFail(http.StatusBadRequest, "that is not a meeting id; list reports them"))
	}
	if m.annotations == nil {
		return mcpserver.AnnotationsResult{}, mcpRefusal(readFail(http.StatusServiceUnavailable, tagIndexUnavailableMessage))
	}
	response, err := m.annotations.meetingAnnotations(ctx, caller, in.ID)
	if err != nil {
		var failure *annotateFailure
		if errors.As(err, &failure) {
			m.annotations.logf("annotations: mcp read meeting=%s: %v", in.ID, failure.cause)
		}
		return mcpserver.AnnotationsResult{}, mcpRefusal(err)
	}
	result := mcpserver.AnnotationsResult{
		StateToken: response.StateToken,
		MeetingID:  response.MeetingID,
		Revision:   response.Revision,
		Resolved:   response.Resolved,
	}
	if response.Sync != nil {
		result.Sync = &mcpserver.SyncStatus{
			State: response.Sync.State, Desired: response.Sync.Desired,
			Confirmed: response.Sync.Confirmed, Error: response.Sync.Error,
		}
	}
	if len(response.Annotations) > 0 {
		var document any
		if err := json.Unmarshal(response.Annotations, &document); err != nil {
			m.logf("mcp: annotations meeting=%s: decode document: %v", in.ID, err)
			return mcpserver.AnnotationsResult{}, errors.New("the recording's annotations could not be read")
		}
		result.Annotations = document
	}
	return result, nil
}

func (m *mcpReader) Search(ctx context.Context, caller string, in mcpserver.SearchInput) (mcpserver.SearchResult, error) {
	response, err := m.exapp.searchMeetings(ctx, m.client, caller, searchParams{
		Query: in.Query, Speaker: in.Speaker, Room: in.Room, Tag: in.Tag, Limit: in.Limit,
	}, m.search, m.logger)
	if err != nil {
		return mcpserver.SearchResult{}, mcpRefusal(err)
	}
	// References only. The route's hits carry a bounded snippet for the
	// viewer; this result has nowhere to put one, so no transcript text read
	// from the index can reach an agent — reading what was said goes through
	// context, which reads the recording as the caller and so crosses
	// Nextcloud's own permission check. The CLI drops it the same way.
	result := mcpserver.SearchResult{
		Hits:     make([]mcpserver.SearchHit, 0, len(response.Hits)),
		Widened:  response.Widened,
		Searched: response.Searched,
		Coverage: mcpserver.SearchCoverage{Visible: response.Coverage.Visible, Searched: response.Coverage.Searched},
	}
	for _, hit := range response.Hits {
		var marks []mcpserver.SearchMark
		if hit.Marks != nil {
			for _, mark := range *hit.Marks {
				marks = append(marks, mcpserver.SearchMark{TagID: mark.TagID, Label: mark.Label})
			}
		}
		result.Hits = append(result.Hits, mcpserver.SearchHit{
			MeetingID: hit.MeetingID, Title: hit.Title, DateLabel: hit.DateLabel,
			RoomID: hit.RoomID, RoomName: hit.RoomName, RoomDisplayName: hit.RoomDisplayName,
			SegmentID: hit.SegmentID, StartMS: hit.StartMS, EndMS: hit.EndMS,
			SpeakerID: hit.SpeakerID, Matched: hit.Matched, Marks: marks,
		})
	}
	return result, nil
}

func (m *mcpReader) Context(ctx context.Context, caller string, in mcpserver.ContextInput) (mcpserver.ContextResult, error) {
	ids, err := validateContextIDs(in.IDs)
	if err != nil {
		return mcpserver.ContextResult{}, err
	}
	request := meetingsContextRequest{ids: ids, asJSON: true}
	if in.FromMS != nil || in.ToMS != nil {
		var from int64
		if in.FromMS != nil {
			from = *in.FromMS
		}
		if request.window, err = newContextWindow(from, in.ToMS); err != nil {
			return mcpserver.ContextResult{}, err
		}
	}
	if strings.TrimSpace(m.exapp.CassiniBin) == "" {
		return mcpserver.ContextResult{}, errors.New("reading meetings is not available on this deployment; this is not an empty result")
	}
	document, err := m.exapp.meetingsContextDocument(ctx, m.client, caller, request, m.logger)
	if err != nil {
		var read *readFailure
		if errors.As(err, &read) && read.status == http.StatusNotFound {
			// A bundle is all of its meetings or none, so say which rule
			// failed it rather than which meeting.
			return mcpserver.ContextResult{}, errors.New("one of those ids is not a meeting you can read; list reports the ids you can")
		}
		return mcpserver.ContextResult{}, mcpRefusal(err)
	}
	defer document.Close()
	raw, err := io.ReadAll(document)
	if err != nil {
		m.logf("mcp: context caller=%s: read document: %v", caller, err)
		return mcpserver.ContextResult{}, errors.New("the meetings could not be read; this is not an empty result")
	}
	result, err := decodeContextDocument(raw)
	if err != nil {
		m.logf("mcp: context caller=%s: decode document: %v", caller, err)
		return mcpserver.ContextResult{}, errors.New("the meetings could not be read; this is not an empty result")
	}
	return result, nil
}

// decodeContextDocument reads either form of cassini.meetings.context.v1 into
// the container form the tool always answers in: one meeting is printed as the
// bare document, several under "meetings".
func decodeContextDocument(raw []byte) (mcpserver.ContextResult, error) {
	var probe struct {
		Version  string          `json:"version"`
		Meetings json.RawMessage `json:"meetings"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return mcpserver.ContextResult{}, err
	}
	if probe.Version != contextDocumentVersion {
		return mcpserver.ContextResult{}, fmt.Errorf("the document declares version %q", probe.Version)
	}
	result := mcpserver.ContextResult{Version: contextDocumentVersion}
	if len(probe.Meetings) > 0 {
		if err := json.Unmarshal(raw, &result); err != nil {
			return mcpserver.ContextResult{}, err
		}
	} else {
		var one mcpserver.MeetingContext
		if err := json.Unmarshal(raw, &one); err != nil {
			return mcpserver.ContextResult{}, err
		}
		result.Meetings = []mcpserver.MeetingContext{one}
	}
	if len(result.Meetings) == 0 {
		return mcpserver.ContextResult{}, errors.New("the document carries no meetings")
	}
	for i := range result.Meetings {
		// Never null on the wire: a meeting with no speech in the window is an
		// empty transcript, not a missing one.
		if result.Meetings[i].Segments == nil {
			result.Meetings[i].Segments = []mcpserver.ContextSegment{}
		}
	}
	return result, nil
}

// contextDocumentVersion is the cassini.meetings.context.v1 version string.
// The contract lives in the CLI's module, which this one does not import; the
// document is the CLI's output, and this is the one field read to trust it.
const contextDocumentVersion = "cassini.meetings.context.v1"

func (m *mcpReader) logf(format string, args ...any) {
	if m.logger != nil {
		m.logger.Printf(format, args...)
	}
}

// mcpRefusal turns a failed read into what the agent is told: the failure's
// public text, with a denial worded as the absence it is served as on every
// other surface. Anything that is not one of the read paths' own failures is
// an outage the agent is told nothing more about.
func mcpRefusal(err error) error {
	var read *readFailure
	if errors.As(err, &read) {
		if read.status == http.StatusNotFound {
			return errMCPNotFound
		}
		return errors.New(read.public)
	}
	var failure *annotateFailure
	if errors.As(err, &failure) {
		if failure.status == http.StatusNotFound {
			return errMCPNotFound
		}
		if failure.public != "" {
			return errors.New(failure.public)
		}
	}
	return errors.New("the read failed; this is not an empty result")
}

// errMCPNotFound is a meeting that does not exist or that the caller may not
// read — deliberately the same answer.
var errMCPNotFound = errors.New("no meeting with that id that you can read; list reports the ids you can")
