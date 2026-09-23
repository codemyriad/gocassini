package operator

import (
	"context"
	"errors"
	"log"
	"net/http"
)

// preflightDirectShares checks only core Nextcloud account, DAV and sharing
// capabilities. It does not require Group Folders or Everyone Group.
func (c ExAppConfig) preflightDirectShares(ctx context.Context, logger *log.Logger) {
	if !c.appAPIActive() {
		return
	}
	provisionMu.Lock()
	defer provisionMu.Unlock()
	ncAccessSubstrate.beginRun()
	client := &http.Client{Timeout: ncProvisionTimeout}
	probe, err := c.probeNCStorage(ctx, client, logger)
	if err != nil {
		ncAccessSubstrate.degraded("nextcloud_probe", err)
		return
	}
	ncAccessSubstrate.setAdminUser(probe.AdminUser)
	c.ensureServiceAccountOnEnable(ctx, client, &probe, logger)
	if !probe.ServiceAccount {
		ncAccessSubstrate.setProbe(probe)
		ncAccessSubstrate.unavailable(storageStepServiceAccount, errors.New(probe.serviceAccountDetail()))
		return
	}
	for _, dir := range recordingsTreeDirs(ncRecordingsRoot) {
		if dir == ncRecordingsRoot {
			if _, err := c.privateArchiveRoot(ctx, client); err != nil {
				ncAccessSubstrate.degraded("private_archive", err)
				return
			}
		}
		if err := c.davMkcol(ctx, client, ncRecordingsOwner, dir); err != nil {
			ncAccessSubstrate.degraded("private_archive", err)
			return
		}
	}
	if _, err := c.privateArchiveRoot(ctx, client); err != nil {
		ncAccessSubstrate.degraded("private_archive", err)
		return
	}
	probe.PrivateRoot = true
	ncAccessSubstrate.setProbe(probe)
	if _, err := c.shareRequest(ctx, client, ncRecordingsOwner, http.MethodGet, c.shareAPIURL(), nil); err != nil {
		ncAccessSubstrate.unavailable("sharing_api", err)
		return
	}
	ncAccessSubstrate.succeed()
}
