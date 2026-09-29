package operator

// The only browser-side setup action is creating the recordings owner. Core
// Nextcloud may require the administrator's password-confirmed session.
type storageSetupStep struct {
	ID      string            `json:"id"`
	Action  string            `json:"action"`
	Title   string            `json:"title"`
	Args    map[string]string `json:"args,omitempty"`
	Browser bool              `json:"browser"`
	Occ     string            `json:"occ,omitempty"`
}

func storageSetupPlan(probe ncStorageProbe) []storageSetupStep {
	if probe.ServiceAccount {
		return nil
	}
	return []storageSetupStep{{
		ID: "account", Action: "create_user", Title: "Create the Cassini recordings account",
		Args:    map[string]string{"user": ncRecordingsOwner, "display_name": ncRecordingsOwnerDisplayName},
		Browser: true, Occ: "occ user:add " + ncRecordingsOwner,
	}}
}
