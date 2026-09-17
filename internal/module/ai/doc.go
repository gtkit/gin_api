// Package ai 是 AI 对话模块的根目录说明（本文件不含代码，只承载文档）。
//
// # 它做什么
//
// 把用户的一句提问，连同该会话的近期历史与可选的系统提示词，交给一家大模型
// 厂商，把回复（流式或一次性）交还给客户端，并把这一轮对话原子地写进库。
//
// 底层是 github.com/gtkit/go-llm-provider/v2：国内主流平台都兼容 OpenAI Chat
// Completions 协议，差别只在 BaseURL、密钥与模型名，因此接一家新厂商是配置里
// 多一段，不是代码里多一个分支。
//
// # 目录与分层
//
//	internal/models/ai_conversation.go       ← 会话头（聚合根）
//	internal/models/ai_message.go            ← 消息（聚合内实体）+ 角色枚举
//	internal/repository/aiconversation/      ← 聚合仓储：两张表的读写收口，见其 README
//	internal/module/ai/
//	├── application/
//	│   ├── ports.go      ← ConversationStore / LLMClient / Auditor（使用方定义接口）
//	│   ├── service.go    ← 编排：校验 → 组装上下文 → 调模型 → 落库
//	│   ├── context.go    ← 上下文组装的纯规则（裁剪、过滤、标题生成），无 IO
//	│   └── errors.go     ← 领域错误，transport 按类型映射状态码
//	├── dto/              ← 请求/响应契约（binding tag、SSE 事件体）
//	└── transport/http/   ← handler / router / errmap
//	internal/runtime/module/ai/
//	├── module.go         ← 配置 → provider 集合 → service（未知厂商即启动失败）
//	├── adapters.go       ← SDK ↔ 中立类型翻译、ProviderError → 领域错误
//	└── httpclient.go     ← LLM 专用出站客户端（Timeout 留零，预算走 ctx）
//	internal/platform/config/module_ai.go    ← modules.ai 强类型配置子树
//
// application 不 import 任何 LLM SDK、gorm 或 gin。这条边界的价值在换库时才显现：
// 换掉 SDK 只改 adapters.go，业务编排一行不动。
//
// # 一轮对话的路径
//
//	handler 绑定请求 → service.prepare（校验 + 归属校验 + 取历史 + 组装上下文）
//	  → LLMClient.Chat / Stream（**事务之外**）
//	  → ConversationStore.SaveTurn（一次事务：会话 + 两条消息 + 汇总列）
//	  → 组装响应 / done 事件
//
// 三条不变量，每条都有对应的测试盯着：
//
//  1. **外部调用绝不进事务**。把 LLM 调用包进事务是长事务与连接池耗尽的典型
//     来源——一次上游卡顿会按住一个数据库连接直到超时。
//  2. **会话与首轮对话一起创建**。先建会话再调模型的话，一次上游失败会留下一个
//     空会话：用户在列表里看到一条什么都没有的记录，既不能续聊也说不清是什么。
//  3. **断流也要落库**。用户已经在屏幕上看到的那些字必须进历史，否则这是这个功能
//     最容易被投诉也最难解释的一种表现。落库用 context.WithoutCancel + 独立预算——
//     那一刻请求 ctx 通常已经取消，用它写库必然失败。
//
// # SSE 的几条约束
//
//   - 起流是**懒的**：第一个 delta（或心跳）之前不写任何字节，这样"提问为空"
//     "会话不存在""密钥被拒"这些在首字节之前就能判定的失败仍能带上正确的状态码。
//     响应头一旦发出就再也改不了状态码。
//   - 流式路由**不挂任何超时中间件**：middleware.Timeout 会掐断正常生成中的长流；
//     middleware.StreamTimeout 与 gtkit/ssex 的连接级 deadline 互相覆盖。整体预算
//     由 handler 内的 context.WithTimeout(stream_total_timeout) 给出。
//   - 心跳 goroutine 必须在 handler 返回**之前**退出：gin 会把 ResponseWriter 归还
//     对象池，晚一步写出去的帧会落到另一个请求的响应上。
//   - 认证中间件禁止 SSE 请求用 query 传令牌，因此前端不能用原生 EventSource，
//     要用 fetch + Authorization 头读流。
//
// # 配置与装配
//
// modules.ai.providers 里**填了 api_key 的条目才启用**；一家都没启用时整个模块
// 不装配、路由不注册，启动期打一条 Warn。这是有意的：注册一组永远报 503 的端点
// 比端点不存在更难排查——前者看起来是上游挂了，后者一眼就是没配。
//
// 厂商名的合法性不在配置包校验（那里不该 import LLM SDK），由 runtime 装配层判定，
// 不认识的名字直接让启动失败——静默跳过会让拼错的名字表现为"配了却不生效"。
//
// # 刻意不做的
//
// Tool Use / Function Calling、Embedding 与 RAG、多模态输入、结构化输出：库都支持
// （RunToolLoop、Embedder、ContentPart、ResponseFormat），本模块只做纯文本对话。
// LLMClient port 与 ChatCall 留出了扩展位，加这些能力不需要动编排骨架。
//
// 上下文压缩（把久远的历史摘要成一段）同样没做：它需要额外的模型调用，而"什么时候
// 该压缩、压缩到多少"是业务判断。当前按条数硬裁剪，规则写在 context.go 里。
package ai
