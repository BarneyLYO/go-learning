# other

## cgo

go 调用 c 的代码

```go
package main

// c lang code在注释里面，且注释必须在import “C” 上面

/*
    int sum (int a, int b) {
        return a + b;
    }
*/
import "C"

func main() {
    fmt.Println(
        "1 + 1=",
        C.sum(1,1)
    )
}

// go tool cgo main.go
```

在内存中开辟一个结构体
结构体有参数也有返回值
结构体地址传入 c 方法
c 方法将结果写入返回值的位置

## 调度器配合

协程是抢占式调度
进入 c 程序后调度器无法抢占协程
调度器停止对此协程的调度

## 协程栈切换

c 的栈不受 runtime 管理
进入 c 时，需要将当前栈切换到协程栈上

## defer

```go

var mu = sync.Mutex{}

func a() {
    defer mu.Unlock() // 汇编中包装成为一个对象记录下来
    mu.Lock()
    // 函数出栈时 runtime.deferreturn(SB)
}
```

记录 defer 信息 函数退出调用
defer 代码太简单， inline 在函数最后

### 1.12 之前 堆上分配现在也差不多

在堆上开辟一个 sched.deferpool
遇到 defer 语句 将信息放入 deferpool 在 p 上面
可能有垃圾回收问题

```go
// A _defer holds an entry on the list of deferred calls.
// If you add a field here, add code to clear it in deferProcStack.
// This struct must match the code in cmd/compile/internal/ssagen/ssa.go:deferstruct
// and cmd/compile/internal/ssagen/ssa.go:(*state).call.
// Some defers will be allocated on the stack and some on the heap.
// All defers are logically part of the stack, so write barriers to
// initialize them are not required. All defers must be manually scanned,
// and for heap defers, marked.
type _defer struct {
	heap      bool
	rangefunc bool    // true for rangefunc list
	sp        uintptr // sp at time of defer
	pc        uintptr // pc at time of defer
	fn        func()  // can be nil for open-coded defers
	link      *_defer // next defer on G; can point to either heap or stack!

	// If rangefunc is true, *head is the head of the atomic linked list
	// during a range-over-func execution.
	head *atomic.Pointer[_defer]
}
```

### 栈上分配 1.13

只能保存很少的 defer 信息
函数退出时调用

### 开发编码 1.14 很难触发

如果 defer 语句在编译器可以固定
直接修改用户代码，defer 语句放入函数末尾

## recover

### panic

panic 会抛出错误
终止协程运行
带崩整个 GO 程序

```go
func main() {
    go func() {
        panic("pppppppp")
        fmt.Print("end g")
    }()

    time.Sleep(time.Second)
    fmt.Println("end, main")

}

```

### panic + defer

```go
func main() {
    defer fmt.Print("defer main") // not run
    go func() {
        defer fmt.Print("defer g") // will run
        panic("pppppppp")
        fmt.Print("end g")
    }()

    time.Sleep(time.Second)
    fmt.Println("end, main")

}
```

当遇到 panic 的协程，此协程的 defer 可以工作
但其他协程的 defer 不会工作

panic 在退出协程之前会执行所有此协程已注册的 defer
不会执行其他协程的 defer

```golang
// runtime/panic.go - gopanic(写的panic关键字会转换这个)
func gopanic(e any) {
	if e == nil {
		if debug.panicnil.Load() != 1 {
			e = new(PanicNilError)
		} else {
			panicnil.IncNonDefault()
		}
	}

	gp := getg()
	if gp.m.curg != gp {
		print("panic: ")
		printpanicval(e)
		print("\n")
		throw("panic on system stack")
	}

	if gp.m.mallocing != 0 {
		print("panic: ")
		printpanicval(e)
		print("\n")
		throw("panic during malloc")
	}
	if gp.m.preemptoff != "" {
		print("panic: ")
		printpanicval(e)
		print("\n")
		print("preempt off reason: ")
		print(gp.m.preemptoff)
		print("\n")
		throw("panic during preemptoff")
	}
	if gp.m.locks != 0 {
		print("panic: ")
		printpanicval(e)
		print("\n")
		throw("panic holding locks")
	}

	var p _panic
	p.arg = e

	runningPanicDefers.Add(1)

	p.start(sys.GetCallerPC(),
            unsafe.Pointer(sys.GetCallerSP()))

	for {
		fn, ok := p.nextDefer() //curr g defer
		if !ok {
			break
		}
		fn()
	}

	// If we're tracing, flush the current generation to make the trace more
	// readable.
	//
	// TODO(aktau): Handle a panic from within traceAdvance more gracefully.
	// Currently it would hang. Not handled now because it is very unlikely, and
	// already unrecoverable.
	if traceEnabled() {
		traceAdvance(false)
	}

	// ran out of deferred calls - old-school panic now
	// Because it is unsafe to call arbitrary user code after freezing
	// the world, we call preprintpanics to invoke all necessary Error
	// and String methods to prepare the panic strings before startpanic.
	preprintpanics(&p)

	fatalpanic(&p)   // should not return
	*(*int)(nil) = 0 // not reached
}

func (p *_panic) nextDefer() (func(), bool) {
	gp := getg()

	if !p.deferreturn {
		if gp._panic != p {
			throw("bad panic stack")
		}

		if p.recovered {
			mcall(recovery) // does not return
			throw("recovery failed")
		}
	}

	// The assembler adjusts p.argp in wrapper functions that shouldn't
	// be visible to recover(), so we need to restore it each iteration.
	p.argp = add(p.startSP, sys.MinFrameSize)

	for {
		for p.deferBitsPtr != nil {
			bits := *p.deferBitsPtr

			// Check whether any open-coded defers are still pending.
			//
			// Note: We need to check this upfront (rather than after
			// clearing the top bit) because it's possible that Goexit
			// invokes a deferred call, and there were still more pending
			// open-coded defers in the frame; but then the deferred call
			// panic and invoked the remaining defers in the frame, before
			// recovering and restarting the Goexit loop.
			if bits == 0 {
				p.deferBitsPtr = nil
				break
			}

			// Find index of top bit set.
			i := 7 - uintptr(sys.LeadingZeros8(bits))

			// Clear bit and store it back.
			bits &^= 1 << i
			*p.deferBitsPtr = bits

			return *(*func())(add(p.slotsPtr, i*goarch.PtrSize)), true
		}

	Recheck:
		if d := gp._defer; d != nil && d.sp == uintptr(p.sp) {
			if d.rangefunc {
				deferconvert(d)
				popDefer(gp)
				goto Recheck
			}

			fn := d.fn

			// TODO(mdempsky): Instead of having each deferproc call have
			// its own "deferreturn(); return" sequence, we should just make
			// them reuse the one we emit for open-coded defers.
			p.retpc = d.pc

			// Unlink and free.
			popDefer(gp)

			return fn, true
		}

		if !p.nextFrame() {
			return nil, false
		}
	}
}
```

### panic defer recover

```go
func main() {
    defer fmt.Print("defer main") // will run

    go func() {
        defer func () {
            r := recover()
            fmt.Print("defer g", r)
        }()
        panic("pppppppp")
        fmt.Print("end g")
    }()

    time.Sleep(time.Second)
    fmt.Println("end, main")
}
```

defer 中执行 recover 可以拯救 panic 的协程
如果 recover， defer 会使用堆上的 deferpool
遇到 panic panic 会从 deferpool 取出 defer 语句执行
defer 中调用 recover， 可以终止 panic 的过程

## reflect

获取对象类型
对任意类型变量赋值
调用任意动态的方法

### 元数据

数据的属性
xxx.class in java

#### reflect.type 对象的类型 interface

对象的类型表示为一个接口
可以对这种类型做各种操作

```go
type Type interface {

	// Align returns the alignment in bytes of a value of
	// this type when allocated in memory.
	Align() int

	// FieldAlign returns the alignment in bytes of a value of
	// this type when used as a field in a struct.
	FieldAlign() int

	// Method returns the i'th method in the type's method set.
	// It panics if i is not in the range [0, NumMethod()).
	//
	// For a non-interface type T or *T, the returned Method's Type and Func
	// fields describe a function whose first argument is the receiver,
	// and only exported methods are accessible.
	//
	// For an interface type, the returned Method's Type field gives the
	// method signature, without a receiver, and the Func field is nil.
	//
	// Methods are sorted in lexicographic order.
	Method(int) Method

	// MethodByName returns the method with that name in the type's
	// method set and a boolean indicating if the method was found.
	//
	// For a non-interface type T or *T, the returned Method's Type and Func
	// fields describe a function whose first argument is the receiver.
	//
	// For an interface type, the returned Method's Type field gives the
	// method signature, without a receiver, and the Func field is nil.
	MethodByName(string) (Method, bool)

	// NumMethod returns the number of methods accessible using Method.
	//
	// For a non-interface type, it returns the number of exported methods.
	//
	// For an interface type, it returns the number of exported and unexported methods.
	NumMethod() int

	// Name returns the type's name within its package for a defined type.
	// For other (non-defined) types it returns the empty string.
	Name() string

	// PkgPath returns a defined type's package path, that is, the import path
	// that uniquely identifies the package, such as "encoding/base64".
	// If the type was predeclared (string, error) or not defined (*T, struct{},
	// []int, or A where A is an alias for a non-defined type), the package path
	// will be the empty string.
	PkgPath() string

	// Size returns the number of bytes needed to store
	// a value of the given type; it is analogous to unsafe.Sizeof.
	Size() uintptr

	// String returns a string representation of the type.
	// The string representation may use shortened package names
	// (e.g., base64 instead of "encoding/base64") and is not
	// guaranteed to be unique among types. To test for type identity,
	// compare the Types directly.
	String() string

	// Kind returns the specific kind of this type.
	Kind() Kind

	// Implements reports whether the type implements the interface type u.
	Implements(u Type) bool

	// AssignableTo reports whether a value of the type is assignable to type u.
	AssignableTo(u Type) bool

	// ConvertibleTo reports whether a value of the type is convertible to type u.
	// Even if ConvertibleTo returns true, the conversion may still panic.
	// For example, a slice of type []T is convertible to *[N]T,
	// but the conversion will panic if its length is less than N.
	ConvertibleTo(u Type) bool

	// Comparable reports whether values of this type are comparable.
	// Even if Comparable returns true, the comparison may still panic.
	// For example, values of interface type are comparable,
	// but the comparison will panic if their dynamic type is not comparable.
	Comparable() bool

	// Bits returns the size of the type in bits.
	// It panics if the type's Kind is not one of the
	// sized or unsized Int, Uint, Float, or Complex kinds.
	Bits() int

	// ChanDir returns a channel type's direction.
	// It panics if the type's Kind is not Chan.
	ChanDir() ChanDir

	// IsVariadic reports whether a function type's final input parameter
	// is a "..." parameter. If so, t.In(t.NumIn() - 1) returns the parameter's
	// implicit actual type []T.
	//
	// For concreteness, if t represents func(x int, y ... float64), then
	//
	//	t.NumIn() == 2
	//	t.In(0) is the reflect.Type for "int"
	//	t.In(1) is the reflect.Type for "[]float64"
	//	t.IsVariadic() == true
	//
	// IsVariadic panics if the type's Kind is not Func.
	IsVariadic() bool

	// Elem returns a type's element type.
	// It panics if the type's Kind is not Array, Chan, Map, Pointer, or Slice.
	Elem() Type

	// Field returns a struct type's i'th field.
	// It panics if the type's Kind is not Struct.
	// It panics if i is not in the range [0, NumField()).
	Field(i int) StructField

	// FieldByIndex returns the nested field corresponding
	// to the index sequence. It is equivalent to calling Field
	// successively for each index i.
	// It panics if the type's Kind is not Struct.
	FieldByIndex(index []int) StructField

	// FieldByName returns the struct field with the given name
	// and a boolean indicating if the field was found.
	// If the returned field is promoted from an embedded struct,
	// then Offset in the returned StructField is the offset in
	// the embedded struct.
	FieldByName(name string) (StructField, bool)

	// FieldByNameFunc returns the struct field with a name
	// that satisfies the match function and a boolean indicating if
	// the field was found.
	//
	// FieldByNameFunc considers the fields in the struct itself
	// and then the fields in any embedded structs, in breadth first order,
	// stopping at the shallowest nesting depth containing one or more
	// fields satisfying the match function. If multiple fields at that depth
	// satisfy the match function, they cancel each other
	// and FieldByNameFunc returns no match.
	// This behavior mirrors Go's handling of name lookup in
	// structs containing embedded fields.
	//
	// If the returned field is promoted from an embedded struct,
	// then Offset in the returned StructField is the offset in
	// the embedded struct.
	FieldByNameFunc(match func(string) bool) (StructField, bool)

	// In returns the type of a function type's i'th input parameter.
	// It panics if the type's Kind is not Func.
	// It panics if i is not in the range [0, NumIn()).
	In(i int) Type

	// Key returns a map type's key type.
	// It panics if the type's Kind is not Map.
	Key() Type

	// Len returns an array type's length.
	// It panics if the type's Kind is not Array.
	Len() int

	// NumField returns a struct type's field count.
	// It panics if the type's Kind is not Struct.
	NumField() int

	// NumIn returns a function type's input parameter count.
	// It panics if the type's Kind is not Func.
	NumIn() int

	// NumOut returns a function type's output parameter count.
	// It panics if the type's Kind is not Func.
	NumOut() int

	// Out returns the type of a function type's i'th output parameter.
	// It panics if the type's Kind is not Func.
	// It panics if i is not in the range [0, NumOut()).
	Out(i int) Type

	// OverflowComplex reports whether the complex128 x cannot be represented by type t.
	// It panics if t's Kind is not Complex64 or Complex128.
	OverflowComplex(x complex128) bool

	// OverflowFloat reports whether the float64 x cannot be represented by type t.
	// It panics if t's Kind is not Float32 or Float64.
	OverflowFloat(x float64) bool

	// OverflowInt reports whether the int64 x cannot be represented by type t.
	// It panics if t's Kind is not Int, Int8, Int16, Int32, or Int64.
	OverflowInt(x int64) bool

	// OverflowUint reports whether the uint64 x cannot be represented by type t.
	// It panics if t's Kind is not Uint, Uintptr, Uint8, Uint16, Uint32, or Uint64.
	OverflowUint(x uint64) bool

	// CanSeq reports whether a [Value] with this type can be iterated over using [Value.Seq].
	CanSeq() bool

	// CanSeq2 reports whether a [Value] with this type can be iterated over using [Value.Seq2].
	CanSeq2() bool
	// contains filtered or unexported methods
}
```

#### reflect.Value 对象的值 struct

可以对这种类型值做各种操作

```go
s := "moody"

st := reflect.TypeOf(s)

sv := reflect.ValueOf(s)

s2, ok := sv.Interface().(string) // type assertion


func MyAdd (a, b int) int {
    return a + b
}

func MyMin (a, b int) int {
    return a - b
}

func Call(f func(a, b int) int) {
    v := reflect.ValueOf(f)
    if v.Kind() != reflect.Func {
        return
    }
    argv := make([]reflect.Value, 2)
    argv[0] = reflect.ValueOf(1)
    argv[1] = reflect.ValueOf(2)
    result := v.Call(argv)

    fmt.Println(result[0].Int())
}

```
