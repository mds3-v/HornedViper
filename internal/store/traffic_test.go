package store

import (
	"context"
	"path/filepath"
	"testing"

	"hornedviper/internal/domain"
)

func TestTrafficTracksConversationID(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "traffic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.RecordTraffic(ctx, domain.Traffic{
		TaskID: 0, ConversationID: 42, Method: "GET", Host: "example.test",
		URL: "https://example.test/", RequestRaw: "GET /",
	}); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListTraffic(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ConversationID != 42 {
		t.Fatalf("traffic=%+v", list)
	}
}

func TestTrafficGroupsAndPagination(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "traffic-groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		if err := st.RecordTraffic(ctx, domain.Traffic{TaskID: 7, Method: "GET", Host: "a.test", URL: "https://a.test/"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RecordTraffic(ctx, domain.Traffic{TaskID: 8, Method: "GET", Host: "b.test", URL: "https://b.test/"}); err != nil {
		t.Fatal(err)
	}
	groups, err := st.TrafficGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].TaskID != 7 && groups[1].TaskID != 7 {
		t.Fatalf("groups=%+v", groups)
	}
	total, err := st.CountTraffic(ctx, 7, 0)
	if err != nil || total != 12 {
		t.Fatalf("total=%d err=%v", total, err)
	}
	matched, err := st.CountTrafficSearch(ctx, 7, 0, "a.test")
	if err != nil || matched != 12 {
		t.Fatalf("search total=%d err=%v", matched, err)
	}
	none, err := st.CountTrafficSearch(ctx, 7, 0, "no-such-token")
	if err != nil || none != 0 {
		t.Fatalf("search none=%d err=%v", none, err)
	}
	page, err := st.ListTrafficPage(ctx, 7, 0, 10, 10)
	if err != nil || len(page) != 2 {
		t.Fatalf("page=%d err=%v", len(page), err)
	}
}
