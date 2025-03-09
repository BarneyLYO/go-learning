# ch4

## 0 byte struct

```go
type k struct {} // unsafe.Sizeof(k{}) === 0 byte
```

所有的单独的空结构体指向 zero base 地址: runtime.malloc.go

### tips

用此特性实现 hashset, 因为 map key 是 comparable 的，所以泛型 T 也是 comparable 的

```go
type HashSet[T comparable] map[T]struct{}
```

或者只给 channel 发一个信号

```go
a := make(chan struct{})
```

## string

```go
fmt.Println(unsafe.Sizeof("aaaa")) // 16
fmt.Println(unsafe.Sizeof("aaaa1")) // 16
```

string => stringStruct(runtime/string.go) in golang

```go
type stringStruct struct {
    str unsafe.Pointer // len(unsafe.Pointer) = 8 uintptr => int
    len int // len(int) = 8
}
```

字符串本质是一个结构体
stringStruct.str => 底层 Byte 数组
stringStruct.len => 字节数

reflect/value.go

```go
type StringHeader struct {
    Data uintptr
    Len int // number of rune(utf-8)
}
```

```go
func main() {
    s := "一二三"
    sh := (*reflect.StringHeader)(unsafe.Pointer(&s)) // unsafe.Pointer(&s) 万能指针
    fmt.Println(sh.Len) // 9 => Len is number of rune
}
```

string in golang is unicode (8^3)
utf8 是变长格式 西方常用字符只需一个字节编码 其他字符需要三个字节 有时需要四个

### 字符串访问

```go
len(aString) // stringStruct.len
for i:= 0; i< len(aString); i++ {
    fmt.Println(s[i]) // the byte underneath
}

// correct way
for idx, char := range s {
    fmt.Println(char) // real unicode(rune)
    fmt.Printf("%c\n", char) // runtime/utf8.go decoderune/encoderune
}

type rune int32
```

### 字符串切分

string => []rune => string

```golang
s = string([]rune(s)[:3])
```

## slice - ref to a array

runtime/slice.go

```go
type slice struct { // reflect.SliceHeader
    array unsafe.Pointer
    len int  // length
    cap int // capacity
}

Unsafe.Sizeof(
    []int{
        1,2,3,
    }
) // 8 + 8 + 8
```

### create

```golang
    arr := [4]int{0,1,2,3}
    slice := arr[0:2]
    slice = make([]int, 10) // makeSlice method(runtime/slice.go), happen in runtime.
    slice = []int{1,2,3,4}  // first create array, and runtime.newobject(slice). and put val into slice, this happend in compiler time,
```

### access

index,range,len(slice)

### add

if no cap change. compiler will just append into array underneath
if cap change, compiler will change the code to runtime.growslice()

1. if wanted cap > current cap \* 2, use wanted cap
2. if slice len < 1024, double
3. if slice len > 1024, 25%
4. when slice growth, not concurrent saftly, because its not atomic operation.

## map

runtime/map.go

```go
type hmap struct {

    count int
    flags uint8
    B unit8 // log_2 of # of buckets
    noverflow uint16
    hash0 uint32 // hash seed

    buckets unsafe.Pointer // array of 2^B, nil if count = 0, bmap list
    oldbuckets unsafe.Pointer
    nevacuate uintptr
    extra *mapextra
}

// bucket for a go map
type bmap struct {
    tophash [bucketCnt]uint8 // to do research 4.4
}
```

bmap -> |tophash|keys|elems|overflow->bmap(mapextra) -> [overflow|oldOverflow|nextOverflow]|

### init

```go
m := make(map[string]int, 10) // runtime.makeMap
hm := map[string]int{
    "1":2,
    "2":3,
    "3":4,
}
/*
 hm := make(map[string]int, 3)
 hm["1"] = 2
 hm["2"] = 3
 hm["3"] = 4

*/
```

## Sync.Map sync/map.go

```go
type Map struct {
    mu Mutex
    read atomic.Value // readOnly struct
    dirty map[interface{}]*entry
    misses int
}
```

## interface

in golang, a struct implement all method from interface, a struct implements the interface

```golang
type Car interface  {
    Drive()
}

type TraficTool interface  {
    Drive()
}

type Truck struct {
    Model string
}

// truck implements the Car
func (t Truck) Drive() {
}

func main () {
    var c Car = Truck{
        Model: '1',
    }
    fmt.Println(c) // c is iface

    var tttt = (Truck)c // 强转
    truck, ok := c.(Truck) //类型断言
    traficTool, ok := c.(TraficTool) // use iface.tab.fun to compare

    switch c.(type) {
    case TraficTool:
        fmt.Println(1)
    case Car:
        fmt.Println(2)
    }

}

//runtime/runtime2.go
type iface struct {
    tab *itab // interface desc
    data unsafe.Pointer // 万能指针, point to real struct
}

//runtime/runtime2.go
type itab struct {
    inter *interfacetype
    _type *_type
    hash  uint32
    _     [4]byte
    fun   [1]uintptr //the implemented method for the type
}
```

### 类型断言

使用在接口值上的操作,可以将接口值转换为其他类型值， 可以配合 switch 进行类型判断

### 结构体和指针实现接口

|                          | 结构体实现接口(a) | 结构体指针实现接口(b) |
| ------------------------ | ----------------- | --------------------- |
| 结构体初始化变量 (1)     | 通过              | 不通过                |
| 结构体指针初始化变量 (2) | 通过              | 通过                  |

```go

type A interface {
    AAAA()
}

func (t *Truck) AAAA(){}

func main () {
    var c Car = &Truck{} // 2a
    var c1 Car = Truck{} // 1a
    var a A = &Truck{} //2b
    var aa A = Truck{} // NO ERROR 2a
}
```

如果一个结构体实现了一个方法，golang 在编译期时会自动加上该结构体的指针对于该方法的实现, 但是结构体指针实现一个方法， 编译时不会自动添加

```go
type A interface {
    AAAA()
}

type Structure struct {}

func(s Structure) AAAA() {

}
/*
    In compile time, following method will be added
    func (s *Structure) AAAA() {

    }
*/
```

### 空接口

空接口可以承载一切数据, 但它的底层数据不是 iface， 而是 eface empty interface

```go
type eface struct {
    _type *_type
    data unsafe.Pointer
}


func k(interface{}) { // println is implemented this way,

}

k(1)
k("123")
k(Truck{ // go actually will generate a new eface and pass the struct into it

})

```

### nil, eface,

nil 是空，不一定是空指针
buildin/buildin.go 定义了 nil

```golang
var nil Type // type can be a pointer,channel,func,interface, map, slice type, nil is actual the zero value of the above 6 things

type Type int

func main() {
    var a *int
    fmt.Println(a == nil) // true
    var b map[int]int
    fmt.Println(b == nil) // true
    fmt.Println(a == b ) // compile error, nil actual has type
    var c struct{}
    fmt.Println(c == nil) //compile error, nil can never be a struct
}
```

nil 是 6 种类型的零值，每种类型的 nil 是不同的

### struct{}

空结构体是 go 中特殊的类型
空结构体的值不是 nil
空结构体的指针不是 nil，但是都是相同的 zerobase

### interface{}

空接口不一定是 nil

```go
var a interface{} //eface
var b interface{}
fmt.Println(a == nil) // true eface without _type and data is nil
fmt.Println(b == nil) // true
var c *int
fmt.Println(c == nil) // true
a = c
fmt.Println(a == nil) // false eface get updated, _type has value
```

## 内存对齐

在 64 位系统，cpu 寻址只能一次读取 64 位内存，也就是说当一个 struct 的 size 大于 64bit，cpu 得可能访问 2 次，java 里面 long/double 是 64 位，在 32 位 jdk 要完成更改需要两次操作，因此，long 和 double 的 write 操作是非原子性的

### 对齐系数

为方便内存对齐，Go 提供对齐系数

```go
unsafe.Alignof(bool(true))
```

对齐系数的含义是变量的内存地址必须被对齐系数整除
如果对齐系数为 4 变量内存地址必须是 4 的倍数

### 结构体对齐

成员变量对齐， 考虑成员大小和成员的对齐系数
结构体长度填充，考虑自身对齐系数和系统字长

```go
type Demo struct {
    a bool // align 1, size 1
    b string // align 8, size 16
    c int16 // align 2, size 2
}
```

每个成员的偏移量是自身大小与其对齐系数较小值的倍数， 且最终排列时，会按照成员变量的定义顺序进行内存布局 所以 成员变量的位置有可能会影响效率？？？

### 结构体长度填充

结构体通过增加长度，对齐系统字长
结构体的长度是最大成员长度与系统字长较小的整数被
DEMO 的长度为 最大成员 string 16 字节， 系统字长 64 位 8 字节， 整数倍为 4 \* 8 = 32

### 节约结构体空间

调整成员顺序，节约空间

```go
type Demo struct {
    a bool // align 1, size 1
    c int16 // align 2, size 2
    b string // align 8, size 16
}
```

DEMO 的长度为 最大成员 string 16 字节， 系统字长 64 位 8 字节， 整数倍为 3 \* 8 = 32

结构体的对齐系数，是其成员的最大对齐系数

### 空结构体对齐

空结构体单独出现，地址为 zerobase
空结构体出现在结构体末尾，需要补齐字长
