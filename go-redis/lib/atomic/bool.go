package atomic

import "sync/atomic"

type AtomicBool uint32

func (ab *AtomicBool) Get() bool {
	return atomic.LoadUint32((*uint32)(ab)) != 0
}

func (ab *AtomicBool) Set(v bool) {
	if v {
		atomic.StoreUint32((*uint32)(ab), 1)
	} else {
		atomic.StoreUint32((*uint32)(ab), 0)
	}
}
