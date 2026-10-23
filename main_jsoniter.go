//go:build jsoniter

package main

import "github.com/gtkit/json/v2"

// jsoniter 构建保留已有的 PHP 兼容解码，在任何并发解析前完成注册。
func init() {
	json.RegisterFuzzyDecoders()
}
