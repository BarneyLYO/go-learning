# Note

## `go build -n`

## build process

1. 词法分析
   源代码到 token
2. 句法分析
   token 处理到语法树 ast
3. 语义分析
   类型检查，代码检查，类型推断，函数内联，逃逸分析
4. 中间码生成
   SSA 代码（中间代码，平台无关）export GOSSAFUNC=main 或者 $env:GOSSAFUNC="main"
5. 代码优化（每一步都有一些）
6. 机器码生成
   ssa => Plan9 汇编（平台相关） => 机器码.a `go build -gcflags -S main`
7. 链接
   各个包链接，生成.exe

1,2,3 编译前端
4,5,6 编译后端

## how go runs

1. runtime/rt0_xxx.s 汇编入口
2. \_rt0_xxxx_xxxx, 跳转到 \_rt0_xxx
3. 存储 argc，argv 寄存器中，跳转到 runtime.rt0_go(SB)
4. 初始化栈，初始化 g0 coroutine， 不归调度器管
5. CALL runtime.check(SB) 已经是 go 方法了 运行时检测 g0
6. copy argc and argv to go g0
7. CALL runtime.args(SB) g0
8. CALL runtime.osinit(SB) g0
9. CALL runtime.schedinit(SB) g0
   - 全局栈空间内存分配
   - 堆内存空间的初始化
   - 初始化当前系统线程
   - 算法初始化 map， hash
   - 加载命令行参数到 os.Args
   - 加载操作系统环境变量
   - 垃圾回收器的参数初始化
   - 设置 process 数量
10. MOVQ runtime.mainPC 获取 runtime.main 函数地址 g0
11. CALL runtime.newprod(SB) 创建一个新的协程(主协程，第二个协程)准备调用 runtime.main，放入 schedler 等待调度 g0
12. CALL runtime.mstart(SB) 调度器启动一个 M，新建的协程就会真正的启动起来，M 用来调度主协程 g0
13. runtime.main -> doInit(&runtime_initstack) -> gcenable() -> 执行用户包依赖的 init 方法 -> .... 调用 main_main. g1 主协程

## go dont have inheritance, go only have compose

用语法糖达成类似继承效果

```go
type People struct {

}

type Man struct {
    People // 匿名字段
}

func (p People) walk() {}

func main() {
    m:= Man{}
    m.walk() // actually is m.People.walk()
    fmt.Println("aaaaa")
}
```

## go interface

行为类似的 struct， 不用 struct 显示实现，ducktyping

```go
type Alive interface {
    walk()
}

type People struct {
    name string
}

func (p *People) walk() { // implement the People
}
```

## Go Modules

1 package is a source code of a project => go mod: you need which project's which version
`go get github.com/jeffail/tunny` - latest
`go get github.com/jeffail/tunny@0.1.3` - latest
vender(local cache)

## go proxy

go env -w GOPROXY=https://goproxy.cn, direct

`replace xxx => xxx` replace the remote repo to the local replacement
