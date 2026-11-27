package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The 202 for a message carries the log's last seq when the daemon took
// it. A log with no events yet is at seq 0, which is a seq; an older
// daemon sends none.
func TestSendMessageReadsTheReceipt(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want Receipt
	}{
		{"seq", `{"status":"injected","seq":42}`, Receipt{Status: "injected", Seq: 42, HasSeq: true}},
		{"seq zero", `{"status":"accepted","seq":0}`, Receipt{Status: "accepted", Seq: 0, HasSeq: true}},
		{"no seq", `{"status":"accepted"}`, Receipt{Status: "accepted"}},
		{"no body", ``, Receipt{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/sessions/s1/messages" {
					http.NotFound(w, r)
					return
				}
				if tc.body != "" {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			got, err := New(srv.URL).SendMessage(context.Background(), "s1", "hello")
			if err != nil {
				t.Fatalf("SendMessage: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
