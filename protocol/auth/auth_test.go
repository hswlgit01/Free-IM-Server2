package auth

import (
	"testing"

	"github.com/openimsdk/open-im-server/v3/protocol/constant"
)

func TestForceLogoutReqCheckPlatformRange(t *testing.T) {
	tests := []struct {
		name       string
		platformID int32
		wantErr    bool
	}{
		{name: "iOS", platformID: constant.IOSPlatformID},
		{name: "admin", platformID: constant.AdminPlatformID},
		{name: "H5", platformID: constant.H5PlatformID},
		{name: "H5Web", platformID: constant.H5WebPlatformID},
		{name: "below range", platformID: constant.IOSPlatformID - 1, wantErr: true},
		{name: "above range", platformID: constant.H5WebPlatformID + 1, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (&ForceLogoutReq{UserID: "user-1", PlatformID: tt.platformID}).Check()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Check() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBatchForceLogoutReqCheckPlatformRange(t *testing.T) {
	for _, platformID := range []int32{constant.H5PlatformID, constant.H5WebPlatformID} {
		req := &BatchForceLogoutReq{Items: []*ForceLogoutItem{{UserID: "user-1", PlatformID: platformID}}}
		if err := req.Check(); err != nil {
			t.Fatalf("platform %d should be accepted: %v", platformID, err)
		}
	}

	req := &BatchForceLogoutReq{Items: []*ForceLogoutItem{{
		UserID:     "user-1",
		PlatformID: constant.H5WebPlatformID + 1,
	}}}
	if err := req.Check(); err == nil {
		t.Fatal("platform above H5Web should be rejected")
	}
}
