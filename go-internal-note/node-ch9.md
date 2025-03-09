# 内存模型与垃圾回收

1. 栈内存
2. 堆内存
3. 垃圾回收

## go 的协程栈的作用

**第一个栈帧是 goexit**
**之后是方法调用**

1. 协程的执行路径
2. 局部变量
3. 函数传参
4. 函数返回值传递

## go 协程栈的位置

go 的堆内存上，大部分语言为分开
由于栈内存也在堆上，GC 也用来释放栈内存
go 的堆内存位于操作系统的虚拟内存上
相当于一个对象

## go 协程栈的结构

从上往下， 越早进入，越在上方
栈帧的大小取决于编译时计算

### 栈帧

```golang
func main() {
    a := 3
    b := 5
    print(sum(a,b))
}

func sum(a,b int) int{
    sum := 0
    sum = a + b
    return sum
}
```

#### main

1. 调用者的栈基址 分配栈帧时的第一条数据 (runtime.main)
2. a = 3
3. b = 5
4. sum 返回值 返回值的内存空间由 caller 开辟
5. sum 的函数参数 5 (b) 反的 拷贝 b 的值
6. sum 的函数参数 3 (a) 拷贝 a 的值
7. sum 返回后的指令

##### 开辟 sum 的栈帧 进入 sum 的栈帧

1. main 的栈基址
2. sum = 0
3. sum = 8 从 main 的栈帧中查找
4. return 将返回值写入 main 中的 4 然后 sum 栈帧被回收

##### sum 栈帧被回收 由于 sum 栈帧调用结束 main 的 567 地址被回收

5. print 的返回值
6. print 的传参 从后往前

##### 开辟 print 栈帧 依此类推

### 参数传递

GO 使用参数拷贝传递 值传递
传递结构体时会拷贝结构体中的全部内容 so 指针传递咯 baby

### 协程栈不够大怎么办 2k - 4k

#### 局部变量太大

##### 逃逸分析

- 不是所有变量都在协程栈上
- 栈帧回收后需要继续使用变量
- 太大的变量

###### 触发情形

1. 指针逃逸
   函数返回局部变量指针
2. 空接口逃逸
   函数的参数为 interface{} 函数的实参很可能会逃逸 因为可能会使用反射 反射往往要求参数在堆上
3. 大变量逃逸
   如果局部变量太大导致栈空间不足 64 位机器 64kB 的变量就会逃逸

#### 栈帧太多

只能栈扩容哦
协程在函数调用前判断栈空间 morestack 必要时对栈进行扩容

##### go1.13 之前使用分段栈

在空位置开辟
逻辑上判断连续
没有空间浪费但是栈指针在不连续的空间跳转

##### go1.13 之后使用连续栈 morestack 汇编实现

开辟新栈空间 将老的栈帧拷贝到新栈空间
空间保持连续 但是伸缩时开销很大
空间不足时扩容 变为原来的两倍
空间使用率不足四分之一的时候 变成原来的二分之一

## go 的堆内存结构

### 操作系统虚拟内存

操作系统给应用提供的虚拟内存空间

### heapArena 内存单元

go 一批一批获取虚拟内存
每次申请 64MB
最多有 2\*\*20 个内存单元 256tb
所有的 heapArena 组成了 mheap go 的堆内存
// runtime.mheap.go

```go
// A heapArena stores metadata for a heap arena. heapArenas are stored
// outside of the Go heap and accessed via the mheap_.arenas index.
type heapArena struct {
	_ sys.NotInHeap

	// spans maps from virtual address page ID within this arena to *mspan.
	// For allocated spans, their pages map to the span itself.
	// For free spans, only the lowest and highest pages map to the span itself.
	// Internal pages map to an arbitrary span.
	// For pages that have never been allocated, spans entries are nil.
	//
	// Modifications are protected by mheap.lock. Reads can be
	// performed without locking, but ONLY from indexes that are
	// known to contain in-use or stack spans. This means there
	// must not be a safe-point between establishing that an
	// address is live and looking it up in the spans array.
	spans [pagesPerArena]*mspan

	// pageInUse is a bitmap that indicates which spans are in
	// state mSpanInUse. This bitmap is indexed by page number,
	// but only the bit corresponding to the first page in each
	// span is used.
	//
	// Reads and writes are atomic.
	pageInUse [pagesPerArena / 8]uint8

	// pageMarks is a bitmap that indicates which spans have any
	// marked objects on them. Like pageInUse, only the bit
	// corresponding to the first page in each span is used.
	//
	// Writes are done atomically during marking. Reads are
	// non-atomic and lock-free since they only occur during
	// sweeping (and hence never race with writes).
	//
	// This is used to quickly find whole spans that can be freed.
	//
	// TODO(austin): It would be nice if this was uint64 for
	// faster scanning, but we don't have 64-bit atomic bit
	// operations.
	pageMarks [pagesPerArena / 8]uint8

	// pageSpecials is a bitmap that indicates which spans have
	// specials (finalizers or other). Like pageInUse, only the bit
	// corresponding to the first page in each span is used.
	//
	// Writes are done atomically whenever a special is added to
	// a span and whenever the last special is removed from a span.
	// Reads are done atomically to find spans containing specials
	// during marking.
	pageSpecials [pagesPerArena / 8]uint8

	// checkmarks stores the debug.gccheckmark state. It is only
	// used if debug.gccheckmark > 0.
	checkmarks *checkmarksMap

	// zeroedBase marks the first byte of the first page in this
	// arena which hasn't been used yet and is therefore already
	// zero. zeroedBase is relative to the arena base.
	// Increases monotonically until it hits heapArenaBytes.
	//
	// This field is sufficient to determine if an allocation
	// needs to be zeroed because the page allocator follows an
	// address-ordered first-fit policy.
	//
	// Read atomically and written with an atomic CAS.
	zeroedBase uintptr
}

// Main malloc heap.
// The heap itself is the "free" and "scav" treaps,
// but all the other global data is here too.
//
// mheap must not be heap-allocated because it contains mSpanLists,
// which must not be heap-allocated.
type mheap struct {
	_ sys.NotInHeap

	// lock must only be acquired on the system stack, otherwise a g
	// could self-deadlock if its stack grows with the lock held.
	lock mutex

	pages pageAlloc // page allocation data structure

	sweepgen uint32 // sweep generation, see comment in mspan; written during STW

	// allspans is a slice of all mspans ever created. Each mspan
	// appears exactly once.
	//
	// The memory for allspans is manually managed and can be
	// reallocated and move as the heap grows.
	//
	// In general, allspans is protected by mheap_.lock, which
	// prevents concurrent access as well as freeing the backing
	// store. Accesses during STW might not hold the lock, but
	// must ensure that allocation cannot happen around the
	// access (since that may free the backing store).
	allspans []*mspan // all spans out there

	// Proportional sweep
	//
	// These parameters represent a linear function from gcController.heapLive
	// to page sweep count. The proportional sweep system works to
	// stay in the black by keeping the current page sweep count
	// above this line at the current gcController.heapLive.
	//
	// The line has slope sweepPagesPerByte and passes through a
	// basis point at (sweepHeapLiveBasis, pagesSweptBasis). At
	// any given time, the system is at (gcController.heapLive,
	// pagesSwept) in this space.
	//
	// It is important that the line pass through a point we
	// control rather than simply starting at a 0,0 origin
	// because that lets us adjust sweep pacing at any time while
	// accounting for current progress. If we could only adjust
	// the slope, it would create a discontinuity in debt if any
	// progress has already been made.
	pagesInUse         atomic.Uintptr // pages of spans in stats mSpanInUse
	pagesSwept         atomic.Uint64  // pages swept this cycle
	pagesSweptBasis    atomic.Uint64  // pagesSwept to use as the origin of the sweep ratio
	sweepHeapLiveBasis uint64         // value of gcController.heapLive to use as the origin of sweep ratio; written with lock, read without
	sweepPagesPerByte  float64        // proportional sweep ratio; written with lock, read without

	// Page reclaimer state

	// reclaimIndex is the page index in allArenas of next page to
	// reclaim. Specifically, it refers to page (i %
	// pagesPerArena) of arena allArenas[i / pagesPerArena].
	//
	// If this is >= 1<<63, the page reclaimer is done scanning
	// the page marks.
	reclaimIndex atomic.Uint64

	// reclaimCredit is spare credit for extra pages swept. Since
	// the page reclaimer works in large chunks, it may reclaim
	// more than requested. Any spare pages released go to this
	// credit pool.
	reclaimCredit atomic.Uintptr

	_ cpu.CacheLinePad // prevents false-sharing between arenas and preceding variables

	// arenas is the heap arena map. It points to the metadata for
	// the heap for every arena frame of the entire usable virtual
	// address space.
	//
	// Use arenaIndex to compute indexes into this array.
	//
	// For regions of the address space that are not backed by the
	// Go heap, the arena map contains nil.
	//
	// Modifications are protected by mheap_.lock. Reads can be
	// performed without locking; however, a given entry can
	// transition from nil to non-nil at any time when the lock
	// isn't held. (Entries never transitions back to nil.)
	//
	// In general, this is a two-level mapping consisting of an L1
	// map and possibly many L2 maps. This saves space when there
	// are a huge number of arena frames. However, on many
	// platforms (even 64-bit), arenaL1Bits is 0, making this
	// effectively a single-level map. In this case, arenas[0]
	// will never be nil.
	arenas [1 << arenaL1Bits]*[1 << arenaL2Bits]*heapArena

	// arenasHugePages indicates whether arenas' L2 entries are eligible
	// to be backed by huge pages.
	arenasHugePages bool

	// heapArenaAlloc is pre-reserved space for allocating heapArena
	// objects. This is only used on 32-bit, where we pre-reserve
	// this space to avoid interleaving it with the heap itself.
	heapArenaAlloc linearAlloc

	// arenaHints is a list of addresses at which to attempt to
	// add more heap arenas. This is initially populated with a
	// set of general hint addresses, and grown with the bounds of
	// actual heap arena ranges.
	arenaHints *arenaHint

	// arena is a pre-reserved space for allocating heap arenas
	// (the actual arenas). This is only used on 32-bit.
	arena linearAlloc

	// allArenas is the arenaIndex of every mapped arena. This can
	// be used to iterate through the address space.
	//
	// Access is protected by mheap_.lock. However, since this is
	// append-only and old backing arrays are never freed, it is
	// safe to acquire mheap_.lock, copy the slice header, and
	// then release mheap_.lock.
	allArenas []arenaIdx

	// sweepArenas is a snapshot of allArenas taken at the
	// beginning of the sweep cycle. This can be read safely by
	// simply blocking GC (by disabling preemption).
	sweepArenas []arenaIdx

	// markArenas is a snapshot of allArenas taken at the beginning
	// of the mark cycle. Because allArenas is append-only, neither
	// this slice nor its contents will change during the mark, so
	// it can be read safely.
	markArenas []arenaIdx

	// curArena is the arena that the heap is currently growing
	// into. This should always be physPageSize-aligned.
	curArena struct {
		base, end uintptr
	}

	// central free lists for small size classes.
	// the padding makes sure that the mcentrals are
	// spaced CacheLinePadSize bytes apart, so that each mcentral.lock
	// gets its own cache line.
	// central is indexed by spanClass.
	central [numSpanClasses]struct {
		mcentral mcentral
		pad      [(cpu.CacheLinePadSize - unsafe.Sizeof(mcentral{})%cpu.CacheLinePadSize) % cpu.CacheLinePadSize]byte
	}

	spanalloc              fixalloc // allocator for span*
	cachealloc             fixalloc // allocator for mcache*
	specialfinalizeralloc  fixalloc // allocator for specialfinalizer*
	specialCleanupAlloc    fixalloc // allocator for specialcleanup*
	specialprofilealloc    fixalloc // allocator for specialprofile*
	specialReachableAlloc  fixalloc // allocator for specialReachable
	specialPinCounterAlloc fixalloc // allocator for specialPinCounter
	specialWeakHandleAlloc fixalloc // allocator for specialWeakHandle
	speciallock            mutex    // lock for special record allocators.
	arenaHintAlloc         fixalloc // allocator for arenaHints

	// User arena state.
	//
	// Protected by mheap_.lock.
	userArena struct {
		// arenaHints is a list of addresses at which to attempt to
		// add more heap arenas for user arena chunks. This is initially
		// populated with a set of general hint addresses, and grown with
		// the bounds of actual heap arena ranges.
		arenaHints *arenaHint

		// quarantineList is a list of user arena spans that have been set to fault, but
		// are waiting for all pointers into them to go away. Sweeping handles
		// identifying when this is true, and moves the span to the ready list.
		quarantineList mSpanList

		// readyList is a list of empty user arena spans that are ready for reuse.
		readyList mSpanList
	}

	// cleanupID is a counter which is incremented each time a cleanup special is added
	// to a span. It's used to create globally unique identifiers for individual cleanup.
	// cleanupID is protected by mheap_.lock. It should only be incremented while holding
	// the lock.
	cleanupID uint64

	unused *specialfinalizer // never set, just here to force the specialfinalizer type into DWARF
}

```

以下对 heapArena 的分配都会造成内存碎片化

1. 线性分配
2. 链表分配

go 使用 heapArena 的分级分配
go 会把 heapArena 切成小块 根据不同级别决定大小
级别就是 mspan

### mspan 内存管理单元

根据隔离适应策略 使用内存时最小的单位为 mspan
每个 mspan 为 n 个相同大小的格子
67 种
需要时才会根据需求开辟

```go
type mspan struct {
	_    sys.NotInHeap
	next *mspan     // next span in list, or nil if none
	prev *mspan     // previous span in list, or nil if none
	list *mSpanList // For debugging.

	startAddr uintptr // address of first byte of span aka s.base()
	npages    uintptr // number of pages in span

	manualFreeList gclinkptr // list of free objects in mSpanManual spans

	// freeindex is the slot index between 0 and nelems at which to begin scanning
	// for the next free object in this span.
	// Each allocation scans allocBits starting at freeindex until it encounters a 0
	// indicating a free object. freeindex is then adjusted so that subsequent scans begin
	// just past the newly discovered free object.
	//
	// If freeindex == nelem, this span has no free objects.
	//
	// allocBits is a bitmap of objects in this span.
	// If n >= freeindex and allocBits[n/8] & (1<<(n%8)) is 0
	// then object n is free;
	// otherwise, object n is allocated. Bits starting at nelem are
	// undefined and should never be referenced.
	//
	// Object n starts at address n*elemsize + (start << pageShift).
	freeindex uint16
	// TODO: Look up nelems from sizeclass and remove this field if it
	// helps performance.
	nelems uint16 // number of object in the span.
	// freeIndexForScan is like freeindex, except that freeindex is
	// used by the allocator whereas freeIndexForScan is used by the
	// GC scanner. They are two fields so that the GC sees the object
	// is allocated only when the object and the heap bits are
	// initialized (see also the assignment of freeIndexForScan in
	// mallocgc, and issue 54596).
	freeIndexForScan uint16

	// Cache of the allocBits at freeindex. allocCache is shifted
	// such that the lowest bit corresponds to the bit freeindex.
	// allocCache holds the complement of allocBits, thus allowing
	// ctz (count trailing zero) to use it directly.
	// allocCache may contain bits beyond s.nelems; the caller must ignore
	// these.
	allocCache uint64

	// allocBits and gcmarkBits hold pointers to a span's mark and
	// allocation bits. The pointers are 8 byte aligned.
	// There are three arenas where this data is held.
	// free: Dirty arenas that are no longer accessed
	//       and can be reused.
	// next: Holds information to be used in the next GC cycle.
	// current: Information being used during this GC cycle.
	// previous: Information being used during the last GC cycle.
	// A new GC cycle starts with the call to finishsweep_m.
	// finishsweep_m moves the previous arena to the free arena,
	// the current arena to the previous arena, and
	// the next arena to the current arena.
	// The next arena is populated as the spans request
	// memory to hold gcmarkBits for the next GC cycle as well
	// as allocBits for newly allocated spans.
	//
	// The pointer arithmetic is done "by hand" instead of using
	// arrays to avoid bounds checks along critical performance
	// paths.
	// The sweep will free the old allocBits and set allocBits to the
	// gcmarkBits. The gcmarkBits are replaced with a fresh zeroed
	// out memory.
	allocBits  *gcBits
	gcmarkBits *gcBits
	pinnerBits *gcBits // bitmap for pinned objects; accessed atomically

	// sweep generation:
	// if sweepgen == h->sweepgen - 2, the span needs sweeping
	// if sweepgen == h->sweepgen - 1, the span is currently being swept
	// if sweepgen == h->sweepgen, the span is swept and ready to use
	// if sweepgen == h->sweepgen + 1, the span was cached before sweep began and is still cached, and needs sweeping
	// if sweepgen == h->sweepgen + 3, the span was swept and then cached and is still cached
	// h->sweepgen is incremented by 2 after every GC

	sweepgen              uint32
	divMul                uint32        // for divide by elemsize
	allocCount            uint16        // number of allocated objects
	spanclass             spanClass     // size class and noscan (uint8)
	state                 mSpanStateBox // mSpanInUse etc; accessed atomically (get/set methods)
	needzero              uint8         // needs to be zeroed before allocation
	isUserArenaChunk      bool          // whether or not this span represents a user arena
	allocCountBeforeCache uint16        // a copy of allocCount that is stored just before this span is cached
	elemsize              uintptr       // computed from sizeclass or from npages
	limit                 uintptr       // end of data in span
	speciallock           mutex         // guards specials list and changes to pinnerBits
	specials              *special      // linked list of special records sorted by offset.
	userArenaChunkFree    addrRange     // interval for managing chunk allocation
	largeType             *_type        // malloc header for large objects.
}
```

#### mspan 查询

每个 heapArena 的 mspan 不确定 如何快速找到所需的 mspan 级别

##### 中心索引 mcentral

136 个 mcentral
68 个需要 GC 扫描的 mspan
68 个不需要 GC 扫描的 比如常量
mspan 有 68 种

mcentral 为每一个 mspan class 的链表头

```go
// Central list of free objects of a given size.
type mcentral struct {
	_         sys.NotInHeap
	spanclass spanClass

	// partial and full contain two mspan sets: one of swept in-use
	// spans, and one of unswept in-use spans. These two trade
	// roles on each GC cycle. The unswept set is drained either by
	// allocation or by the background sweeper in every GC cycle,
	// so only two roles are necessary.
	//
	// sweepgen is increased by 2 on each GC cycle, so the swept
	// spans are in partial[sweepgen/2%2] and the unswept spans are in
	// partial[1-sweepgen/2%2]. Sweeping pops spans from the
	// unswept set and pushes spans that are still in-use on the
	// swept set. Likewise, allocating an in-use span pushes it
	// on the swept set.
	//
	// Some parts of the sweeper can sweep arbitrary spans, and hence
	// can't remove them from the unswept set, but will add the span
	// to the appropriate swept list. As a result, the parts of the
	// sweeper and mcentral that do consume from the unswept list may
	// encounter swept spans, and these should be ignored.
	partial [2]spanSet // list of spans with a free object
	full    [2]spanSet // list of spans with no free objects
}
```

有性能问题 使用互斥锁保护
高并发场景下 锁冲突问题严重
参考 GMP 模型 增加线程本地缓存
每一个线程有一个 mcentral 缓存
runtime 为每一个 P 分配 136 个 mspan

##### 线程缓存 mcache

每一个 p 有一个 mcache
一个 mcache 有 136mspan
mcache 中 每一个级别的 mspan 只有一个
用完了会从 mcentral 换一个新的
mcentral 满了 会从 heapAreana 可开启新的 mspan

```go
// Per-thread (in Go, per-P) cache for small objects.
// This includes a small object cache and local allocation stats.
// No locking needed because it is per-thread (per-P).
//
// mcaches are allocated from non-GC'd memory, so any heap pointers
// must be specially handled.
type mcache struct {
	_ sys.NotInHeap

	// The following members are accessed on every malloc,
	// so they are grouped here for better caching.
	nextSample  int64   // trigger heap sample after allocating this many bytes
	memProfRate int     // cached mem profile rate, used to detect changes
	scanAlloc   uintptr // bytes of scannable heap allocated

	// Allocator cache for tiny objects w/o pointers.
	// See "Tiny allocator" comment in malloc.go.

	// tiny points to the beginning of the current tiny block, or
	// nil if there is no current tiny block.
	//
	// tiny is a heap pointer. Since mcache is in non-GC'd memory,
	// we handle it by clearing it in releaseAll during mark
	// termination.
	//
	// tinyAllocs is the number of tiny allocations performed
	// by the P that owns this mcache.
	tiny       uintptr
	tinyoffset uintptr
	tinyAllocs uintptr

	// The rest is not accessed on every malloc.

	alloc [numSpanClasses]*mspan // spans to allocate from, indexed by spanClass

	stackcache [_NumStackOrders]stackfreelist

	// flushGen indicates the sweepgen during which this mcache
	// was last flushed. If flushGen != mheap_.sweepgen, the spans
	// in this mcache are stale and need to the flushed so they
	// can be swept. This is done in acquirep.
	flushGen atomic.Uint32
}
```

Go 模仿了 TCmalloc 建立了自己的堆内存架构

## 对象分级

#### tiny 0 - 16B 无指针

分配到普通 mspan class 1 - class 67
从 mcache 拿到 class 2 mspan 16byte 一个 slot
将多个微对象合并成一个 16byte 存入

```go
// Allocate an object of size bytes.
// Small objects are allocated from the per-P cache's free lists.
// Large objects (> 32 kB) are allocated straight from the heap.
//
// mallocgc should be an internal detail,
// but widely used packages access it using linkname.
//go:linkname mallocgc
func mallocgc(size uintptr, typ *_type, needzero bool) unsafe.Pointer {
	if doubleCheckMalloc {
		if gcphase == _GCmarktermination {
			throw("mallocgc called with gcphase == _GCmarktermination")
		}
	}

	// Short-circuit zero-sized allocation requests.
	if size == 0 {
		return unsafe.Pointer(&zerobase)
	}

	// It's possible for any malloc to trigger sweeping, which may in
	// turn queue finalizers. Record this dynamic lock edge.
	// N.B. Compiled away if lockrank experiment is not enabled.
	lockRankMayQueueFinalizer()

	// Pre-malloc debug hooks.
	if debug.malloc {
		if x := preMallocgcDebug(size, typ); x != nil {
			return x
		}
	}

	// For ASAN, we allocate extra memory around each allocation called the "redzone."
	// These "redzones" are marked as unaddressable.
	var asanRZ uintptr
	if asanenabled {
		asanRZ = redZoneSize(size)
		size += asanRZ
	}

	// Assist the GC if needed.
	if gcBlackenEnabled != 0 {
		deductAssistCredit(size)
	}

	// Actually do the allocation.
	var x unsafe.Pointer
	var elemsize uintptr
	if size <= maxSmallSize-mallocHeaderSize {
		if typ == nil || !typ.Pointers() {
			if size < maxTinySize {
				x, elemsize = mallocgcTiny(size, typ, needzero)
			} else {
				x, elemsize = mallocgcSmallNoscan(size, typ, needzero)
			}
		} else if heapBitsInSpan(size) {
			x, elemsize = mallocgcSmallScanNoHeader(size, typ, needzero)
		} else {
			x, elemsize = mallocgcSmallScanHeader(size, typ, needzero)
		}
	} else {
		x, elemsize = mallocgcLarge(size, typ, needzero)
	}

	// Notify sanitizers, if enabled.
	if raceenabled {
		racemalloc(x, size-asanRZ)
	}
	if msanenabled {
		msanmalloc(x, size-asanRZ)
	}
	if asanenabled {
		// Poison the space between the end of the requested size of x
		// and the end of the slot. Unpoison the requested allocation.
		frag := elemsize - size
		if typ != nil && typ.Pointers() && !heapBitsInSpan(elemsize) && size <= maxSmallSize-mallocHeaderSize {
			frag -= mallocHeaderSize
		}
		asanpoison(unsafe.Add(x, size-asanRZ), asanRZ)
		asanunpoison(x, size-asanRZ)
	}

	// Adjust our GC assist debt to account for internal fragmentation.
	if gcBlackenEnabled != 0 && elemsize != 0 {
		if assistG := getg().m.curg; assistG != nil {
			assistG.gcAssistBytes -= int64(elemsize - size)
		}
	}

	// Post-malloc debug hooks.
	if debug.malloc {
		postMallocgcDebug(x, elemsize, typ)
	}
	return x
}

```

#### small 16B - 32KB

#### Large 大对象 32KB+

量身定做 mspan class 0

## GC

什么样的对象需要垃圾回收
标记清除 vs 标记整理(java 老年代) vs 标记复制 (两块 heapArena 有用的复制到另外一块，java 新生代)
由于 golang 的独特分级 mspan based 的 heaparena 堆结构内存，标记清楚就可以了

找到有引用的对象 剩下就是没有引用的

### 从哪里开始找 GC root

1. 被栈上的指针引用
2. 全局变量指针引用
3. 寄存器中的指针引用

上述变量称为 gc root set

### 怎么找

Root 节点进行广度优先搜索 所谓的可达性分析标记

### 串行 GC 老版本 go

stop the world， 暂停所有其他协程 性能影响太大
可达性分析标记
释放
恢复协程

### 并发垃圾回收

标记阶段难点在

#### 三色标记法

3 个列表

1. 黑色 有用 已经分析扫描
2. 灰色 有用 未分析扫描
3. 白色 暂时 无用

从 Go1.5 开始采用三色标记法实现标记阶段的并发。

最开始所有对象都是白色
扫描所有可达对象，标记为灰色，放入待处理队列
从队列提取灰色对象，将其引用对象标记为灰色放入队列，自身标记为黑色
写屏障监控对象内存修改，重新标色或是放入队列
完成标记后 对象不是白色就是黑色，清理操作只需要把白色对象回收内存回收就好。
首先是指能够跟用户代码并发的进行，其次是指标记工作不是递归地进行，而是多个 goroutine 并发的进行。前者通过 write-barrier 解决并发问题，后者通过 gc-work 队列实现非递归地 mark 可达对象。

http://legendtkl.com/2017/04/28/golang-gc/

### GC 时机

1. 系统定时
   sysmon 定时检查 2min 没有 GC 枪支 GC runtime/proc.go
2. 用户触发
   runtime.GC
3. 申请内存
   申请堆空间时 可能导致 GC mallocgc

### GC 优化原则

少在堆上少产生垃圾

1. 内存池化
   缓存性质对象 chan 的缓冲区
   频繁创建删除
   使用内存池
2. 减少逃逸
   逃逸使原本在栈上的对象进入堆中
   fmt 包容易导致逃逸
   返回指针不是拷贝
3. 使用空结构体
   空结构体指向一个固定地址
   不占用堆空间
   比如用 chan 传递空结构体

### gc 分析

go tool pprof
go tool trace
GODEBUG="gctrace=1"
