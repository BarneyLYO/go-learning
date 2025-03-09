package wait

import (
	"sync"
	"time"
)

type Wait struct {
	sync.WaitGroup
}

/*
return true if timeout
*/
func (w *Wait) WaitTill(timeout time.Duration) bool {
	ch := make(chan bool, 1)
	go func() {
		defer close(ch)
		w.Wait()
		ch <- true
	}()

	select {
	case <-ch:
		return false
	case <-time.After(timeout):
		return true // timout
	}
}
