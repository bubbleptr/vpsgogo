// Package async runs the views' slow work off the UI thread and brings its
// results back to it.
package async

import "sync"

// Runner runs a task in the background. The task hands its results to post,
// which applies them on the UI thread and redraws: views change their state
// only inside post.
type Runner interface {
	Go(task func(post func(apply func())))
}

// Live runs tasks in goroutines. Its update is mygo's Window.Update, which
// exists only once the window does: results posted before Bind wait for it.
type Live struct {
	mu      sync.Mutex
	update  func(func())
	waiting []func()
}

// Bind sets the function that applies results on the UI thread, such as
// (*mygo.Window).Update, and applies those that waited for it.
func (l *Live) Bind(update func(func())) {
	l.mu.Lock()
	l.update = update
	waiting := l.waiting
	l.waiting = nil
	l.mu.Unlock()
	for _, f := range waiting {
		update(f)
	}
}

func (l *Live) Go(task func(post func(func()))) {
	go task(func(apply func()) {
		l.mu.Lock()
		update := l.update
		if update == nil {
			l.waiting = append(l.waiting, apply)
		}
		l.mu.Unlock()
		if update != nil {
			update(apply)
		}
	})
}

// Manual holds tasks until Flush, for tests: what a view shows while it
// waits and after can both be checked, with no goroutines.
type Manual struct {
	tasks []func(post func(func()))
}

func (m *Manual) Go(task func(post func(func()))) { m.tasks = append(m.tasks, task) }

// Pending reports how many tasks wait for Flush.
func (m *Manual) Pending() int { return len(m.tasks) }

// Flush runs the waiting tasks, and those they start, applying their
// results as they post them. Follow it with Tester.Frame to see them.
func (m *Manual) Flush() {
	for len(m.tasks) > 0 {
		t := m.tasks[0]
		m.tasks = m.tasks[1:]
		t(func(apply func()) { apply() })
	}
}
