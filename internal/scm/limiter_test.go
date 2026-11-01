package scm_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

type clock struct {
	at time.Time
}

func newClock() *clock {
	return &clock{at: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) now() time.Time {
	return c.at
}

func (c *clock) advance(by time.Duration) {
	c.at = c.at.Add(by)
}

func write(limiter *scm.Limiter, installation int64) error {
	release, err := limiter.Admit(installation, true)
	if err == nil {
		release()
	}
	return err
}

func TestTheEightyFirstWriteInAMinuteIsRefusedWithItsWait(t *testing.T) {
	clock := newClock()
	limiter := scm.NewLimiter(clock.now)
	if err := write(limiter, 1); err != nil {
		t.Fatal(err)
	}
	clock.advance(10 * time.Second)
	for index := range 79 {
		if err := write(limiter, 1); err != nil {
			t.Fatalf("write %d: %v", index, err)
		}
	}
	var refused *scm.LimitError
	if err := write(limiter, 1); !errors.As(err, &refused) {
		t.Fatalf("err = %v", err)
	}
	if refused.Wait != 50*time.Second {
		t.Errorf("wait = %s", refused.Wait)
	}
}

func TestASpentWriteReturnsExactlyOneMinuteLater(t *testing.T) {
	clock := newClock()
	limiter := scm.NewLimiter(clock.now)
	for range 80 {
		if err := write(limiter, 1); err != nil {
			t.Fatal(err)
		}
	}
	clock.advance(time.Minute - 1)
	if write(limiter, 1) == nil {
		t.Error("a write passed before its token returned")
	}
	clock.advance(1)
	if err := write(limiter, 1); err != nil {
		t.Errorf("the token did not return: %v", err)
	}
}

func TestNoRollingWindowEverPassesEitherLimit(t *testing.T) {
	clock := newClock()
	limiter := scm.NewLimiter(clock.now)
	var passed []time.Time
	for range 4 * 3600 {
		for write(limiter, 1) == nil {
			passed = append(passed, clock.at)
		}
		clock.advance(time.Second)
	}
	if len(passed) < 1500 {
		t.Fatalf("only %d writes passed in four hours", len(passed))
	}
	assertRolling(t, passed, time.Minute, 80)
	assertRolling(t, passed, time.Hour, 500)
}

func assertRolling(t *testing.T, passed []time.Time, window time.Duration, limit int) {
	t.Helper()
	left := 0
	for index, at := range passed {
		for at.Sub(passed[left]) >= window {
			left++
		}
		if count := index - left + 1; count > limit {
			t.Fatalf("%d writes inside %s, ending at %s", count, window, at)
		}
	}
}

func TestTheHourLimitHoldsAfterTheMinuteLimitHasCleared(t *testing.T) {
	clock := newClock()
	limiter := scm.NewLimiter(clock.now)
	started := clock.at
	for written := 0; written < 500; {
		if write(limiter, 1) != nil {
			clock.advance(time.Minute)
			continue
		}
		written++
	}
	clock.advance(2 * time.Minute)
	var refused *scm.LimitError
	if err := write(limiter, 1); !errors.As(err, &refused) {
		t.Fatalf("write 501 after %s: %v", clock.at.Sub(started), err)
	}
	if refused.Wait != started.Add(time.Hour).Sub(clock.at) {
		t.Errorf("wait = %s", refused.Wait)
	}
}

func TestARefusedWriteTakesNothingFromTheOtherBucket(t *testing.T) {
	clock := newClock()
	limiter := scm.NewLimiter(clock.now)
	for range 80 {
		if err := write(limiter, 1); err != nil {
			t.Fatal(err)
		}
	}
	for range 1000 {
		if write(limiter, 1) == nil {
			t.Fatal("a write passed the empty minute bucket")
		}
	}
	clock.advance(time.Minute)
	for index := range 80 {
		if err := write(limiter, 1); err != nil {
			t.Fatalf("refused writes spent the hour bucket: write %d: %v", index, err)
		}
	}
	clock.advance(time.Minute)
	for written := 160; written < 500; written++ {
		for write(limiter, 1) != nil {
			clock.advance(time.Minute)
		}
	}
}

func TestAHundredRequestsInFlightHoldBackTheNext(t *testing.T) {
	limiter := scm.NewLimiter(newClock().now)
	releases := make([]func(), 0, 100)
	for range 100 {
		release, err := limiter.Admit(1, false)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	var refused *scm.LimitError
	if _, err := limiter.Admit(1, false); !errors.As(err, &refused) {
		t.Fatalf("err = %v", err)
	}
	releases[0]()
	if _, err := limiter.Admit(1, false); err != nil {
		t.Errorf("a released place stayed taken: %v", err)
	}
}

func TestAReadSpendsNoWrite(t *testing.T) {
	limiter := scm.NewLimiter(newClock().now)
	for range 1000 {
		release, err := limiter.Admit(1, false)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	if err := write(limiter, 1); err != nil {
		t.Error(err)
	}
}

func TestInstallationsHaveTheirOwnBuckets(t *testing.T) {
	limiter := scm.NewLimiter(newClock().now)
	for range 80 {
		if err := write(limiter, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := write(limiter, 2); err != nil {
		t.Errorf("installation 2 was held by installation 1: %v", err)
	}
}

func TestTheAdapterGoesThroughTheLimiterBeforeTheHost(t *testing.T) {
	stub := scmtest.New(t)
	limiter := scm.NewLimiter(newClock().now)
	for range 80 {
		if err := write(limiter, 9); err != nil {
			t.Fatal(err)
		}
	}
	host, err := scm.NewGitHub(scm.GitHubOptions{API: stub.Server.URL, Repository: "example/widgets", Tokens: scm.StaticToken(stub.Token), Limits: limiter, Installation: 9})
	if err != nil {
		t.Fatal(err)
	}
	var refused *scm.LimitError
	if _, err := host.PostCheck(context.Background(), scm.CheckRun{Completed: time.Now()}); !errors.As(err, &refused) {
		t.Fatalf("err = %v", err)
	}
	if len(stub.Requests) != 0 {
		t.Errorf("requests = %v", stub.Requests)
	}
	stub.SetPrivate(true)
	if _, err := host.Visibility(context.Background()); err != nil {
		t.Errorf("a read was held by the write limit: %v", err)
	}
	if len(stub.Requests) != 1 || stub.Requests[0][:len(http.MethodGet)] != http.MethodGet {
		t.Errorf("requests = %v", stub.Requests)
	}
}
