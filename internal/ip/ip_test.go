package ip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func setSources(t *testing.T, handlers []http.Handler) []string {
	t.Helper()
	urls := make([]string, 0, len(handlers))
	for _, h := range handlers {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		urls = append(urls, srv.URL)
	}
	return urls
}

func TestDetectAgreement(t *testing.T) {
	old := Sources
	t.Cleanup(func() { Sources = old })
	Sources = setSources(t, []http.Handler{
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("50.47.220.127"))
		}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("50.47.220.127"))
		}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("50.47.220.127"))
		}),
	})

	addr, err := Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if got := addr.String(); got != "50.47.220.127" {
		t.Fatalf("want 50.47.220.127, got %s", got)
	}
}

func TestDetectMajority(t *testing.T) {
	old := Sources
	t.Cleanup(func() { Sources = old })
	Sources = setSources(t, []http.Handler{
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("50.47.220.127"))
		}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("50.47.220.127"))
		}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("1.2.3.4"))
		}),
	})

	addr, err := Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if got := addr.String(); got != "50.47.220.127" {
		t.Fatalf("majority should win, got %s", got)
	}
}

func TestDetectDisagreement(t *testing.T) {
	old := Sources
	t.Cleanup(func() { Sources = old })
	Sources = setSources(t, []http.Handler{
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("1.1.1.1"))
		}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("2.2.2.2"))
		}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("3.3.3.3"))
		}),
	})

	_, err := Detect(context.Background())
	if err == nil {
		t.Fatal("want error when sources disagree")
	}
}

func TestDetectSourceFailure(t *testing.T) {
	old := Sources
	t.Cleanup(func() { Sources = old })
	// Two dead sources, one alive — below the required agreement of 2.
	Sources = setSources(t, []http.Handler{
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("50.47.220.127"))
		}),
	})

	_, err := Detect(context.Background())
	if err == nil {
		t.Fatal("want error when fewer than two sources respond")
	}
}
