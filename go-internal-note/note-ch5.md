# goroutine

## thread problem

1. memory consuming
2. context switch
3. os overhood

## 本质

将一段程序的运行状态打包，可以调度，复用线程（栈切换）
在程序运行期间 协程本质是一个 g 结构体，

runtime/runtime2.go => type g

```golang
type g struct { // 协程在golang的表示
    /*
        stack describer the actual stack memory [stack.lo, stack.hi)
        协程栈
    */
    stack stack // 堆栈地址
    stackguard0 uintptr
    stackguard1 uintptr
    _panic *_panic // innermost panic - offset known to liblink
    _defer *_defer // innermost defer
    m *m // current m; offser known to arm liblink
    sched gobuf // 程序运行现场

    syscallsp uintptr
    syscallpc uintptr
    stktopsp uintptr

    param unsafe.Pointer
    atomicstatus uint32 // 协程状态
    stackLock uint32
    goid int64 // goroutine id
    schedlink guintptr
    waitsince int64 // approx time when g become blocked
    waitreason waitReason // if status == Gwaiting

    preempt bool //preemption signal. duplicates stackguard0
    preemptStop bool // transition to _Gpreempted on preemptin
    preemptShrink bool // shrink stack at synchronous safe point


}

type stack struct {
    lo uintptr
    hi uintptr
}

type heldLockInfo strcut {
    lockAddr uintptr
    rank lockRank
}

type gobuf struct {
    sp uintptr // 原始指针 stack pointer 栈指针， 指向当前线程栈用到什么地方（栈帧）
    pc uintptr // program counter 当前协程运行到哪一行代码
    g guintptr
    ctxt unsafe.Pointer
    ret uintptr
    lr uintptr
    bp uintptr
}

// 描述操作系统线程的结构体
type m struct {
    g0 *g // goroutine with scheduling stack， golang第一个启动的协程， 调度协程
    morebuf gobuf // gobuf arg to morestack
    divmod uint32 // div/mod denominator for arm

    //
    procid uint64
    gsignal *g // signal-handling g
    goSigStack gsignalStack // Go-allocated signal handling
    sigmask sigset // storage for saved singnal mask
    tls [tlsSlots]uintptr // thread-local storage
    mstartfn func()
    curg *g // current running goroutine
    caughtsig guintptr  // goroutine running during fatal signal
    p puintptr // attached p for executing go code
    nextp puintptr
    oldp puintptr // the p that was attached before executing
    id int64
    mallocing int32
    throwing int32
    preemptoff string
    locks int32
    dying int32
    profilehz int32
    spinning bool
    blocked bool
    newsigstack bool
    printlock int8
    incgo bool
    freeWait uint32
    fastrand [2]uint32
    needextram bool
    traceback uint8
    ncgocall uint64
    ncgo int32
    cgoCallersUse uint32
    cgoCallers *cgoCallers
    doesPark bool
    park note
    allink *m
    shedlink muintptr
    lockedg guintptr
    createstack [32]uintptr
    lockedExt uint32
    lockedInt uint32
    nextwaitm muintptr
    waitunlockf func(*g, unsafe.Pointer) bool
    waitlock unsafe.Pointer
    waittraceev byte
    waittraceskip int
    startingtrace bool
    syscalltick uint32
    freelink *m

    mFixup struct {
        lock mutex
        used uint32
        fn func(bool) bool
    }

    libcall libcall
    libcallpc uintptr
    libcallsp uintptr
    libcallg guintptr
    syscall libcall

    vdsoSP uintptr
    vdsoPC uintptr

    // ....
    signalPending uint32
    dlogPerM

    mOS // 底层操作系统对于线程的描述 runtime/os_linux, runtime/os_windows,etc...

    locksHeldLen int
    locksHeld [10]heldLockInfo

}
```

## 单线程循环

In go 0.x
单线程循环 schedule()[g0 stack] -> execute()[g0 stack] -> gogo()[g0 stack，汇编实现，平台相关] -> 业务方法 -> goexit() -> back to schedule()

in execute, take g from runnable queue

1. schedule() runtime/proc.go

```go
func schedule() {
    _g_ := getg() //获取当前运行协程

    if _g_.m.locks != 0 {
        throw("schedule: holding locks")
    }

    if _g_.m.lockedg != 0 {
        stoplockedm()
        execute(_g_.m.lockedg.ptr(), false) // never returns
    }

    // we should not schedule away from a g that is executing a cgo call
    // since the cgo call is using the m's g0 stack
    if _g_.m.incgo {
        throw("schedule: in cgo")
    }

top:
    pp := _g_.m.p.ptr()
    pp.preempt = false

    if sched.gcwaiting != 0 {
        gcstopm()
        goto top
    }

    if pp.runSafePointFn != 0 {
        runSafePointFn()
    }

    if _g_.m.spinning && (pp.runnext != 0 || pp.runqhead != pp.runqtail) {
        throw("schedule: spinning with local work")
    }

    checkTimers(pp, 0)

    var gp *g // 即将要运行的协程
    var inheritTime bool

    tryWakeP := false
    if trace.enabled || trace.shutdown {
        gp = traceReader()
        if gp != nil {
            casgstatus(gp, _Gwaiting, _Grunnable)
            traceGoUnpark(gp, 0)
            tryWakeP = true
        }
    }

    if gp == nil && gcBlackenEnabled != 0 {
        gp = gcController.findRunnableGCWorker(_g_.m.p.ptr())
        tryWakeP = tryWakeP || gp != nil
    }

    if gp == nil {
		// Check the global runnable queue once in a while to ensure fairness.
		// Otherwise two goroutines can completely occupy the local runqueue
		// by constantly respawning each other.
        // 每执行61次线程循环， 从global rq 拿g执行一下，避免global rq饥饿
		if _g_.m.p.ptr().schedtick%61 == 0 && sched.runqsize > 0 {
			lock(&sched.lock)
			gp = globrunqget(_g_.m.p.ptr(), 1)
			unlock(&sched.lock)
		}
	}

    if gp == nil {
        // 从当前运行的协程的m对应的p里面获取G
		gp, inheritTime = runqget(_g_.m.p.ptr())
		// We can see gp != nil here even if the M is spinning,
		// if checkTimers added a local goroutine via goready.
	}

    if gp == nil {
		gp, inheritTime = findrunnable() // blocks until work is available， stealwork也在这里发生
	}

 	// This thread is going to run a goroutine and is not spinning anymore,
	// so if it was marked as spinning we need to reset it now and potentially
	// start a new spinning M.
	if _g_.m.spinning {
		resetspinning()
	}

    if sched.disable.user && !schedEnabled(gp) {
		// Scheduling of this goroutine is disabled. Put it on
		// the list of pending runnable goroutines for when we
		// re-enable user scheduling and look again.
		lock(&sched.lock)
		if schedEnabled(gp) {
			// Something re-enabled scheduling while we
			// were acquiring the lock.
			unlock(&sched.lock)
		} else {
			sched.disable.runnable.pushBack(gp)
			sched.disable.n++
			unlock(&sched.lock)
			goto top
		}
	}

 	// If about to schedule a not-normal goroutine (a GCworker or tracereader),
	// wake a P if there is one.
	if tryWakeP {
		wakep()
	}

    if gp.lockedm != 0 {
		// Hands off own p to the locked m,
		// then blocks waiting for a new p.
		startlockedm(gp)
		goto top
	}


	execute(gp, inheritTime)
}
```

gogo() asm_amd64 通过修改栈指针、基指针、程序计数器等寄存器的值，将执行流程从一个 goroutine 切换到另一个 goroutine。

```s
TEXT runtime.gogo(SB), NOSPLIT, $0-8
    MOVQ buf+0(FP), BX
    MOVQ gobuf_g(BX), DX // 知道了stack大小，取出gobuf
    MOVQ 0(DX), CX
    JMP gogo<>(SB)

TEXT runtime.gogo(SB), NOSPLIT, $0
    get_tls(CX)
    MOVQ DX, g(CX)
    MOVQ DX, R14
    MOVQ gobuf_sp(BX), SP // 往协程栈插入了goexit
    MOVQ gobuf_ret(BX), AX
    MOVQ gobuf_ctxt(BX), DX
    MOVQ gobuf_bp(BX), BP
    MOVQ $0, gobuf_sp(BX) //清零gobuf_sp
    MOVQ $0, gobuf_ret(BX)
    MOVQ $0, gobuf_ctxt(BX)
    MOVQ $0, gobuf_bp(BX)
    MOVQ gobuf_pc(BX), BX // 跳转线程的程序计数器
    JMP BX // 跳转到位置进行执行


TEXT runtime.goexit(SB),NOSPLIT|TOPFRAME,$0-0
    BYTE $0x90
    CALL runtime.goexit1(SB) // does not return, in golang
    BYTE $0x90
```

```golang
// Finishes execution of the current goroutine.
func goexit1() {
	if raceenabled {
		racegoend()
	}
	if trace.enabled {
		traceGoEnd()
	}
	mcall(goexit0) // assemply,切换栈， g0栈调用goexit0
}

// goexit continuation on g0.
func goexit0(gp *g) {
	_g_ := getg()

	casgstatus(gp, _Grunning, _Gdead)
	if isSystemGoroutine(gp, false) {
		atomic.Xadd(&sched.ngsys, -1)
	}
	gp.m = nil
	locked := gp.lockedm != 0
	gp.lockedm = 0
	_g_.m.lockedg = 0
	gp.preemptStop = false
	gp.paniconfault = false
	gp._defer = nil // should be true already but just in case.
	gp._panic = nil // non-nil for Goexit during panic. points at stack-allocated data.
	gp.writebuf = nil
	gp.waitreason = 0
	gp.param = nil
	gp.labels = nil
	gp.timer = nil

	if gcBlackenEnabled != 0 && gp.gcAssistBytes > 0 {
		// Flush assist credit to the global pool. This gives
		// better information to pacing if the application is
		// rapidly creating an exiting goroutines.
		assistWorkPerByte := float64frombits(atomic.Load64(&gcController.assistWorkPerByte))
		scanCredit := int64(assistWorkPerByte * float64(gp.gcAssistBytes))
		atomic.Xaddint64(&gcController.bgScanCredit, scanCredit)
		gp.gcAssistBytes = 0
	}

	dropg()

	if GOARCH == "wasm" { // no threads yet on wasm
		gfput(_g_.m.p.ptr(), gp)
		schedule() // never returns
	}

	if _g_.m.lockedInt != 0 {
		print("invalid m->lockedInt = ", _g_.m.lockedInt, "\n")
		throw("internal lockOSThread error")
	}
	gfput(_g_.m.p.ptr(), gp)
	if locked {
		// The goroutine may have locked this thread because
		// it put it in an unusual kernel state. Kill it
		// rather than returning it to the thread pool.

		// Return to mstart, which will release the P and exit
		// the thread.
		if GOOS != "plan9" { // See golang.org/issue/22227.
			gogo(&_g_.m.g0.sched)
		} else {
			// Clear lockedExt on plan9 since we may end up re-using
			// this thread.
			_g_.m.lockedExt = 0
		}
	}
	schedule()
}
```

## 多线程循环 GO 1.0

单线程循环 _ n + runnable queue _ 1
runnable queue 有并发问题, need lock

## 本地队列 （P - processor）

每个 M 每次从全局拿 n 个 G 放入本地队列

```go
type p struct {
	id          int32
	status      uint32 // one of pidle/prunning/...
	link        puintptr
	schedtick   uint32     // incremented on every scheduler call
	syscalltick uint32     // incremented on every system call
	sysmontick  sysmontick // last tick observed by sysmon
	m           muintptr   // back-link to associated m (nil if idle)
	mcache      *mcache
	pcache      pageCache
	raceprocctx uintptr

	deferpool    [5][]*_defer // pool of available defer structs of different sizes (see panic.go)
	deferpoolbuf [5][32]*_defer

	// Cache of goroutine ids, amortizes accesses to runtime·sched.goidgen.
	goidcache    uint64
	goidcacheend uint64

	// Queue of runnable goroutines. Accessed without lock.
	runqhead uint32
	runqtail uint32
	runq     [256]guintptr
	// runnext, if non-nil, is a runnable G that was ready'd by
	// the current G and should be run next instead of what's in
	// runq if there's time remaining in the running G's time
	// slice. It will inherit the time left in the current time
	// slice. If a set of goroutines is locked in a
	// communicate-and-wait pattern, this schedules that set as a
	// unit and eliminates the (potentially large) scheduling
	// latency that otherwise arises from adding the ready'd
	// goroutines to the end of the run queue.
	runnext guintptr

	// Available G's (status == Gdead)
	gFree struct {
		gList
		n int32
	}

	sudogcache []*sudog
	sudogbuf   [128]*sudog

	// Cache of mspan objects from the heap.
	mspancache struct {
		// We need an explicit length here because this field is used
		// in allocation codepaths where write barriers are not allowed,
		// and eliminating the write barrier/keeping it eliminated from
		// slice updates is tricky, moreso than just managing the length
		// ourselves.
		len int
		buf [128]*mspan
	}

	tracebuf traceBufPtr

	// traceSweep indicates the sweep events should be traced.
	// This is used to defer the sweep start event until a span
	// has actually been swept.
	traceSweep bool
	// traceSwept and traceReclaimed track the number of bytes
	// swept and reclaimed by sweeping in the current sweep loop.
	traceSwept, traceReclaimed uintptr

	palloc persistentAlloc // per-P to avoid mutex

	_ uint32 // Alignment for atomic fields below

	// The when field of the first entry on the timer heap.
	// This is updated using atomic functions.
	// This is 0 if the timer heap is empty.
	timer0When uint64

	// The earliest known nextwhen field of a timer with
	// timerModifiedEarlier status. Because the timer may have been
	// modified again, there need not be any timer with this value.
	// This is updated using atomic functions.
	// This is 0 if the value is unknown.
	timerModifiedEarliest uint64

	// Per-P GC state
	gcAssistTime         int64 // Nanoseconds in assistAlloc
	gcFractionalMarkTime int64 // Nanoseconds in fractional mark worker (atomic)

	// gcMarkWorkerMode is the mode for the next mark worker to run in.
	// That is, this is used to communicate with the worker goroutine
	// selected for immediate execution by
	// gcController.findRunnableGCWorker. When scheduling other goroutines,
	// this field must be set to gcMarkWorkerNotWorker.
	gcMarkWorkerMode gcMarkWorkerMode
	// gcMarkWorkerStartTime is the nanotime() at which the most recent
	// mark worker started.
	gcMarkWorkerStartTime int64

	// gcw is this P's GC work buffer cache. The work buffer is
	// filled by write barriers, drained by mutator assists, and
	// disposed on certain GC state transitions.
	gcw gcWork

	// wbBuf is this P's GC write barrier buffer.
	//
	// TODO: Consider caching this in the running G.
	wbBuf wbBuf

	runSafePointFn uint32 // if 1, run sched.safePointFn at next safe point

	// statsSeq is a counter indicating whether this P is currently
	// writing any stats. Its value is even when not, odd when it is.
	statsSeq uint32

	// Lock for timers. We normally access the timers while running
	// on this P, but the scheduler can also do it from a different P.
	timersLock mutex

	// Actions to take at some time. This is used to implement the
	// standard library's time package.
	// Must hold timersLock to access.
	timers []*timer

	// Number of timers in P's heap.
	// Modified using atomic instructions.
	numTimers uint32

	// Number of timerModifiedEarlier timers on P's heap.
	// This should only be modified while holding timersLock,
	// or while the timer status is in a transient state
	// such as timerModifying.
	adjustTimers uint32

	// Number of timerDeleted timers in P's heap.
	// Modified using atomic instructions.
	deletedTimers uint32

	// Race context used while executing timer functions.
	timerRaceCtx uintptr

	// preempt is set to indicate that this P should be enter the
	// scheduler ASAP (regardless of what G is running on it).
	preempt bool

	pad cpu.CacheLinePad
}
```

每一个 P 负责一个 M，构建一个本地 G 队列， M 每次从 P 中获取一个 G 执行

本质上就是 M 与 G 的中介，负责给 M 送料，目的就是减少并发冲突

## stealwork

在本地 P 或者全局的队列都找不到 G，就去别的 P 中偷，增加线程利用率

## 新建协程 runtime/proc.go newproc method

随机寻找一个 P， 将新协程放入 P 的 runnext（插队）
如果 P 的本地队列满了，放入全局队列

## 协程饥饿

```golang
        // 每执行61次线程循环， 从global rq 拿g执行一下，避免global rq饥饿
		if _g_.m.p.ptr().schedtick%61 == 0 && sched.runqsize > 0 {
			lock(&sched.lock)
			gp = globrunqget(_g_.m.p.ptr(), 1)
			unlock(&sched.lock)
		}
```

## 抢占调度

协程长时间执行，以下会发生抢占调度

### 主动挂起 runtime.gopark

time.Sleep 底层调用的就是 gopark

### 系统调用完成时

业务方法 -> entersyscall() -> system call -> exitsyscall(put g into global runnable queue) -> schedule

### runtime.morestack() 汇编 基于协作的抢占式调度

每次函数跳转编译器会插入
本意是检查协程栈是否有足够的空间
当 runtime 发现协程运行超过 10 毫秒时 会将 g.stackguard0 => 0xfffffade(抢占标志)
汇编会调用 runtime.newstack

### 基于信号的抢占调度

线程信号 SIGPIPE SIGURG SIGHUP
线程可以注册对应信号的处理函数

可以用 SIGURG，当 GC 工作时，向目标线程发送信号
doSigPreempt -> asyncPreempt(汇编) -> asyncPreempt2

## 协程太多？

以下代码跑一会就报错，系统资源耗尽

```go
for i:= 0; i < math.MaxInt32; i++ {
    go func(i: number) {
        time.Sleep(time.Second)
    }(i)
}
```

#### solution

##### 优化代码

##### channel 缓冲区

```golang
c := make(chan struct{}, 3000)
for i:=0; i< math.MaxInt32; i++ {
    c <- struct{}{} // 满了3000会卡在这
    go func(i: number, ch chan struct{}) {
        time.Sleep(time.Second)
        <- ch
    }(i, c)
}
```

##### 协程池 tunny

不推荐， go 的 runtime 已经是个池了 傻逼
go 的初衷，即用即毁
