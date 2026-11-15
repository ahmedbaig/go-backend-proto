package eventsx_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ahmedbaig/go-backend-http"
	"github.com/ahmedbaig/go-backend-proto/eventsx"
)

type post struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

func TestRoundTrip(t *testing.T) {
	ctx := httpx.WithIDs(context.Background(), "r1", "c1")
	ev, err := eventsx.New(ctx, "posts", "posts.created", post{ID: 7, Title: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := eventsx.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"correlationId":"c1"`, `"topic":"posts"`, `"title":"hi"`} {
		if !strings.Contains(strings.ReplaceAll(s, " ", ""), want) {
			t.Fatalf("%s missing %s", s, want)
		}
	}
	back, err := eventsx.Unmarshal(b)
	if err != nil {
		t.Fatal(err)
	}
	p, err := eventsx.Decode[post](back)
	if err != nil || p.ID != 7 || p.Title != "hi" {
		t.Fatalf("got %+v %v", p, err)
	}
}
