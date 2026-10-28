package operator

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

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
