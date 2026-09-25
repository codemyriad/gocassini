package operator

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

const storageStepServiceAccount = "owner_account"

type ncStorageProbe struct {
	AdminUser             string
	ServiceAccount        bool
	ServiceAccountAttempt string
	PrivateRoot           bool
}

func (p ncStorageProbe) serviceAccountDetail() string {
	if p.ServiceAccountAttempt != "" {
		return p.ServiceAccountAttempt
	}
	return "the Cassini recordings account is missing"
}

func (c ExAppConfig) probeNCStorage(ctx context.Context, client *http.Client, logger *log.Logger) (ncStorageProbe, error) {
	admin, err := c.resolveAdminIdentity(ctx, client, logger)
	if err != nil {
		return ncStorageProbe{}, err
	}
	probe := ncStorageProbe{AdminUser: admin}
	probe.ServiceAccount, err = c.userExists(ctx, client, ncRecordingsOwner)
	if err != nil {
		return probe, fmt.Errorf("check recordings account: %w", err)
	}
	return probe, nil
}

// privateArchiveRoot checks ownership and mount type using core WebDAV
// properties. It covers incoming shares as well as Team folders without
// requiring an app-list or Group Folders API call.
func (c ExAppConfig) privateArchiveRoot(ctx context.Context, client *http.Client) (exists bool, err error) {
	endpoint := c.davFileURL(ncRecordingsOwner, ncRecordingsRoot)
	body := []byte(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns" xmlns:nc="http://nextcloud.org/ns"><d:prop><oc:owner-id/><nc:mount-type/></d:prop></d:propfind>`)
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", endpoint, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	c.setAppAPIDAVHeadersForUser(req, ncRecordingsOwner)
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml")
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusMultiStatus {
		return false, fmt.Errorf("private archive PROPFIND returned %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false, err
	}
	var multi struct {
		Responses []struct {
			Propstats []struct {
				Status string `xml:"status"`
				Prop   struct {
					Owner string `xml:"owner-id"`
					Mount string `xml:"mount-type"`
				} `xml:"prop"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(raw, &multi); err != nil {
		return false, fmt.Errorf("decode private archive properties: %w", err)
	}
	for _, item := range multi.Responses {
		for _, stat := range item.Propstats {
			if strings.Contains(stat.Status, " 200 ") {
				if strings.TrimSpace(stat.Prop.Owner) != ncRecordingsOwner || strings.TrimSpace(stat.Prop.Mount) != "" {
					return true, fmt.Errorf("recordings root is not a private folder owned by %q", ncRecordingsOwner)
				}
				return true, nil
			}
		}
	}
	return true, fmt.Errorf("private archive properties were not returned")
}
