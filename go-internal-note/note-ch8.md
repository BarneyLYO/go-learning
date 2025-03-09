# IO

IO 模型指的是同时操作 SOCKET 的方案

In golang
在底层使用操作系统多路复用 IO
在协程使用阻塞模型
阻塞协程，休眠协程

## EPOLL 抽象层

epoll 抽象层是为了统一各个操作系统对多路复用器的实现
由 network poller 抽象

## 多路复用器

1. 新建多路复用 epoll_create() `SYS_EPOLL_CREATE`
   runtime/netpoll_epoll.go `func epollcreate(size int32) int32` => sys_linux_amd64.s

```s
TEXT runtime-epollcreate(SB),NOSPLIT,$0
    MOVL size+0(FP),DI
    MOVL $SYS_epoll_create, AX
    SYSCALL
    MOVL AX, ret+8(FP)
    RET
```

2. 往多路复用器里插入需要监听的事件 epoll_ctl()
3. 查询发送了什么事件 epoll_wait()

## Go Networker Poller 多路复用器抽象

epoll_create -> netpollinit()

1. 新建 epoll
2. 新建一个 pipe 管道用于中断 Epoll
3. 将管道有数据达的事件注册到 epoll 中

epoll_ctl -> netpollopen()
epoll_wait -> netpoll()

```
#include <sys/epoll.h>
int epoll_create(int size);
int epoll_ctl(int epfd, int op, int fd, struct epoll_event _event);
int epoll_wait(int epfd, struct epoll_event _ events, int maxevents, int timeout);

// Go 对上面三个调用的封装
func netpollinit()
func netpollopen(fd uintptr, pd \*pollDesc) int32
func netpoll(block bool) gList
```

```go
// runtime netpoll.go
// 对于网络层的一个socket的描述， 记录了那些协程在等读，在等写
type pollDesc struct {
	_     sys.NotInHeap
	link  *pollDesc      // in pollcache, protected by pollcache.lock
	fd    uintptr        // constant for pollDesc usage lifetime
	fdseq atomic.Uintptr // protects against stale pollDesc

	// atomicInfo holds bits from closing, rd, and wd,
	// which are only ever written while holding the lock,
	// summarized for use by netpollcheckerr,
	// which cannot acquire the lock.
	// After writing these fields under lock in a way that
	// might change the summary, code must call publishInfo
	// before releasing the lock.
	// Code that changes fields and then calls netpollunblock
	// (while still holding the lock) must call publishInfo
	// before calling netpollunblock, because publishInfo is what
	// stops netpollblock from blocking anew
	// (by changing the result of netpollcheckerr).
	// atomicInfo also holds the eventErr bit,
	// recording whether a poll event on the fd got an error;
	// atomicInfo is the only source of truth for that bit.
	atomicInfo atomic.Uint32 // atomic pollInfo

	// rg, wg are accessed atomically and hold g pointers.
	// (Using atomic.Uintptr here is similar to using guintptr elsewhere.)
	rg atomic.Uintptr // pdReady 1, pdWait 2, G waiting for read(addr) or pdNil
	wg atomic.Uintptr // pdReady 1, pdWait 2, G waiting for write(addr) or pdNil

	lock    mutex // protects the following fields
	closing bool
	user    uint32    // user settable cookie
	rseq    uintptr   // protects from stale read timers
	rt      timer     // read deadline timer (set if rt.f != nil)
	rd      int64     // read deadline (a nanotime in the future, -1 when expired)
	wseq    uintptr   // protects from stale write timers
	wt      timer     // write deadline timer
	wd      int64     // write deadline (a nanotime in the future, -1 when expired)
	self    *pollDesc // storage for indirect interface. See (*pollDesc).makeArg.
}

// runtime/netpoll_epoll.go
var (
	epfd int32 = -1 // epoll descriptor

	netpollBreakRd, netpollBreakWr uintptr // for netpollBreak

	netpollWakeSig atomic.Uint32 // used to avoid duplicate calls of netpollBreak
)

func netpollinit() {
    var errno, uintptr
    epfd, errno = syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if errno != 0 {
		println("runtime: epollcreate failed with", errno)
		throw("runtime: netpollinit failed")
	}
    r, w, errpipe := nonblockingPipe() // create linux pipe, for close epoll
    if errpipe != 0 {
		println("runtime: pipe failed with", -errpipe)
		throw("runtime: pipe failed")
	}
    ev := syscall.EpollEvent{
		Events: syscall.EPOLLIN,
	}
    *(**uintptr)(unsafe.Pointer(&ev.Data)) = &netpollBreakRd
    errno = syscall.EpollCtl(epfd, syscall.EPOLL_CTL_ADD, r, &ev) // add event to epoll
    if errno != 0 {
		println("runtime: epollctl failed with", errno)
		throw("runtime: epollctl failed")
	}
	netpollBreakRd = uintptr(r)
	netpollBreakWr = uintptr(w)
}

// not open a epoll, is for insert a event into epoll
func netpollopen(fd uintptr, pd *pollDesc) uintptr {
	var ev syscall.EpollEvent
    // add syscall.EPOLLIN | syscall.EPOLLOUT | syscall.EPOLLRDHUP | syscall.EPOLLET
	ev.Events = syscall.EPOLLIN | syscall.EPOLLOUT | syscall.EPOLLRDHUP | syscall.EPOLLET
	tp := taggedPointerPack(unsafe.Pointer(pd), pd.fdseq.Load())
	*(*taggedPointer)(unsafe.Pointer(&ev.Data)) = tp
	return syscall.EpollCtl(epfd, syscall.EPOLL_CTL_ADD, int32(fd), &ev)
}

// netpoll checks for ready network connections.
// Returns list of goroutines that become runnable.
// delay < 0: blocks indefinitely
// delay == 0: does not block, just polls
// delay > 0: block for up to that many nanoseconds
func netpoll(delay int64) (gList, int32) {
	if epfd == -1 {
		return gList{}, 0
	}
	var waitms int32
	if delay < 0 {
		waitms = -1
	} else if delay == 0 {
		waitms = 0
	} else if delay < 1e6 {
		waitms = 1
	} else if delay < 1e15 {
		waitms = int32(delay / 1e6)
	} else {
		// An arbitrary cap on how long to wait for a timer.
		// 1e9 ms == ~11.5 days.
		waitms = 1e9
	}
	var events [128]syscall.EpollEvent
retry:
	n, errno := syscall.EpollWait(epfd, events[:], int32(len(events)), waitms)
	if errno != 0 {
		if errno != _EINTR {
			println("runtime: epollwait on fd", epfd, "failed with", errno)
			throw("runtime: netpoll failed")
		}
		// If a timed sleep was interrupted, just return to
		// recalculate how long we should sleep now.
		if waitms > 0 {
			return gList{}, 0
		}
		goto retry
	}
	var toRun gList
	delta := int32(0)
	for i := int32(0); i < n; i++ {
		ev := events[i]
		if ev.Events == 0 {
			continue
		}

		if *(**uintptr)(unsafe.Pointer(&ev.Data)) == &netpollBreakRd {
			if ev.Events != syscall.EPOLLIN {
				println("runtime: netpoll: break fd ready for", ev.Events)
				throw("runtime: netpoll: break fd ready for something unexpected")
			}
			if delay != 0 {
				// netpollBreak could be picked up by a
				// nonblocking poll. Only read the byte
				// if blocking.
				var tmp [16]byte
				read(int32(netpollBreakRd), noescape(unsafe.Pointer(&tmp[0])), int32(len(tmp)))
				netpollWakeSig.Store(0)
			}
			continue
		}

		var mode int32
		if ev.Events&(syscall.EPOLLIN|syscall.EPOLLRDHUP|syscall.EPOLLHUP|syscall.EPOLLERR) != 0 {
			mode += 'r'
		}
		if ev.Events&(syscall.EPOLLOUT|syscall.EPOLLHUP|syscall.EPOLLERR) != 0 {
			mode += 'w'
		}
		if mode != 0 {
			tp := *(*taggedPointer)(unsafe.Pointer(&ev.Data))
			pd := (*pollDesc)(tp.pointer())
			tag := tp.tag()
			if pd.fdseq.Load() == tag {
				pd.setEventErr(ev.Events == syscall.EPOLLERR, tag)
				delta += netpollready(&toRun, pd, mode)
			}
		}
	}
	return toRun, delta
}

func EpollWait(epfd int32, events []EpollEvent, maxev, waitms int32) (n int32, errno uintptr) {
	var ev unsafe.Pointer
	if len(events) > 0 {
		ev = unsafe.Pointer(&events[0])
	} else {
		ev = unsafe.Pointer(&_zero)
	}
	r1, _, e := Syscall6(SYS_EPOLL_PWAIT, uintptr(epfd), uintptr(ev), uintptr(maxev), uintptr(waitms), 0, 0)
	return int32(r1), e
}

func netpollready(toRun *gList, pd *pollDesc, mode int32) int32 {
	delta := int32(0)
	var rg, wg *g
	if mode == 'r' || mode == 'r'+'w' {
		rg = netpollunblock(pd, 'r', true, &delta)
	}
	if mode == 'w' || mode == 'r'+'w' {
		wg = netpollunblock(pd, 'w', true, &delta)
	}
	if rg != nil {
		toRun.push(rg)
	}
	if wg != nil {
		toRun.push(wg)
	}
	return delta
}

func netpollunblock(pd *pollDesc, mode int32, ioready bool, delta *int32) *g {
	gpp := &pd.rg // read g
	if mode == 'w' {
		gpp = &pd.wg // write g
	}

	for {
		old := gpp.Load()
		if old == pdReady {
			return nil
		}
		if old == pdNil && !ioready {
			// Only set pdReady for ioready. runtime_pollWait
			// will check for timeout/cancel before waiting.
			return nil
		}
		new := pdNil
		if ioready {
			new = pdReady
		}
		if gpp.CompareAndSwap(old, new) {
			if old == pdWait {
				old = pdNil
			} else if old != pdNil {
				*delta -= 1
			}
			return (*g)(unsafe.Pointer(old))
		}
	}
}
```

go 将多路复用器的操作进行了抽象和匹配

1. 将新建多路复用器抽象为 netpollinit
2. 将插入监听事件抽象为了 netpollopen
3. 将查询事件抽象为了 netpoll 返回等待事件的协程列表

## Network Poller 初始化

```go
type pollCache struct {
	lock  mutex
	first *pollDesc // list pollDesc is a list first.link, so socket list
	// PollDesc objects must be type-stable,
	// because we can get ready notification from epoll/kqueue
	// after the descriptor is closed/reused.
	// Stale notifications are detected using seq variable,
	// seq is incremented when deadlines are changed or descriptor is reused.
}

var (
	netpollInitLock mutex
	netpollInited   atomic.Uint32

	pollcache      pollCache
	netpollWaiters atomic.Uint32
)

```

poll_runtime_pollServerInit() 只执行一次 用于打开底层 epoll

```go
func poll_runtime_pollServerInit() {
	netpollGenericInit()
}

func netpollGenericInit() {
	if netpollInited.Load() == 0 { // only one network poll will be init
		lockInit(&netpollInitLock, lockRankNetpollInit)
		lock(&netpollInitLock)
		if netpollInited.Load() == 0 {
			netpollinit()
			netpollInited.Store(1)
		}
		unlock(&netpollInitLock)
	}
}

```

## Network Poller 新增加监听

socket poll_runtime_pollOpen
pollcache 链表里面分配一个 pollDesc
初始化 pollDesc rg = 0 wg = 0 fd = socketFd
调用 netpollopen

```go
// go:linkname a b, 说明a和b是连接起来的
//go:linkname poll_runtime_pollOpen internal/poll.runtime_pollOpen
func poll_runtime_pollOpen(fd uintptr) (*pollDesc, int) {
	pd := pollcache.alloc()
	lock(&pd.lock)
	wg := pd.wg.Load()
	if wg != pdNil && wg != pdReady {
		throw("runtime: blocked write on free polldesc")
	}
	rg := pd.rg.Load()
	if rg != pdNil && rg != pdReady {
		throw("runtime: blocked read on free polldesc")
	}
	pd.fd = fd // socket id
	if pd.fdseq.Load() == 0 {
		// The value 0 is special in setEventErr, so don't use it.
		pd.fdseq.Store(1)
	}
	pd.closing = false
	pd.setEventErr(false, 0)
	pd.rseq++
	pd.rg.Store(pdNil)
	pd.rd = 0
	pd.wseq++
	pd.wg.Store(pdNil)
	pd.wd = 0
	pd.self = pd
	pd.publishInfo()
	unlock(&pd.lock)

	errno := netpollopen(fd, pd) //
	if errno != 0 {
		pollcache.free(pd)
		return nil, int(errno)
	}
	return pd, 0
}


func (c *pollCache) alloc() *pollDesc {
	lock(&c.lock)
	if c.first == nil {
		const pdSize = unsafe.Sizeof(pollDesc{})
		n := pollBlockSize / pdSize
		if n == 0 {
			n = 1
		}
		// Must be in non-GC memory because can be referenced
		// only from epoll/kqueue internals.
		mem := persistentalloc(n*pdSize, 0, &memstats.other_sys)
		for i := uintptr(0); i < n; i++ {
			pd := (*pollDesc)(add(mem, i*pdSize))
			pd.link = c.first
			c.first = pd
		}
	}
	pd := c.first
	c.first = pd.link
	lockInit(&pd.lock, lockRankPollDesc)
	unlock(&c.lock)
	return pd
}

```

## network poller 收发数据

1. G 需要收发数据 Socket 已经可读写
   - runtime 循环调用 netpoll G0 协程调用 比如 gcStart(因为会循环，作为钩子)
   - 发现 socket 可以读写 给对应的 rg 或者 wg 设置为 pdReady 1
   - 协程调用 poll_runtime_pollWait()
   - 判断 wg 或者 rg 是否为 pgReady

```go
    // poll_runtime_pollWait, which is internal/poll.runtime_pollWait,
// waits for a descriptor to be ready for reading or writing,
// according to mode, which is 'r' or 'w'.
// This returns an error code; the codes are defined above.
//
//go:linkname poll_runtime_pollWait internal/poll.runtime_pollWait
func poll_runtime_pollWait(pd *pollDesc, mode int) int {
	errcode := netpollcheckerr(pd, int32(mode))
	if errcode != pollNoError {
		return errcode
	}
	// As for now only Solaris, illumos, AIX and wasip1 use level-triggered IO.
	if GOOS == "solaris" || GOOS == "illumos" || GOOS == "aix" || GOOS == "wasip1" {
		netpollarm(pd, mode)
	}
	for !netpollblock(pd, int32(mode), false) {
		errcode = netpollcheckerr(pd, int32(mode))
		if errcode != pollNoError {
			return errcode
		}
		// Can happen if timeout has fired and unblocked us,
		// but before we had a chance to run, timeout has been reset.
		// Pretend it has not happened and retry.
	}
	return pollNoError
}

func netpollblock(pd *pollDesc, mode int32, waitio bool) bool {
	gpp := &pd.rg
	if mode == 'w' {
		gpp = &pd.wg
	}

	// set the gpp semaphore to pdWait
	for {
		// Consume notification if already ready.
		if gpp.CompareAndSwap(pdReady, pdNil) {
			return true
		}
		if gpp.CompareAndSwap(pdNil, pdWait) {
			break
		}

		// Double check that this isn't corrupt; otherwise we'd loop
		// forever.
		if v := gpp.Load(); v != pdReady && v != pdNil {
			throw("runtime: double wait")
		}
	}

	// need to recheck error states after setting gpp to pdWait
	// this is necessary because runtime_pollUnblock/runtime_pollSetDeadline/deadlineimpl
	// do the opposite: store to closing/rd/wd, publishInfo, load of rg/wg
	if waitio || netpollcheckerr(pd, mode) == pollNoError {
		gopark(netpollblockcommit, unsafe.Pointer(gpp), waitReasonIOWait, traceBlockNet, 5)
	}
	// be careful to not lose concurrent pdReady notification
	old := gpp.Swap(pdNil)
	if old > pdWait {
		throw("runtime: corrupted polldesc")
	}
	return old == pdReady
}

```

2. G 需要收发数据 Socket 暂时无法读写
   runtime call netpoll() [g0 Goroutine]
   Gorotine 调用 poll_runtime_pollWait()
   发现对应的 rg 或者 wg 为 0
   给对应的 rg 或 wg 置为协程地址
   休眠等待
   runtime 循环调用 netpoll 方法 g0

## net 包

### net.Listen()

1. 新建 socket 执行 bind 操作
2. 新建一个 NetFD net 包对 socket 的详情描述
3. 返回一个 TCPListener 对象
4. 将 TCPListener 的 fd 信息加入监听
5. TCPListener 对象本质是一个 listen 状态的 socket

```golang
func main() {
	ln, err := net.Listen("tcp", ":8888")
	if err != nil {
		panic(err)
	}
}

// net/dial.go
func Listen(network, address string) (Listener, error) {
	var lc ListenConfig
	return lc.listen(context.Background(), network, address)
}

func (lc *ListenConfig) Listen(ctx context.Context, network, address string) (Listener, error) {
	addrs, err := DefaultResolver.resolveAddrList(ctx, "listen", network, address, nil)
	if err != nil {
		return nil, &OpError{Op: "listen", Net: network, Source: nil, Addr: nil, Err: err}
	}
	sl := &sysListener{
		ListenConfig: *lc,
		network:      network,
		address:      address,
	}
	var l Listener
	la := addrs.first(isIPv4)
	switch la := la.(type) {
	case *TCPAddr:
		if sl.MultipathTCP() {
			l, err = sl.listenMPTCP(ctx, la)
		} else {
			l, err = sl.listenTCP(ctx, la)
		}
	case *UnixAddr:
		l, err = sl.listenUnix(ctx, la)
	default:
		return nil, &OpError{Op: "listen", Net: sl.network, Source: nil, Addr: la, Err: &AddrError{Err: "unexpected address type", Addr: address}}
	}
	if err != nil {
		return nil, &OpError{Op: "listen", Net: sl.network, Source: nil, Addr: la, Err: err} // l is non-nil interface containing nil pointer
	}
	return l, nil
}

func (sl *sysListener) listenTCP(ctx context.Context, laddr *TCPAddr) (*TCPListener, error) {
	return sl.listenTCPProto(ctx, laddr, 0)
}

func (sl *sysListener) listenTCPProto(ctx context.Context, laddr *TCPAddr, proto int) (*TCPListener, error) {
	var ctrlCtxFn func(ctx context.Context, network, address string, c syscall.RawConn) error
	if sl.ListenConfig.Control != nil {
		ctrlCtxFn = func(ctx context.Context, network, address string, c syscall.RawConn) error {
			return sl.ListenConfig.Control(network, address, c)
		}
	}
	fd, err := internetSocket(ctx, sl.network, laddr, nil, syscall.SOCK_STREAM, proto, "listen", ctrlCtxFn)
	if err != nil {
		return nil, err
	}
	return &TCPListener{fd: fd, lc: sl.ListenConfig}, nil
}

func internetSocket(ctx context.Context, net string, laddr, raddr sockaddr, sotype, proto int, mode string, ctrlCtxFn func(context.Context, string, string, syscall.RawConn) error) (fd *netFD, err error) {
	switch runtime.GOOS {
	case "aix", "windows", "openbsd", "js", "wasip1":
		if mode == "dial" && raddr.isWildcard() {
			raddr = raddr.toLocal(net)
		}
	}
	family, ipv6only := favoriteAddrFamily(net, laddr, raddr, mode)
	return socket(ctx, net, family, sotype, proto, ipv6only, laddr, raddr, ctrlCtxFn)
}

// socket returns a network file descriptor that is ready for
// asynchronous I/O using the network poller.
func socket(ctx context.Context, net string, family, sotype, proto int, ipv6only bool, laddr, raddr sockaddr, ctrlCtxFn func(context.Context, string, string, syscall.RawConn) error) (fd *netFD, err error) {
	s, err := sysSocket(family, sotype, proto)
	if err != nil {
		return nil, err
	}
	if err = setDefaultSockopts(s, family, sotype, ipv6only); err != nil {
		poll.CloseFunc(s)
		return nil, err
	}
	if fd, err = newFD(s, family, sotype, net); err != nil {
		poll.CloseFunc(s)
		return nil, err
	}

	// This function makes a network file descriptor for the
	// following applications:
	//
	// - An endpoint holder that opens a passive stream
	//   connection, known as a stream listener
	//
	// - An endpoint holder that opens a destination-unspecific
	//   datagram connection, known as a datagram listener
	//
	// - An endpoint holder that opens an active stream or a
	//   destination-specific datagram connection, known as a
	//   dialer
	//
	// - An endpoint holder that opens the other connection, such
	//   as talking to the protocol stack inside the kernel
	//
	// For stream and datagram listeners, they will only require
	// named sockets, so we can assume that it's just a request
	// from stream or datagram listeners when laddr is not nil but
	// raddr is nil. Otherwise we assume it's just for dialers or
	// the other connection holders.

	if laddr != nil && raddr == nil {
		switch sotype {
		case syscall.SOCK_STREAM, syscall.SOCK_SEQPACKET:
			if err := fd.listenStream(ctx, laddr, listenerBacklog(), ctrlCtxFn); err != nil {
				fd.Close()
				return nil, err
			}
			return fd, nil
		case syscall.SOCK_DGRAM:
			if err := fd.listenDatagram(ctx, laddr, ctrlCtxFn); err != nil {
				fd.Close()
				return nil, err
			}
			return fd, nil
		}
	}
	if err := fd.dial(ctx, laddr, raddr, ctrlCtxFn); err != nil {
		fd.Close()
		return nil, err
	}
	return fd, nil
}

func (fd *netFD) listenStream(ctx context.Context, laddr sockaddr, backlog int, ctrlCtxFn func(context.Context, string, string, syscall.RawConn) error) error {
	var err error
	if err = setDefaultListenerSockopts(fd.pfd.Sysfd); err != nil {
		return err
	}
	var lsa syscall.Sockaddr
	if lsa, err = laddr.sockaddr(fd.family); err != nil {
		return err
	}

	if ctrlCtxFn != nil {
		c := newRawConn(fd)
		if err := ctrlCtxFn(ctx, fd.ctrlNetwork(), laddr.String(), c); err != nil {
			return err
		}
	}

	// use a system fd for the socket
	if err = syscall.Bind(fd.pfd.Sysfd, lsa); err != nil {
		return os.NewSyscallError("bind", err)
	}
	if err = listenFunc(fd.pfd.Sysfd, backlog); err != nil {
		return os.NewSyscallError("listen", err)
	}

	// -> fd.pfd.Init(fd.net, true) -> pfd.pd[pollDesc].init -> runtime_pollOpen
	if err = fd.init(); err != nil {
		return err
	}
	lsa, _ = syscall.Getsockname(fd.pfd.Sysfd)
	fd.setAddr(fd.addrFunc()(lsa), nil)
	return nil
}

// to go:linkname poll_runtime_pollOpen -> netpollopen
func runtime_pollOpen(fd uintptr)(uintptr, int)


type netFD struct {
	pfd poll.FD

	// immutable until Close
	family      int
	sotype      int
	isConnected bool // handshake completed or use of association with peer
	net         string
	laddr       Addr
	raddr       Addr
}

// poll.FD
type FD struct {
	// Lock sysfd and serialize access to Read and Write methods.
	fdmu fdMutex

	// System file descriptor. Immutable until Close.
	Sysfd int

	// Platform dependent state of the file descriptor.
	SysFile

	// I/O poller.
	pd pollDesc

	// Semaphore signaled when file is closed.
	csema uint32

	// Non-zero if this file has been set to blocking mode.
	isBlocking uint32

	// Whether this is a streaming descriptor, as opposed to a
	// packet-based descriptor like a UDP socket. Immutable.
	IsStream bool

	// Whether a zero byte read indicates EOF. This is false for a
	// message based socket connection.
	ZeroReadIsEOF bool

	// Whether this is a file rather than a network socket.
	isFile bool
}

```

### net.Accept

```golang
ln, err := net.Listen("tcp", "8888")
if err != nil {
	panic(err)
}
conn,err := ln.Accept()
if err != nil {
	panic(err)
}

func (l *TCPListener) Accept() (Conn, error) {
	if !l.ok() {
		return nil, errror
	}

	c, err := l.accept() // *
	// ....
	return c, nil
}

func (ln *TCPListener) accept() (*TCPConn, error) {
	fd, err := ln.fd.accept() // netFD -> fd.pfd.Accept
	// errr
	tc := newTCPConn(fd)
	if ln.lc.KeepAlive >= 0 {
		setKeepAlive(fd, true)
		ka := ln.lc.KeepAlive
		if ln.lc.KeepAlive == 0 {
			ka = defaultTCPKeepAlive
		}
		setKeepAlivePeriod(fd, ka)
	}
	return tc, nil
}

// Accept wraps the accept network call.
func (fd *FD) Accept() (int, syscall.Sockaddr, string, error) {
	if err := fd.readLock(); err != nil {
		return -1, nil, "", err
	}
	defer fd.readUnlock()

	if err := fd.pd.prepareRead(fd.isFile); err != nil {
		return -1, nil, "", err
	}
	for {
		s, rsa, errcall, err := accept(fd.Sysfd)
		if err == nil {
			return s, rsa, "", err
		}
		switch err {
		case syscall.EINTR:
			continue
		case syscall.EAGAIN: // socket no data
			if fd.pd.pollable() {
				if err = fd.pd.waitRead(fd.isFile); err == nil {
					continue
				}
			}
		case syscall.ECONNABORTED:
			// This means that a socket on the listen
			// queue was closed before we Accept()ed it;
			// it's a silly error, so try again.
			continue
		}
		return -1, nil, errcall, err
	}
}

func accept(s int) (int, syscall.Sockaddr, string, error) {
	ns, sa, err := Accept4Func(s, syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC)
	if err != nil {
		return -1, nil, "accept4", err
	}
	return ns, sa, "", nil
}

func (pd *pollDesc) wait (mode int, isFile bool) error {
	if pd.runtimeCtx == 0 //...
	res := runtime_pollWait(pd.runtimeCtx, mode)
	return convertErr(res, isFile)
}

type Listener interface {
	Accept() (Conn,error)
	Close() error
	Addr() Addr
}

type Conn interface {
	Read(b []byte)(n int, err error)
	Write(b []byte)(n int, err error)
	Close() error
}
```

1. 直接调用 Socket 的 accept
2. 如果失败 休眠等待新的连接
3. 将新的 Socket 包装为 TCPConn 变量返回
4. TCPConn 的 FD 信息加入监听
5. TCPConn 本质上是一个 ESTABLISHED 状态的 Socket

### net.Read

```go
conn, err := ln.Accept()
var body [100]byte
for true {
	_, err := conn.Read(body[:])
}

// net/net.go
func (c *conn) Read(b []byte)(int, error) {
	if !c.ok //
	n, err := c.fd.Read(b)
	if err != nil && err != io.EOF {
		err = &OpError{}
	}
	return n, err
}

```

1. 直接调用 Socket 原生读写方法
2. 如果失败 休眠等待可读可写
3. 被唤醒后调用系统读写

### example

```golang
func handleConnection(conn net.Conn) {
	defer conn.Close()
	var body [100] byte
	for {
		_, err := conn.Read(body[:])
		if err != nil {
			break
		}
		// print

		_, err = conn.Write(body[:])
		if err != nil {
			break
		}
	}
}

func main() {
	ln, err := net.Listen("tcp", ":8888")
	// errr
	for {
		conn, err := ln.Accept()
		// errr
		go handleConnection(conn)
	}
}
```
