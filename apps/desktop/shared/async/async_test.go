package async

import "testing"

func TestLiveHoldsResultsUntilBound(t *testing.T) {
	var l Live
	posted := make(chan struct{})
	var got []string
	l.Go(func(post func(func())) {
		post(func() { got = append(got, "early") })
		close(posted)
	})
	<-posted
	if len(got) != 0 {
		t.Fatal("applied before the window existed")
	}
	l.Bind(func(f func()) { f() })
	if len(got) != 1 {
		t.Fatalf("after Bind: %v", got)
	}
}
