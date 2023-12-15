package util

import (
	"fmt"
	"time"

	sync "github.com/yylt/enipam/pkg/lock"

	"k8s.io/klog/v2"
)

// Parameters are the user specified parameters
type Parameters struct {
	// MinInterval is the minimum required interval between invocations of
	// TriggerFunc
	MinInterval time.Duration

	// TriggerFunc is the function to be called when Trigger() is called
	// while respecting MinInterval and serialization
	TriggerFunc func()

	// ShutdownFunc is called when the trigger is shut down
	ShutdownFunc func()

	// Name is the unique name of the trigger. It must be provided in a
	// format compatible to be used as prometheus name string.
	Name string
}

// Trigger represents an active trigger logic. Use NewTrigger() to create a
// trigger
type Trigger struct {
	// protect mutual access of 'trigger' between Trigger() and waiter()
	mutex   sync.RWMutex
	trigger bool

	// params are the user specified parameters
	params Parameters

	// lastTrigger is the timestamp of the last invoked trigger
	lastTrigger time.Time

	// wakeupCan is used to wake up the background trigger routine
	wakeupChan chan struct{}

	// closeChan is used to stop the background trigger routine
	closeChan chan struct{}

	// numFolds is the current count of folds that happened into the
	// currently scheduled trigger
	numFolds int
}

// NewTrigger returns a new trigger based on the provided parameters
func NewTrigger(p Parameters) (*Trigger, error) {

	if p.TriggerFunc == nil {
		return nil, fmt.Errorf("trigger function is nil")
	}

	t := &Trigger{
		params:     p,
		wakeupChan: make(chan struct{}, 1),
		closeChan:  make(chan struct{}, 1),
	}

	// Guarantee that initial trigger has no delay
	if p.MinInterval > time.Duration(0) {
		t.lastTrigger = time.Now().Add(-1 * p.MinInterval)
	}

	go t.waiter()

	return t, nil
}

// needsDelay returns whether and how long of a delay is required to fullfil
// MinInterval
func (t *Trigger) needsDelay() (bool, time.Duration) {
	if t.params.MinInterval == time.Duration(0) {
		return false, 0
	}

	sleepTime := time.Since(t.lastTrigger.Add(t.params.MinInterval))
	return sleepTime < 0, sleepTime * -1
}

// Trigger triggers the call to TriggerFunc as specified in the parameters
// provided to NewTrigger(). It respects MinInterval and ensures that calls to
// TriggerFunc are serialized. This function is non-blocking and will return
// immediately before TriggerFunc is potentially triggered and has completed.
func (t *Trigger) Trigger() {
	t.mutex.Lock()
	t.trigger = true
	t.numFolds++
	t.mutex.Unlock()

	select {
	case t.wakeupChan <- struct{}{}:
	default:
	}
}

// Shutdown stops the trigger mechanism
func (t *Trigger) Shutdown() {
	close(t.closeChan)
}

func (t *Trigger) waiter() {
	for {
		// keep critical section as small as possible
		t.mutex.Lock()
		triggerEnabled := t.trigger
		t.trigger = false
		t.mutex.Unlock()

		// run the trigger function
		if triggerEnabled {
			if delayNeeded, delay := t.needsDelay(); delayNeeded {
				time.Sleep(delay)
			}

			t.mutex.Lock()
			t.lastTrigger = time.Now()
			klog.Infof("trigger %s had trigger number %d before start", t.params.Name, t.numFolds)
			t.numFolds = 0
			t.mutex.Unlock()

			t.params.TriggerFunc()
		}

		select {
		case <-t.wakeupChan:
		case <-t.closeChan:
			shutdownFunc := t.params.ShutdownFunc
			if shutdownFunc != nil {
				shutdownFunc()
			}
			return
		}
	}
}
