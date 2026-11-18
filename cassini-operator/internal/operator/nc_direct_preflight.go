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
	c.preflightDirectSharesLocked(ctx, logger)
}

// recheckDegradedStorage repeats the storage check when the last one ended
// "degraded" because Nextcloud did not answer: the signature of a container
// that started before Nextcloud did. It runs on the storage-usage timer, so it
// never waits behind another check, and it leaves every other verdict alone:
// a healthy install costs no request, and a real fault waits for a publish or
// an administrator.
func (c ExAppConfig) recheckDegradedStorage(ctx context.Context, logger *log.Logger) {
	if !c.appAPIActive() || !provisionMu.TryLock() {
		return
	}
	defer provisionMu.Unlock()
	snap := ncAccessSubstrate.snapshot("")
	if snap.State != string(ncSubstrateDegraded) || snap.Step != "nextcloud_probe" {
		return
	}
	c.preflightDirectSharesLocked(ctx, logger)
}

func (c ExAppConfig) preflightDirectSharesLocked(ctx context.Context, logger *log.Logger) {
	ncAccessSubstrate.beginRun()
	client := &http.Client{Timeout: ncProvisionTimeout}
	probe, err := c.probeNCStorage(ctx, client, logger)
	if err != nil {
		ncAccessSubstrate.degraded("nextcloud_probe", err)
		return
	}
	ncAccessSubstrate.setAdminUser(probe.AdminUser)
	if err := c.ensureServiceAccountOnEnable(ctx, client, &probe, logger); err != nil {
		ncAccessSubstrate.degraded("nextcloud_probe", err)
		return
	}
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
		c.supportShare(ctx, "share_list", "", "", err, 0)
		// Only Nextcloud's own refusal (the share API is off) blocks
		// recording. A server fault, a proxy page, a rate limit or a timeout
		// proves nothing, so it is degraded: recording continues and publish
		// checks again.
		var refused *ncShareError
		if errors.As(err, &refused) && (refused.HTTPStatus/100 == 2 || refused.HTTPStatus == refused.OCSStatus) &&
			(refused.OCSStatus == http.StatusUnauthorized || refused.OCSStatus == http.StatusForbidden || refused.OCSStatus == http.StatusNotFound) {
			ncAccessSubstrate.unavailable("sharing_api", err)
		} else {
			ncAccessSubstrate.degraded("sharing_api", err)
		}
		// The most common reason the share API refuses is that it is turned
		// off, and only the sharing settings say so. Read them here too, so the
		// storage row can name the setting instead of a generic failure.
		c.checkSharingPolicy(ctx, client, probe.AdminUser, logger)
		return
	}
	ncAccessSubstrate.succeed()
	// The share API answering proves nothing about whether a share will be
	// accepted; the instance's sharing policy decides that (D-885). Recorded
	// beside the storage verdict, after it, so storage never waits on it.
	c.checkSharingPolicy(ctx, client, probe.AdminUser, logger)
}
