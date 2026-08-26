package vanishedjob

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

type fakeRunClient struct{ cancels, reruns int }

func (client *fakeRunClient) ForceCancel(context.Context, string, int64) error {
	client.cancels++
	return nil
}
func (client *fakeRunClient) FullRerun(context.Context, string, int64) error {
	client.reruns++
	return nil
}

type eventLog struct{ events []Event }

func (log *eventLog) Emit(_ context.Context, event Event) error {
	log.events = append(log.events, event)
	return nil
}

func TestControllerExecutesAndPersistsOneFullRecovery(t *testing.T) {
	now := time.Date(2026, 8, 26, 14, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	client, events := &fakeRunClient{}, &eventLog{}
	controller := Controller{
		Policy: testPolicy(t), Store: FileStore{Path: filepath.Join(directory, "state.json"), LockPath: filepath.Join(directory, "state.lock")},
		Client: client, Events: events, Now: func() time.Time { return now },
	}
	job := testJob(now)
	decision, err := controller.Reconcile(context.Background(), job)
	if err != nil || decision.Action != ActionForceCancel || client.cancels != 1 {
		t.Fatalf("cancel decision=%#v client=%#v err=%v", decision, client, err)
	}
	job.RunStatus, job.RunConclusion = "completed", "cancelled"
	decision, err = controller.Reconcile(context.Background(), job)
	if err != nil || decision.Action != ActionFullRerun || client.reruns != 1 {
		t.Fatalf("rerun decision=%#v client=%#v err=%v", decision, client, err)
	}
	job.RunAttempt, job.RunStatus, job.RunConclusion = 2, "completed", "success"
	decision, err = controller.Reconcile(context.Background(), job)
	if err != nil || decision.Action != ActionComplete || client.reruns != 1 {
		t.Fatalf("complete decision=%#v client=%#v err=%v", decision, client, err)
	}
	key := RecordKey(job.Repository, job.RunID, 1)
	if record, err := controller.Store.Get(key); err != nil || record != nil {
		t.Fatalf("terminal record=%#v err=%v", record, err)
	}
	if len(events.events) != 3 {
		t.Fatalf("events=%#v", events.events)
	}
}
