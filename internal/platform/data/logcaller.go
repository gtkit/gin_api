package dao

import (
	"reflect"
	"runtime"
	"strconv"
	"strings"
)

// Match package names rather than paths so trimpath and local replaces behave alike.
const gormHost = "gorm.io/"

// Only the project package path varies when the scaffold's module is renamed.
var adapterPackage = reflect.TypeFor[gormLogger]().PkgPath()

// callerSource skips this adapter, GORM, generated query and runtime frames.
func callerSource() string {
	const (
		maxDepth = 32
		// 跳过 runtime.Callers 自身与本函数两帧；zlogger 的方法帧由前缀判定过滤，不依赖具体深度。
		skipSelf = 2
	)
	var pcs [maxDepth]uintptr
	n := runtime.Callers(skipSelf, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		if frame.PC != 0 && !internalFrame(frame) {
			return frame.File + ":" + strconv.Itoa(frame.Line)
		}
		if !more {
			return ""
		}
	}
}

func internalFrame(frame runtime.Frame) bool {
	switch {
	case strings.HasSuffix(frame.File, ".gen.go"):
		return true
	case strings.HasSuffix(frame.File, "_test.go"):
		return false
	}
	fn := frame.Function
	return strings.HasPrefix(fn, gormHost) ||
		strings.HasPrefix(fn, adapterPackage+".") ||

		strings.HasPrefix(fn, "runtime.")
}
