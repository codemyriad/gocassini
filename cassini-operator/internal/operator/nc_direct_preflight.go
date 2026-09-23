package operator

import (
	"context"
	"errors"
	"log"
	"net/http"
)

// preflightDirectShares checks only the prerequisites of the direct-share
// model. The groupfolders and group_everyone apps are optional and never gate
// recording. It reuses the owner-account probe while the old setup API is
// being retired.
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
	ncAccessSubstrate.setProbe(probe)
	ncAccessSubstrate.setPrerequisites(nil)
	ncStorage.set(false, storageModeSourceResolved, true)
	ncAccessSubstrate.setMode(storageModeName(false), storageModeSourceResolved)
	if !probe.ServiceAccount {
		ncAccessSubstrate.unavailable(storageStepServiceAccount, errors.New("the cassini service account is missing"))
		return
	}
	if !probe.DefaultRootProbed || probe.DefaultRootShadowed {
		ncAccessSubstrate.unavailable("private_archive", errors.New("the private archive path could not be confirmed free of Team folder mounts"))
		return
	}
	for _, dir := range recordingsTreeDirs(ncDefaultRecordingsRoot) {
		if err := c.davMkcol(ctx, client, ncRecordingsOwner, dir); err != nil {
			ncAccessSubstrate.degraded("private_archive", err)
			return
		}
	}
	if _, err := c.shareRequest(ctx, client, ncRecordingsOwner, http.MethodGet, c.shareAPIURL(), nil); err != nil {
		ncAccessSubstrate.unavailable("sharing_api", err)
		return
	}
	ncAccessSubstrate.succeed()
}
