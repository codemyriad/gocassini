package operator

import (
	"fmt"
	"testing"
)

// A stock Nextcloud: neither app enabled, no service account, no folder.
func TestReproPlanOrder(t *testing.T) {
	probe := ncStorageProbe{
		Prereqs: []ncPrerequisiteStatus{
			{Name: ncAppGroupFolders, State: ncPrerequisiteMissing},
			{Name: ncAppEveryoneGroup, State: ncPrerequisiteMissing},
		},
		FolderProbed: true,
	}
	for i, s := range storageSetupPlan(true, probe) {
		fmt.Printf("%d id=%-20s action=%-20s browser=%v\n", i, s.ID, s.Action, s.Browser)
	}
	ok, step, _ := probe.sanityForTarget(true)
	fmt.Printf("available=%v step=%s\n", ok, step)
}
