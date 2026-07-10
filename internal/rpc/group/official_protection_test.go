package group

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	commonconfig "github.com/openimsdk/open-im-server/v3/pkg/common/config"
)

func TestCheckUserHasOfficialProtectionRequiresCompleteData(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    bool
		wantErr bool
	}{
		{name: "protected", body: `{"errCode":0,"data":{"has_protection":true}}`, want: true},
		{name: "explicitly unprotected", body: `{"errCode":0,"data":{"has_protection":false}}`},
		{name: "missing data", body: `{"errCode":0}`, wantErr: true},
		{name: "missing protection field", body: `{"errCode":0,"data":{}}`, wantErr: true},
		{name: "business error", body: `{"errCode":500,"data":{"has_protection":false}}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			g := &groupServer{
				config:     &Config{Share: commonconfig.Share{ChatAPIURL: server.URL, Secret: "secret"}},
				httpClient: server.Client(),
			}
			got, err := g.checkUserHasOfficialProtection(context.Background(), "user-1")
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("result = %v, want %v", got, tt.want)
			}
		})
	}
}
