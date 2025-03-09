# mutex

## lock basic

### atomic operation

```go
func add (p *int32) {
    *p++ // not atomic opeation
    /*
        *p = *p + 1 // load, opeate, write to memory problem
    */
}

func addAtomically(p *int32) {
    atomic.AddInt32(p, 1)
}

func main() {
    c := int32(0)
    for i := 0; i < 1000; i++ {
        go add(&c)
    }
    time.Sleep( 50 * time.Second)
    fmt.Println(c) // sometimes less than 1000
}


// atomic.AddInt32 // 汇编实现 -> TEXT -Xadd(SB), NOSPLIT, $0-20
```

```s
TEXT -Xadd(SB), NOSPLIT, $0-20
    MOVQ ptr+0(FP), BX
    MOVL delta+8(FP), AX
    MOVL AX, CX
    LOCK # cpu级别的内存锁, cpu给内存上了lock指令
    XADDL AX, 0(BX)
    ADDL CX, AX
    MOVL AX, ret+16(FP)
    RET
```

LOCK：这是一个前缀指令，用于确保后续的指令是原子操作。在多处理器环境下，LOCK 前缀会锁定总线，防止其他处理器同时访问该内存地址，从而保证操作的原子性。

当处理器执行带有 LOCK 前缀的指令时，硬件会自动完成以下操作：
锁定总线：在执行 LOCK 前缀修饰的指令期间，处理器会向总线发出锁定信号，阻止其他处理器对同一内存区域进行访问，确保该指令的原子性。例如在代码中 LOCK XADDL AX, 0(BX) 执行时，总线被锁定。
执行指令：处理器执行 XADDL 指令，也就是将 AX 寄存器的值与 BX 所指向内存地址处的值进行交换并相加，然后将结果存回该内存地址。
自动解锁：当 XADDL 指令执行完毕，硬件会自动撤销总线锁定信号，相当于完成了 “解锁” 操作，使得其他处理器可以再次访问该内存区域。

在 go 里面， atomic 操作其实是一种硬件层面的加锁机制，保证操作一个变量的时候，其他协程或线程无法访问
只能用于简单变量的简单操作
CAS 同理

```go
atomic.CompareAndSwapInt32(&c, 10, 1000)
atomic.LoadInt64() // load the
```

### Semaphore 信号锁

核心是一个同时可并发的 uint32 的数
每一个 semaphare 对应一个 SemaRoot 结构体
SemaRoot 中有一个平衡二叉树，用于协程排队

uint32 => SemaRoot => sudog[balance tree] => G

uint32 > 0, 获取锁直接给这个 uint32 - 1 就算获取锁成功了. 释放锁直接就给 uint32 + 1 就算释放了

uint32 == 0，获取锁，协程休眠，进入堆树等待, 释放锁，从对树中取出一个协程，唤醒。 sema 锁会退化成为一个休眠队列

```golang

// runtime/sema.go
type semaRoot {
    lock   mutex
    treap  *sudog // root of balanced tree of unique waiters *
    nwait  uint32 // number of waiters. Read w/o the lock
}

type sudog struct {
    g *g
    next *sudog
    prev *sudog
    elem unsafe.Pointer // data element 用来指向接收channel传过来的值的变量地址
    // ....
}

// call from runtime
func semacquire(addr *uint32) {
    semacquire1(addr, false, 0, 0)
}

func semacquire(addr *uint32, lifo bool, profile semaProfileFlags, skipframes int) {
    gp := getg()
    if gp != gp.m.curg {
        throw("semacquire not on the G stack")
    }

    // Easy Case
    if cansemacquire(addr) {
        return
    }

    // Hard case
    // increment waiter count
    // try cansemacquire one more time, return if succeeded
    // enqueue itself as a waiter
    // sleep
    // (waiter descriptor is dequeued by signaler)
    s := acquireSudog()
    root := semroot(addr)
    t0 := int64(0)
    s.releasetime = 0
    s.acquiretime = 0
    s.ticket = 0
    if profile&semaBlockProfile != 0 && blockprofilerate > 0 {
        t0 = cputicks()
        s.releasetime = -1
    }
    if profile&semaMutexProfile != 0 && mutexprofilerate > 0 {
        if t0 == 0 {
            t0 == cputicks()
        }
        s.acquiretime = t0
    }
    for {
        lockWithRank(&root.lock, lockRankRoot)
        atomic.Xadd(&root.nwait, 1)
        if cansemacquire(addr) {
            atomic.Xadd(&root.nwait, -1)
            unlock(&root.lock)
            break
        }
        root.queue(addr, s, lifo) // 排队
        goparkunlock(&root.lock, waitReasonSemacquire, tracrEvGoBlockSync, 4 + skip) //休眠当前协程 当前协程自己休眠。 puts the current goroutine into a waiting state and unlocks the lock, the goroutine can be made runnable again by calling goready(gp) blockhere
        if s.ticket != 0 || cansemacquire(addr) {
            break
        }
    }
    if s.releasetime > 0 {
        blockevent (s.releasetime - t0, 3 + skipframes)
    }

    releaseSudog(s)
}

func cansemacquire(addr *uint32) bool {
    for {
        v := atomic.Load(addr)
        if v == 0 {
            return false
        }
        if atomic.Cas(addr, v, v - 1) {
            return true
        }
    }
}

func semrelease (addr *uint32) {
    semrelease1(addr, false, 0)
}

func semrelease1 (addr *uint32, handoff bool, skipframes int) {
    root := semroot(addr)
    atomic.Xadd(addr, 1)

    // easy case no waiters
    if atomic.Load(&root.nwait) == 0 {
        return
    }

    lockWithRank(&root.lock, lockRankRoot)
    if atomic.Load(&root.nwait) == 0 {
        unlock(&root.lock)
        return
    }
    s, t0 := root.dequeue(addr)
    if s != nil {
        atomic.Xadd(&root.nwait, -1)
    }
    unlock(&root.lock)
    if s != nil {
        acquiretime := s.acquiretime
        if acquiretime != 0 {
            mutexevent(t0 - acquiretime, 3 + skipframes)
        }
        if s.ticket != 0 {
            throw("corrupted semaphore ticket")
        }
        if handoff && cansemacquire(addr) {
            s.ticket = 1
        }
        readyWithTime(s, 5 + skipframes)
        if s.ticket == 1 && getg().m.locks == 0 {
            goyield()
        }
    }
}

func semroot(addr *uint32) *semaRoot {
	return &semtable[(uintptr(unsafe.Pointer(addr))>>3)%semTabSize].root
}

// Prime to not correlate with any user patterns.
const semTabSize = 251

var semtable [semTabSize]struct {
	root semaRoot
	pad  [sys.CacheLineSize - unsafe.Sizeof(semaRoot{})]byte
}

```

## sync.Mutex

互斥锁
go 中用于并发保护最常见方案

```go
type Person struct {
    mu sync.Mutex
    salary int
    level int
}

func (p *Person) promote() {
    p.mu.Lock()
    defer p.mu.Unlock()

    p.salary += 1000
    fmt.Println(p.salary)
    p.level += 1
    fmt.Println(p.level)
}
```

### CAS dont do it cas spinning lock, goroutine will stack here an can not be scheduled

```go
type Person struct {
    mu int32
    salary int
    level int
}

func (p *Person) promote () {
    atomic.CompareAndSwapInt32(&p.mu, 0, 1) // cas spinning lock, goroutine will stack here an can not be scheduled
    p.salary += 1000
    fmt.Println(p.salary)
    p.level += 1
    fmt.Println(p.level)
    atomic.CompareAndSwapInt32(&p.mu, 1, 0)
}
```

### sync.Mutex 正常模式

锁状态 [29bit {WaiterShift} ,1bit {Starving},1bit {Woken}, 1bit {Locked}]
Locked: 0 no, 1 yes
Woken: 0 no, 1yes
Starving: 0 no, 1 yes
waitershift 等待者数量

```go
type Mutex struct {
    state int32 // 锁状态 [29bit {WaiterShift} ,1bit {Starving},1bit {Woken}, 1bit {Locked}]
    sema unint32 // = 0 sema等待队列
}

func (m *Mutex) Lock() {
    if atomic.CompareAndSwapInt32(&m.state, 0, mutexLocked) {
        if race.Enabled {}
        return
    }
    m.lockSlow()
}

func (m *Mutex) lockSlow() {
    var waitStartTime int64
	starving := false
	awoke := false
	iter := 0
	old := m.state
	for {
		if old&(mutexLocked|mutexStarving) == mutexLocked && runtime_canSpin(iter) {
			if !awoke && old&mutexWoken == 0 && old>>mutexWaiterShift != 0 &&
				atomic.CompareAndSwapInt32(&m.state, old, old|mutexWoken) {
				awoke = true
			}
			runtime_doSpin()
			iter++
			old = m.state
			continue
		}

		new := old

        // now starving try lock
        if old & mutexStarving == 0 {
            new |= mutexLocked
        }

        // if locked or starving waiter add 1
		if old&(mutexLocked|mutexStarving) != 0 {
			new += 1 << mutexWaiterShift
		}

        if starving && old & mutexLocked != 0 {
            new |= mutexStarving
        }

		if awoke {
            if new&mutexWoken == 0 {
                throw("in consistent mutex state")
            }
            new &^= mutexWoken // xor
		}

		if atomic.CompareAndSwapInt32(&m.state, old, new) {
		   if old&(mutexLocked|mutexStarving) == 0 {
				break // locked the mutex with CAS
			}

            queueLifo := waitStartTime != 0
            if waitStartTime == 0 {
                waitStartTime = runtime_nanotime()
            }
            runtime_semacquireMutex(&m.sema, queueLifo, 1) // blocking 排队了
            starving = starving || runtime_nanotime() - waitStartTime > starvatinThreadhold // enter staving mode
            old = m.state
            if old&mutexStarving != 0 {
                if old&(mutexLocked|mutexWoken) != 0 || old>>mutexWaiterShift == 0 {
					throw("sync: inconsistent mutex state")
				}
                delta := int32(mutexLocked - 1<<mutexWaiterShift)
                if !starving || old>>mutexWaiterShift == 1 {
                    delta -= mutexStarving
                }
                atomic.AddInt32(&m.state, delta)
                break
            }
            awoke = true
            iter = 0
		} else {
			old = m.state
		}
	}

    if race.Enabled {
        race.Acquire(unsafe.Pointer(m))
    }
}

func (m *Mutex) Unlock() {
	if runtime.BoolRecordTrad {
		runtime.RecordTradOp(&m.Record.PreLoc)
	}

	if race.Enabled {
		_ = m.state
		race.Release(unsafe.Pointer(m))
	}

	// Fast path: drop lock bit. 如果新的state为0， no waiter，就可以直接过了
	new := atomic.AddInt32(&m.state, -mutexLocked)
	if new != 0 {
		m.unlockSlow(new)
	}
}

func (m *Mutex) unlockSlow(new int32) {
	if (new+mutexLocked)&mutexLocked == 0 {
		throw("sync: unlock of unlocked mutex")
	}
	if new&mutexStarving == 0 {
		old := new
		for {
			// If there are no waiters or a goroutine has already
			// been woken or grabbed the lock, no need to wake anyone.
			// In starvation mode ownership is directly handed off from unlocking
			// goroutine to the next waiter. We are not part of this chain,
			// since we did not observe mutexStarving when we unlocked the mutex above.
			// So get off the way.
			if old>>mutexWaiterShift == 0 || old&(mutexLocked|mutexWoken|mutexStarving) != 0 {
				return
			}
			// Grab the right to wake someone.
			new = (old - 1<<mutexWaiterShift) | mutexWoken
			if atomic.CompareAndSwapInt32(&m.state, old, new) {
				runtime_Semrelease(&m.sema, false, 1) // dequeue a G
				return
			}
			old = m.state
		}
	} else {
		// Starving mode: handoff mutex ownership to the next waiter, and yield
		// our time slice so that the next waiter can start to run immediately.
		// Note: mutexLocked is not set, the waiter will set it after wakeup.
		// But mutex is still considered locked if mutexStarving is set,
		// so new coming goroutines won't acquire it.
		runtime_Semrelease(&m.sema, true, 1)
	}
}

```

#### 正常模式 加锁

1. 尝试 CAS 直接加锁， 在锁状态的 Locked bit 加锁
2. 无法加锁，多次自选尝试
3. 多次失败，进入 sema 队列休眠

#### 正常模式 解锁

1. 尝试 cas 直接解锁，在锁状态的 Locked bit 解锁
2. 若发现有协程在 sema 中休眠，唤醒一个协程

### Mutex 饥饿

- 协程等待锁的时间超过 1ms，切换到饥饿模式
- 饥饿模式中 不自旋 新来的协程直接 sema 休眠
- 饥饿模式中，被唤醒的协程直接获取锁
- 没有协程在队列中等待，回归正常模式

## sync.RWMutex Read Write Lock 读多写少

只读其他不能修改
多协程可以共享读
只读不需要互斥

两个等待队列，读队列和写队列
读锁是共享锁
写锁是独占锁

```golang
type Person struct {
    mu sync.RWMutex
    salary int
    level int
}

func (p *Person) promote() {
    p.mu.Lock()
    defer p.mu.Unlock()

    p.salary += 1000
    fmt.Println(p.salary)
    p.level += 1
    fmt.Println(p.level)
}

func (p *Person) printPerson() {
    p.mu.RLock()
    defer p.mu.RUnlock()
    fmt.Println(s.salary)
}
```

```go
type RWMutex struct {
    w Mutex // held if there are pending writers 写锁
    writerSem uint32 // semaphore for writers to wait for completing readers
    readerSem uint32 // semaphore for readers to wait for completing writers
    readerCount int32 // number of pending readers, positive means reading goroutine, negative means writing goroutines
    readerWait int // number of departing readers
}

const rwmutexMaxReaders = 1 << 39
```

### 加写锁

1. 先加 mutex 写锁，若已加锁，阻塞等待
2. 将 readerCounter 变为负值，阻塞读锁获取
3. 计算需要等待多时读协程释放
4. 如果需要等待读协程释放，陷入 writerSem

### 解写锁

1. 将 readerCount 变为正值 加上 rwmutexMaxReaders 允许读锁的获取
2. 释放在 readerSem 中等待的读协程
3. 解锁 mutex

### 加读锁

1. 给 readerCount 无脑加一 atomic operation
2. 如果 readerCounter 是正 加锁成功
3. 如果 readerCount 是负数 什么被加了写锁 陷入 readerSem

### 解读锁

1. 给 readerCount - 1 atomic operation
2. 如果 readerCount 是正数 解锁成功
3. 如果 readerCount 是负数 有写锁在排队 如果自己是 readerWait 的最后一个 唤醒写协程

## WaitGroup

```go
wg := sync.WaitGroup()
wg.Add(3)
go func (w *sync.WaitGroup) {
    fmt.Println(1)
    w.Done()
}(*wg)
go func (w *sync.WaitGroup) {
    fmt.Println(2)
    w.Done()
}(*wg)
go func (w *sync.WaitGroup) {
    fmt.Println(3)
    w.Done()
}(*wg)
wg.Wait()

type WaitGroup struct {
    noCopy noCopy

    /*
        0 - counter
        1 - waiter counter
        2 - sema
    */
    state1 [3]uint32
}

// 如果等待的协程没了，直接返回
// 否则waiter+1 陷入sema
func (wg *WaitGroup) Wait() {
    // statep = counter __ waiter
    statep, semap := wg.state()
    if race.Enabled {
        _ = *statep
        race.Disable()
    }
    for {
        state := atomic.LoadUint64(statep)
        v := int32(state >> 32) // counter
        w := uint32(state)
        if v == 0 {
            // Counter is 0 no need to wait
            if race.Enabled {
                race.Enabled()
                race.Acquire(unSafe.Point(wg))
            }
            return
        }

        // Increment waiters count
        if atomic.CompareAndSwapUint64(statep, state, state + 1) {
            if race.Enabled && w == 0 {
                race.Write(unsafe.Pointer(semap))
            }
        }

        runtime_semacquire(semap) // enqueue and block
        if *statep != 0 panic("xxxx")
        if race.Enabled {
            race.Enabled()
            race.Acquire(unsafe.Pointer(wg))
        }
        return
    }
}

// 被等待的协程做完，counter-1
func (wg *WaitGroup) Done() {
    wg.Add(-1)
}

func (wg *WaitGroup) Add(delta int) {
    statep, semap := wg.state()
    // no race code
    state := atomic.AddUint64(statep, uint64(delta)<<32)
    v := int32(state >> 32)
    w := uint32(state)
    // no race code
    if v < 0 {
		panic("sync: negative WaitGroup counter")
	}
    if w != 0 && delta > 0 && v == int32(delta) {
		panic("sync: WaitGroup misuse: Add called concurrently with Wait")
	}
    if v > 0 || w == 0 {
		return
	}
    if *statep != state {
		panic("sync: WaitGroup misuse: Add called concurrently with Wait")
	}
    *statep = 0
    // v==0,
    for ; w != 0; w-- {
        // wake up coroutine
		runtime_Semrelease(&wg.sema, false)
	}
}
```

## Once 一段代码 方法 逻辑只执行一次

```go
func p () {
    fmt.Println(1)
}
once := sync.Once{}
go once.Do(p)
go once.Do(p)
go once.Do(p)
// only print once
```

思路 1
一个变量记录一下 从零变成 1 就不会执行了 CAS 但 cas 对协程不友好
思路 2

1. 抢一个 mutex 抢不到陷入 sema 休眠
2. 抢到的执行代码 更改执行 flag 释放锁
3. 其他协程唤醒后判断值是否修改 如若修改则返回

```go
type Once struct {
    done uint32
    m Mutex
}

func (o *Once) Do (f func()) {
    if atomic.LoadUint32(&o.done) == 0 {
        o.doSlow(f)
    }
}

func (o *Once) doSlow(f func()) {
    o.m.Lock()
    defer o.m.unlock()
    if o.done == 0 {
        defer atomic.StoreUint32(&o.done, 1)
        f()
    }
}

```

1. 先判断是否已经改值
2. 没改 尝试获取锁
3. 获取到锁的协程执行业务 改值 解锁
4. 冲突协程唤醒后直接返回

## 排查锁异常问题

### 锁拷贝

锁拷贝导致死锁,特别是当一个对象内部有锁时 特别容易发生

```go
m := sync.Mutex{}
m.Lock()
n:= m // lock copy !!!!
m.Unlock()
n.Lock() // nope, n alreay locked
```

- 解决方案 go vet

#### go vet

vet 还能检测可能的 bug 或可疑的构造

### data race 竞争检测

go build -race main.go 之后运行一下
