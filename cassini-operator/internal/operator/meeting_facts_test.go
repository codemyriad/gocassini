package operator

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The sqlite driver with every statement counted: the test below asks how
// many queries a lookup costs, which no fake in this package can answer.
var (
	sqlStatements    atomic.Int64
	registerCounting sync.Once
)

type countingDriver struct{ inner driver.Driver }

func (d countingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return countingConn{conn}, nil
}

// countingConn exposes driver.Conn alone, so database/sql prepares every
// statement through Prepare, where it is counted.
type countingConn struct{ driver.Conn }

func (c countingConn) Prepare(query string) (driver.Stmt, error) {
	sqlStatements.Add(1)
	return c.Conn.Prepare(query)
}

func openCountingSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	registerCounting.Do(func() {
		base, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		sql.Register("counting-sqlite", countingDriver{inner: base.Driver()})
		_ = base.Close()
	})
	db, err := sql.Open("counting-sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// The facts for everything waiting for a person cost a fixed number of
// queries, however many meetings wait: /setup reads them inside a bounded
// time, and a person with a hundred recordings waiting must not run out of
// it and lose every title.
func TestMeetingFactsForManyMeetingsCostAFixedNumberOfQueries(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	ctx := context.Background()
	ids := make([]string, 0, 150)
	for i := 1; i <= 150; i++ {
		id := fmt.Sprintf("J%03d", i)
		ids = append(ids, id)
		switch i % 3 {
		case 0: // Recorded and indexed.
			seedJobRow(t, rt.store.db, seededJobRow{ID: id, Stage: "done", State: "succeeded", CreatedAt: "2026-10-30T09:00:00Z"})
			setJobTimes(t, rt, id, "2026-10-30T10:00:00.000000000Z", "2026-10-30T10:45:00.000000000Z")
			indexMeeting(t, rt, id+".opus", "Meeting "+id, 2520000)
		case 1: // An import: no job, a lifecycle row.
			if _, err := rt.store.db.Exec(`INSERT INTO meeting_lifecycle(name,file_id,document_path,state,age_anchor,anchor_source,created_at_utc,recorded_at_local,reason)
VALUES(?,?,?,'active','2026-09-01T08:00:00Z','createdAtUtc','2026-09-01T08:00:00Z','','')`, id+".opus", testMeetingFileID(id+".opus"), "CassiniRecordings/meetings/"+id+".opus"); err != nil {
				t.Fatal(err)
			}
			indexMeeting(t, rt, id+".opus", "Import "+id, 65*60*1000)
		case 2: // Unknown to the operator.
		}
	}
	counted := &Runtime{
		logger:          rt.logger,
		store:           &Store{db: openCountingSQLite(t, rt.cfg.DBPath)},
		meetingMetadata: &meetingMetadataStore{sidecarDB: sidecarDB{db: openCountingSQLite(t, meetingMetadataPath(rt.cfg.DBPath))}},
	}
	lookup := func(ids []string) (map[string]meetingFacts, int64) {
		sqlStatements.Store(0)
		facts := counted.meetingFactsForJobs(ctx, ids)
		return facts, sqlStatements.Load()
	}
	_, few := lookup(ids[:3])
	facts, many := lookup(ids)
	if few == 0 || many != few {
		t.Fatalf("150 meetings cost %d queries, 3 cost %d", many, few)
	}
	if len(facts) != 150 {
		t.Fatalf("facts for %d meetings, want 150", len(facts))
	}
	recorded := meetingFacts{Title: "Meeting J003", StartedAt: time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC), Duration: 42 * time.Minute}
	imported := meetingFacts{Title: "Import J001", StartedAt: time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC), Duration: 65 * time.Minute}
	if facts["J003"] != recorded || facts["J001"] != imported || facts["J002"] != (meetingFacts{}) {
		t.Fatalf("facts = %+v / %+v / %+v", facts["J003"], facts["J001"], facts["J002"])
	}
}
