package operator

type storageCause struct {
	admin string
	user  string
}

var storageCauses = map[string]storageCause{
	"nextcloud_probe":         {"Nextcloud did not answer the recordings account check.", "Nextcloud did not answer the recordings setup check."},
	"private_archive":         {"Cassini could not confirm its recordings folder is private and writable.", "The recordings folder could not be confirmed private and writable."},
	"sharing_api":             {"Nextcloud file sharing is unavailable to the Cassini recordings account.", "Nextcloud file sharing is unavailable for recordings."},
	storageStepServiceAccount: {"The Cassini recordings account does not exist.", "The account that saves recordings does not exist."},
}

func storageCauseFor(step string) string     { return storageCauses[step].admin }
func storageUserCauseFor(step string) string { return storageCauses[step].user }
