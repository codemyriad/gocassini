package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Import notifications (#523): the importer hears that an import finished or
// failed even after closing the page. They are Nextcloud notifications sent
// through AppAPI, one per import, and none while the person has Cassini open:
// its Import panel asks for the list every few seconds while work is running,
// so a recent ask means they are watching it happen.
//
// The text is English, like Cassini's other server-written text (the Talk
// "ready" message, share notes); #476 covers translating it.

// appAPINotificationPath is AppAPI's route for an ExApp's notification. The
// recipient is the user named in the AUTHORIZATION-APP-API header.
const appAPINotificationPath = "/ocs/v1.php/apps/app_api/api/v1/notification"

// importWatchingWindow is how recently the importer must have asked for their
// imports to count as watching. The panel asks every 2.5 s while any import
// is unfinished.
const importWatchingWindow = 20 * time.Second

// importNotifyTimeout bounds one notification request.
const importNotifyTimeout = 15 * time.Second

// importWatchers remembers when each person last asked for their imports.
type importWatchers struct {
	mu   sync.Mutex
	seen map[string]time.Time
	now  func() time.Time
}

func (w *importWatchers) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

func (w *importWatchers) saw(user string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen == nil {
		w.seen = map[string]time.Time{}
	}
	w.seen[user] = w.clock()
}

func (w *importWatchers) watching(user string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	last, ok := w.seen[user]
	return ok && w.clock().Sub(last) < importWatchingWindow
}

// errNotificationsAppOff is AppAPI saying the Notifications app is disabled:
// nothing can be delivered, and nothing is wrong with the import.
var errNotificationsAppOff = fmt.Errorf("the Notifications app is not enabled")

// sendUserNotification asks AppAPI to notify user. subject may hold
// {placeholders} filled from params (Nextcloud rich objects); link, when not
// empty, is where the notification leads.
func (c ExAppConfig) sendUserNotification(ctx context.Context, client *http.Client, user, subject string, params map[string]any, message, link string) error {
	subjectParams := map[string]any{
		"rich_subject":        subject,
		"rich_subject_params": params,
		"rich_message":        message,
		"rich_message_params": map[string]any{},
	}
	if link != "" {
		subjectParams["link"] = link
	}
	body, err := json.Marshal(map[string]any{"params": map[string]any{
		"object":         "app_api",
		"object_id":      fmt.Sprintf("cassini-import-%d", time.Now().UnixNano()),
		"subject_type":   "app_api_ex_app",
		"subject_params": subjectParams,
	}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.NextcloudURL, "/")+appAPINotificationPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.setAppAPIOCSHeadersForUser(req, user)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == http.StatusNotFound && bytes.Contains(raw, []byte("notifications app is not enabled")) {
		return errNotificationsAppOff
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("AppAPI notification: HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	return nil
}

// cassiniViewerURL is Cassini's page in Nextcloud, opened on meetingID when
// one is given. base is the public Nextcloud address.
func cassiniViewerURL(base, appID, meetingID string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(base), "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || appID == "" || strings.ContainsAny(appID, "/?#") {
		return "", fmt.Errorf("no public Nextcloud address to link to")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/index.php/apps/app_api/embedded/" + appID + "/viewer"
	u.RawPath = ""
	if meetingID != "" {
		u.Fragment = "meeting=" + url.QueryEscape(meetingID)
	}
	return u.String(), nil
}

// importFailureAdvice is the notification's second line for a failed import:
// what happened, when the server knows, and what to do.
func importFailureAdvice(errText string) string {
	switch importFailureCode(errText) {
	case importFailDiskShort:
		return "It is too big to import right now. Ask your administrator."
	case importFailStalled:
		return "The download from your Files stopped. Try again from Cassini's Import."
	case importFailTimedOut:
		return "It took too long. Try again from Cassini's Import."
	case importFailInvalidFile:
		return "It is not a valid meeting file from Cassini."
	}
	return "Open Cassini's Import to see why and try again."
}

// notifySettled tells the importer that job finished (done) or failed with
// errText, unless they are watching. It runs in the background: a slow or
// absent Nextcloud never holds up the next import. Nothing is said for a
// cancel, which the person did themselves.
func (s *importService) notifySettled(job ImportJob, done bool, errText string) {
	if job.isCLI() || s.watchers.watching(job.CreatedBy) || !s.exapp.appAPIActive() {
		return
	}
	rt := s.rt
	ctx := rt.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	name := strings.TrimSpace(job.SourceName)
	var subject, message, link string
	base := rt.readinessBackendURL()
	if done {
		if title := strings.TrimSpace(rt.meetingFactsFor(ctx, job.ID).Title); title != "" {
			name = title
		}
		subject = "{meeting} was imported"
		link, _ = cassiniViewerURL(base, s.exapp.AppID, job.ID)
	} else {
		subject = "Import of {meeting} failed"
		message = importFailureAdvice(errText)
		link, _ = cassiniViewerURL(base, s.exapp.AppID, "")
	}
	params := map[string]any{"meeting": map[string]string{"type": "highlight", "id": job.ID, "name": notificationText(name, 80)}}
	rt.workerWG.Add(1)
	go func() {
		defer rt.workerWG.Done()
		sendCtx, cancel := context.WithTimeout(ctx, importNotifyTimeout)
		defer cancel()
		err := s.exapp.sendUserNotification(sendCtx, s.client, job.CreatedBy, subject, params, message, link)
		switch {
		case err == nil:
			s.notificationsOff.Store(false)
		case err == errNotificationsAppOff:
			// Said once until it changes: an administrator may have turned the
			// app off on purpose.
			if !s.notificationsOff.Swap(true) {
				s.logf("imports: %v, so importers are not notified when an import finishes", err)
			}
		default:
			s.logf("import %s: notify %s: %v", job.ID, job.CreatedBy, err)
		}
	}()
}
