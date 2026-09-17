//go:build windows

package cmd

// syncDir 在 Windows 上是 no-op：该平台没有"刷目录元数据"这个操作。
//
// FlushFileBuffers 不接受目录句柄，恒返回 ERROR_ACCESS_DENIED。沿用 Unix 写法
// 会让 jwt generate 在 Windows 上必然失败——而密钥轮换是 SCAFFOLD.md 登记的
// 预置能力，附带操作手册，不能只在一部分平台上存在。
//
// no-op 并未降低保证，因为没有更强的动作可做：NTFS 是日志文件系统，目录项变更
// 走日志，崩溃后由日志重放保证一致性，用户态拿不到比这更细的控制。
//
// 只有目录这一处分流。writeExclusive 里对普通文件句柄的 f.Sync() 两个平台都保留——
// 那是 Windows 完全支持的操作，也是私钥内容真正落盘的那一步。
//
// 刻意不写成"调用后吞掉 EACCES"：那样会在 Unix 上也吞掉真实的权限故障，
// 把一个平台事实变成一条静默的错误路径。
func syncDir(string) error {
	return nil
}
