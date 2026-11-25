package syncmap

import (
	"fmt"
	"sync"
)

// 定义一个泛型的 SyncMap，支持任意类型的键和值.
type SyncMap[K comparable, V any] struct {
	m sync.Map
}

// 设置键值对.
func (sm *SyncMap[K, V]) Store(key K, value V) {
	sm.m.Store(key, value)
}

// 获取键对应的值.
func (sm *SyncMap[K, V]) Load(key K) (V, bool) {
	val, ok := sm.m.Load(key)
	if ok {
		return val.(V), true
	}
	var zeroValue V
	return zeroValue, false
}

// 删除键值对.
func (sm *SyncMap[K, V]) Delete(key K) {
	sm.m.Delete(key)
}

// 遍历所有键值对.
func (sm *SyncMap[K, V]) Range(f func(key K, value V) bool) {
	sm.m.Range(func(key, value any) bool {
		return f(key.(K), value.(V))
	})
}

func example() {
	// 使用 SyncMap 存储字符串 -> int 类型的键值对
	sm1 := &SyncMap[string, int]{}
	sm1.Store("apple", 5)
	sm1.Store("banana", 10)

	// 加载并打印值
	if value, ok := sm1.Load("apple"); ok {
		fmt.Println("apple:", value)
	}
	if value, ok := sm1.Load("banana"); ok {
		fmt.Println("banana:", value)
	}

	// 使用 SyncMap 存储 int -> string 类型的键值对
	sm2 := &SyncMap[int, string]{}
	sm2.Store(1, "one")
	sm2.Store(2, "two")

	// 遍历所有键值对
	sm2.Range(func(key int, value string) bool {
		fmt.Printf("%d: %s\n", key, value)
		return true
	})
}
