package appstore

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colabhive/colabhive/internal/models"
)

type flakyCreateRuntime struct {
	models.RuntimeEngine
	failCount atomic.Int32
	calls     atomic.Int32
	lastReq   models.SessionAllocRequest
}

func (f *flakyCreateRuntime) CreateSession(ctx context.Context, req models.SessionAllocRequest) (*models.RuntimeSession, error) {
	f.lastReq = req
	n := f.calls.Add(1)
	if n <= f.failCount.Load() {
		return nil, errors.New("no eligible pooled account available for requested accelerator (pooled-account(G4): excluded after failed attempt) (prior attempt: colab backend HTTP 503)")
	}
	return &models.RuntimeSession{
		ID:          "sess_waitlist_ok",
		AccountID:   "acc_1",
		Accelerator: req.Accelerator,
		Status:      models.SessionIdle,
	}, nil
}

func TestIsTransientGPUShortageError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("colab backend HTTP 503 (资源紧张)"), true},
		{errors.New("colab backend HTTP 412 (Google Colab 后端临时返回错误页)"), true},
		{errors.New("no eligible pooled account available (pooled-account(G4): health=MAX_CONCURRENCY)"), true},
		{errors.New("gpu A100 cooldown"), true},
		{errors.New("invalid preferred_gpu \"RTX9090\""), false},
		{errors.New("unauthorized token"), false},
		{context.Canceled, false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := isTransientGPUShortageError(tc.err); got != tc.want {
			t.Errorf("isTransientGPUShortageError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestAllocateSessionWithWaitlistRetriesAndSucceeds(t *testing.T) {
	orig := queueRetryScheduleOverride
	queueRetryScheduleOverride = []time.Duration{15 * time.Millisecond, 20 * time.Millisecond}
	defer func() { queueRetryScheduleOverride = orig }()

	rt := &flakyCreateRuntime{}
	rt.failCount.Store(2)
	m := &Manager{rt: rt}

	inst := &models.AppInstance{
		ID:    "app_test_waitlist",
		State: models.AppStateStarting,
	}
	sess, err := m.allocateSessionWithWaitlist(
		context.Background(),
		inst,
		models.SessionAllocRequest{Accelerator: models.AcceleratorG4, AutoFallback: true},
		true,
		2*time.Second,
	)
	if err != nil {
		t.Fatalf("expected waitlist to succeed on 3rd attempt, got err: %v", err)
	}
	if sess == nil || sess.ID != "sess_waitlist_ok" {
		t.Fatalf("unexpected session: %+v", sess)
	}
	if rt.calls.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", rt.calls.Load())
	}
	if !rt.lastReq.AutoFallback {
		t.Fatalf("expected AutoFallback=true to propagate")
	}
	joinedLogs := strings.Join(inst.DeployLogs, "\n")
	if !strings.Contains(joinedLogs, "候补抢卡成功") {
		t.Fatalf("expected waitlist success log, got: %s", joinedLogs)
	}
}

func TestAllocateSessionWithWaitlistCancelsImmediately(t *testing.T) {
	orig := queueRetryScheduleOverride
	queueRetryScheduleOverride = []time.Duration{10 * time.Second}
	defer func() { queueRetryScheduleOverride = orig }()

	rt := &flakyCreateRuntime{}
	rt.failCount.Store(99)
	m := &Manager{rt: rt}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(40 * time.Millisecond)
		cancel()
	}()

	inst := &models.AppInstance{ID: "app_test_cancel", State: models.AppStateStarting}
	t0 := time.Now()
	_, err := m.allocateSessionWithWaitlist(ctx, inst, models.SessionAllocRequest{Accelerator: models.AcceleratorG4}, true, time.Minute)
	elapsed := time.Since(t0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("cancel took too long: %v", elapsed)
	}
}
