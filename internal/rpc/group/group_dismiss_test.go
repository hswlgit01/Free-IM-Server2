package group

import (
	"testing"

	pbgroup "github.com/openimsdk/open-im-server/v3/protocol/group"
)

func TestShouldCleanupDismissedGroupMembersAfterNotification(t *testing.T) {
	if !shouldCleanupDismissedGroupMembersAfterNotification(&pbgroup.DismissGroupReq{}) {
		t.Fatal("normal dismiss should cleanup members after notification so the group cannot reappear after sync")
	}
	if shouldCleanupDismissedGroupMembersAfterNotification(&pbgroup.DismissGroupReq{DeleteMember: true}) {
		t.Fatal("explicit member deletion should not run the post-notification cleanup twice")
	}
}
