package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func pullServer(t *testing.T, handler func(w http.ResponseWriter, model string)) *Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if r.URL.Path != "/api/pull" || json.NewDecoder(r.Body).Decode(&req) != nil || !req.Stream {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		handler(w, req.Model)
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL)
}

func TestPullSumsLayers(t *testing.T) {
	c := pullServer(t, func(w http.ResponseWriter, _ string) {
		io.WriteString(w, `{"status":"pulling manifest"}
{"status":"pulling aaa","digest":"sha256:aaa","total":1000,"completed":0}
{"status":"pulling aaa","digest":"sha256:aaa","total":1000,"completed":600}
{"status":"pulling bbb","digest":"sha256:bbb","total":100,"completed":100}
{"status":"pulling aaa","digest":"sha256:aaa","total":1000,"completed":1000}
{"status":"verifying sha256 digest"}
{"status":"writing manifest"}
{"status":"success"}
`)
	})
	var got []string
	err := c.Pull(t.Context(), "m", func(status string, done, total int64) {
		got = append(got, status+":"+strconv.FormatInt(done, 10)+"/"+strconv.FormatInt(total, 10))
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "pulling manifest:0/0 pulling aaa:0/1000 pulling aaa:600/1000 pulling bbb:700/1100 pulling aaa:1100/1100 " +
		"verifying sha256 digest:1100/1100 writing manifest:1100/1100 success:1100/1100"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %s", strings.Join(got, " "))
	}
}

func TestPullErrors(t *testing.T) {
	cases := []struct {
		name string
		body func(w http.ResponseWriter)
		want string
	}{
		{"error line", func(w http.ResponseWriter) {
			io.WriteString(w, "{\"status\":\"pulling manifest\"}\n{\"error\":\"pull model manifest: file does not exist\"}\n")
		}, "ollama pull m: pull model manifest: file does not exist"},
		{"stream cut", func(w http.ResponseWriter) {
			io.WriteString(w, "{\"status\":\"pulling manifest\"}\n")
		}, "stream ended before success"},
		{"http error", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"model not found"}`)
		}, "ollama pull m: model not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := pullServer(t, func(w http.ResponseWriter, _ string) { tc.body(w) })
			err := c.Pull(t.Context(), "m", nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestPullOutlastsClientTimeout(t *testing.T) {
	c := pullServer(t, func(w http.ResponseWriter, _ string) {
		io.WriteString(w, "{\"status\":\"pulling manifest\"}\n")
		w.(http.Flusher).Flush()
		time.Sleep(300 * time.Millisecond)
		io.WriteString(w, "{\"status\":\"success\"}\n")
	})
	c.http.Timeout = 100 * time.Millisecond
	if err := c.Pull(t.Context(), "m", nil); err != nil {
		t.Fatal(err)
	}
}

func TestPullCancel(t *testing.T) {
	release := make(chan struct{})
	c := pullServer(t, func(w http.ResponseWriter, _ string) {
		io.WriteString(w, "{\"status\":\"pulling manifest\"}\n")
		w.(http.Flusher).Flush()
		<-release
	})
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithCancel(t.Context())
	err := c.Pull(ctx, "m", func(string, int64, int64) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
