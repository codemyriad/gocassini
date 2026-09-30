package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Retention has a deliberately narrower substrate than ordinary file reading.
// Unknown or inaccessible storage evidence is not evidence of compatibility.
func (s *annotationService) remoteRetentionCapability(ctx context.Context) error {
	client := retentionDAVClient(s.client)
	admin, err := s.exapp.resolveAdminIdentity(ctx, client, s.logger)
	if err != nil {
		return fmt.Errorf("cannot verify Nextcloud administrator capability")
	}
	get := func(route string, target any) error {
		raw, err := s.exapp.shareRequest(ctx, client, admin, http.MethodGet, strings.TrimRight(s.exapp.NextcloudURL, "/")+route, nil)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, target)
	}
	var capabilities struct {
		Version struct {
			Major int `json:"major"`
			Minor int `json:"minor"`
			Micro int `json:"micro"`
		} `json:"version"`
	}
	if err = get("/ocs/v2.php/cloud/capabilities?format=json", &capabilities); err != nil {
		return fmt.Errorf("cannot verify Nextcloud version")
	}
	v := capabilities.Version
	if v.Major < 33 || v.Major > 35 || v.Major == 33 && v.Minor == 0 && v.Micro < 9 {
		return fmt.Errorf("Nextcloud version is outside the certified retention range")
	}
	var apps struct {
		Apps []string `json:"apps"`
	}
	if err = get("/ocs/v2.php/cloud/apps?filter=enabled&format=json", &apps); err != nil {
		return fmt.Errorf("cannot verify enabled storage and access-control apps")
	}
	for _, app := range apps.Apps {
		switch app {
		case "files_accesscontrol", "files_automatedtagging", "encryption", "files_external", "end_to_end_encryption":
			return fmt.Errorf("remote retention is unavailable with %s enabled; MIME/name access rules and this storage configuration are not certified", app)
		}
	}
	var info struct {
		Nextcloud struct {
			Storage struct {
				Home  *int `json:"num_storages_home"`
				Other *int `json:"num_storages_other"`
			} `json:"storage"`
		} `json:"nextcloud"`
	}
	if err = get("/ocs/v2.php/apps/serverinfo/api/v1/info?format=json", &info); err != nil {
		return fmt.Errorf("enable Nextcloud serverinfo so retention can verify the storage backend")
	}
	storage := info.Nextcloud.Storage
	if storage.Home == nil || storage.Other == nil || *storage.Home < 1 || *storage.Other != 0 {
		return fmt.Errorf("remote retention requires verified local home storage; object or other storage is not certified")
	}
	exists, err := s.exapp.privateArchiveRoot(ctx, client)
	if err != nil || !exists {
		return fmt.Errorf("Cassini archive must be an existing private home folder")
	}
	return nil
}
