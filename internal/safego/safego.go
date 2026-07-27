package safego

import (
	"log"
	"runtime/debug"
)

// Recover 是 goroutine 入口与长期循环每轮工作的恢复边界。Go 中任意 goroutine 的
// 未恢复 panic 都会终止整个进程，所以每个处理外部输入的 goroutine 都必须调用它。
//
// 只能写成 defer safego.Recover("scope") 这种直接 defer 的形式：recover 仅在被
// 直接 defer 的函数内调用才生效，再包一层闭包会让它永远返回 nil。
func Recover(scope string) {
	if r := recover(); r != nil {
		log.Printf("panic recovered in %s: %v\n%s", scope, r, debug.Stack())
	}
}
