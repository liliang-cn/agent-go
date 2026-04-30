# 我家桌面上养了一只哈士奇，它现在听 Claude 的话——一篇用桌宠讲明白 MCP 的文章

桌面右下角，一只哈士奇正坐着。它不动，不叫，眼神涣散，一副"我不知道我在哪"的标准二哈表情。

我打开 Claude，跟它说："让那只狗跑起来。"

哈士奇蹿了出去。

我又说："停下，看右边。"

它停下来，扭头，看右边。

我说："演一下吃东西的动作。"

它开始嘎嘣嘎嘣咬空气。

我没有写一行胶水代码。我没有给 Claude 装"哈士奇插件"。Claude 也根本不知道我桌面上是哈士奇还是柴犬还是一只穿西装的猫。它只知道有一个叫 `husky-pet` 的工具能听它的话——这个工具叫做 **MCP server**。

这就是这篇文章想说的事：MCP 是什么，为什么它让你能用一句"让狗跑起来"指挥一个跨进程、跨语言、跨协议的栈，最后干净利落地拉动 Three.js 里的骨骼动画。

我们用一只名叫 husky-pet 的开源桌宠做例子。源码在 `/Users/liliang/Things/AI/projects/pets/husky-pet`，技术栈是 Tauri (Rust) + Three.js + Rust 写的 MCP server。麻雀虽小，五脏俱全，且这只麻雀以前还坑过它的主人——这部分等下也讲。

---

## 一、先说人话：MCP 到底是什么

如果你最近半年关注 AI，听过一万遍"MCP"这三个字母，但每次别人解释你都越听越懵——这不是你的问题，是大家解释方式都太"协议向"了。

让我用一句最朴素的话讲完：

> **MCP 就是给大模型订了一份外卖菜单。**

模型不用知道厨师是谁，不用知道菜怎么做。它只看菜单上有什么菜（"工具"），用什么参数点（JSON），点完之后等一份盒饭（结果）回来。

具体一点：

- **MCP Client**：那个想点菜的人，比如 Claude Desktop、Cursor、Codex、AgentGo 这些。
- **MCP Server**：那家"接单的店"。它对外贴一张菜单（"可以调用的工具列表"），客户端发请求过来，它干完活把结果送回去。
- **协议本身**：客户端和服务端用什么形式互发消息——通常是 stdio 或者 HTTP，里面包 JSON-RPC。

哈士奇这个例子里，MCP 菜单上一共九道菜：

```text
show          → 让狗出现
hide          → 让狗消失
toggle_visibility → 让狗忽明忽暗（薛定谔的狗）
start_walking → 让狗开始溜达
stop_walking  → 让狗停下
reset_pose    → 让狗"立正"（重置姿态）
play_action   → 让狗演一个具体动作，例如 "Eating", "Attack", "Gallop_Jump"
get_state     → 问一下狗现在在干嘛
list_actions  → 看看这只狗一共会演哪些动作
```

注意，这九道菜不是 Claude 自带的。Claude 出厂的时候不知道哈士奇这个东西。是 husky-pet 项目自己写了一个 MCP server（Rust 写的，280 行不到），把这张菜单贴出去。任何懂 MCP 协议的客户端连上来，自动就能看到这张菜单，自动就会点。

这就是 MCP 最朴素的价值：**一次贴菜单，所有 AI 都能点**。

---

## 二、Husky 这只狗，它的全栈在哪

桌宠看起来人畜无害，但拆开看其实有四层。我们从底往上数：

```text
┌────────────────────────────────┐
│  MCP Client (Claude / Codex)    │  ← 它说："让狗跑起来"
└──────────────┬─────────────────┘
               │ stdio (JSON-RPC)
               ▼
┌────────────────────────────────┐
│  mcp-pet-server (Rust)          │  ← 一个薄薄的转发层
└──────────────┬─────────────────┘
               │ HTTP (POST /commands)
               ▼
┌────────────────────────────────┐
│  Tauri app (Rust 进程)          │  ← 真·桌宠 app
│  127.0.0.1:47831                │
└──────────────┬─────────────────┘
               │ window.eval(...)
               ▼
┌────────────────────────────────┐
│  Three.js 前端 (浏览器引擎)     │  ← 真正让狗动起来的人
│  window.__HUSKY_PET_CONTROL__   │
└────────────────────────────────┘
```

四层。乍一看像在骗经费，但每一层都有它自己的职责，删掉一层都不行。我们一层一层看。

### 第一层：MCP Client——发号施令的那个

你用什么客户端都行。它们都说同一种 MCP 普通话。客户端的工作很简单：

1. 启动的时候 `initialize`，握个手
2. `tools/list`，把菜单拿过来
3. 真要点菜的时候 `tools/call`，附上参数
4. 等结果

模型那边的事情更朴素——它在系统提示里能看到工具描述，根据用户说的话决定要不要调、调哪个、传什么参数。"让那只狗跑起来"对它而言只是 `start_walking` 没参数。

### 第二层：mcp-pet-server——只做转发的"前台小哥"

`/Users/liliang/Things/AI/projects/pets/husky-pet/mcp-server/src/main.rs` 这一整个文件，本质上就一句话：

> 我从 stdio 收到 MCP 工具调用，我把它转成一个 HTTP POST 发给本机 47831 端口，然后把结果返回。

代码长这样（精简过）：

```rust
fn start_walking(&self) -> Result<String, McpError> {
    self.send_http_command(&PetControlCommand::StartWalking)?;
    Ok("Desktop pet started walking.".to_string())
}
```

它不存状态、不维护队列、不模拟 app 行为。它就是一根管子。MCP 进来，HTTP 出去。

为什么要这一层？因为 Claude 不会说 HTTP `POST /commands`，Tauri app 也不会说 stdio 上的 JSON-RPC。中间得有个"懂双语的"。

### 第三层：Tauri app——拿着遥控器的人

这是真正的桌宠 app，Rust 里跑了一个本地 HTTP server，监听 `127.0.0.1:47831`，对外暴露：

```text
GET  /health      → 我活着吗？
GET  /state       → 现在狗在干嘛？
POST /state       → 把狗的状态强行改一下
POST /commands    → 让狗做点什么
```

注意一个关键点：**Tauri app 才是单一事实来源**（single source of truth）。狗当前是不是在走、当前在演什么动作、有哪些动作可演，这些数据**只**从 app 出。MCP server 不缓存，不重建，不猜测。

这一点后面会再讲，因为这是"重写架构"的关键。

### 第四层：Three.js 页面——真正干活的那位

哈士奇是个 GLB 模型，在 Three.js 里跑骨骼动画。Tauri app 把 webview 嵌进了一个无边框透明窗口里，所以你看到的"桌面上的狗"其实是一个浏览器窗口，里面装着一个 3D 场景。

`src/main.js` 暴露了一个全局函数 `window.__HUSKY_PET_CONTROL__`。Tauri 收到 `POST /commands` 后，会直接 `webview.eval("window.__HUSKY_PET_CONTROL__('start_walking')")`。前端拿到指令，切换骨骼动画 clip，狗就跑起来了。

到这一步，事情终于落到了"真的让 3D 模型动起来"这一层。

---

## 三、一句"让狗跑起来"的完整旅程

把上面四层串起来。我对 Claude 说：

> 让那只哈士奇跑起来

接下来的事情——**全程不到一秒，不到一行胶水代码**：

1. **Claude** 看到工具列表里有 `start_walking`，决定调用，参数 `{}`。
2. **MCP Client** 通过 stdio 给 `mcp-pet-server` 发 `tools/call { name: "start_walking" }`。
3. **mcp-pet-server** 把它翻译成 `POST http://127.0.0.1:47831/commands`，body 是 `{"kind": "start_walking"}`。
4. **Tauri app** 收到 HTTP 请求，识别这是个动作类命令，转成一段 JS：`window.__HUSKY_PET_CONTROL__('start_walking')`，喂给 webview。
5. **Three.js** 切换到 `Gallop` 动画 clip，骨骼系统插值，模型抬腿，开始跑。
6. **Tauri app** 把成功响应顺着 HTTP 回给 `mcp-pet-server`。
7. **mcp-pet-server** 在 stdio 上回 `"Desktop pet started walking."`。
8. **Claude** 看到工具返回成功，告诉用户："狗已经跑起来了。"

四层、两种协议、两种语言、一段 eval 注入。从用户视角看，就是一句话之后，狗动了。

这就是 MCP 真正的魔法所在：**它把"模型给指令"和"系统执行指令"之间所有的脏活脏话，包成了一个标准接口**。Claude 不用懂 Tauri，Tauri 不用懂 Claude，桥靠的是一份共同的菜单。

---

## 四、坑：作者一开始把这事干岔了

如果文章到这就结束，看起来太顺了，反而像 PPT。但 husky-pet 真实的开发故事不是这样——作者老老实实把这个事第一次干岔了，留了一份非常宝贵的复盘文档（`docs/mcp-control-architecture.md`）。我们看看翻车现场。

### 翻车一：MCP 和内部桥揉在了一起

最早的设计里，三件事被塞进了同一个 server：

- MCP 协议层（接外面）
- 接前端的"内部桥"（指挥页面）
- 命令队列（自己存一份）

结果就是经典的"看起来全是绿勾勾，但桌面上啥也没动"。MCP 工具调用返回 `success`，狗站在那里像个雕塑，眼神依旧涣散。

调试这种东西最折磨：**到底是协议没通，还是 app 没收到，还是收到了但没传到前端？** 三个失败点叠在一个进程里，日志靠肉眼都看不清。

后来作者把它拆开了：

> **app 自己暴露本地 HTTP 控制口；MCP server 只做 MCP → HTTP 转发。**

这一句话，是这个项目最值钱的一行结论。

### 翻车二：前端还在轮询一条已经废掉的命令队列

旧前端代码里有 `dequeue_control_command` 和 `startControlPolling()`——一种"我每 200ms 问一次 Rust 有没有命令要我做"的轮询模型。

但 Rust 那边后来的改动里，命令路径已经不走队列了——直接 eval 注入。这意味着：

- MCP server 把命令送到了 app
- app 也确实收到了 HTTP
- 但前端 JS 在死磕一条早就没人写入的队列

这种 bug 长得就像"协议层全成功，物理层啥也没动"。开发者可能盯着日志看半天看不出来，因为没有任何错误。

修复也很朴素：**前端别再轮询那个不存在的队列了，直接暴露一个 `window.__HUSKY_PET_CONTROL__`，让 Rust 喂指令进来就完事了。**

### 翻车三：`~/.local/bin` 装的不是稳定的二进制

中间一段时间，`~/.local/bin/mcp-pet-server` 是个 zsh wrapper 脚本，反复在 wrapper 和真·二进制之间切换。问题是 stdio 排查的时候，**你不知道客户端实际启的是哪一层**。

最后的纪律：

- 客户端配的是 release 二进制路径。
- 不是 `cargo run`，不是 wrapper，不是 dev 模式。
- 一切验证以 release 为准。

这听起来像废话，但你做过 stdio MCP 调试就知道，路径里多套一层 wrapper，一个简单的 "stdout 被吃掉了" 就能让你怀疑人生。

---

## 五、复盘的真正洞见：MCP 的边界纪律

这次复盘真正值钱的不是"我们后来怎么修好的"，而是**它指出了 MCP 设计里最容易被忽视的一条纪律**——

> **协议边界不要混。一根管子只走一种协议。**

具体到 husky-pet：

- MCP 这种协议，只存在于 **客户端 ↔ mcp-pet-server** 这一段。
- HTTP 这种协议，只存在于 **mcp-pet-server ↔ Tauri app** 这一段。
- DOM eval 这种"协议"，只存在于 **Tauri ↔ webview** 这一段。

**它们相邻，但不重叠。**

混在一起的代价我们刚刚见识过——出 bug 你都不知道自己在哪一层挂了。拆开之后好处立竿见影：

- `curl 127.0.0.1:47831/state` 可以单独验证 app 控制口是不是活的。
- 走 stdio 给 `mcp-pet-server` 发一条 `tools/list` 可以单独验证 MCP 层是不是通的。
- 调 `tools/call(start_walking)` 之后再读 `/state.walking == true`，可以单独验证整条链是不是通的。

每一层都能独立 mock、独立测试、独立替换。哪天你不想用 Tauri 了，要换成 Electron？没问题，只要新 app 也开 47831 端口、也接 `POST /commands` 就行，MCP server 一个字都不用改。哪天不想用 Rust MCP 库，想用 Python？也没问题，菜单内容不变，客户端啥都看不出区别。

**这就是 MCP 想给你的——一个稳定的、可以替换底层实现的标准接口。**

---

## 六、那为什么不是 REST？为什么不是 gRPC？

每次讲 MCP 都有人问这个。问得好。

REST 是给"人和后端"或"后端和后端"之间用的，它假设双方都看得懂 URL 路径、HTTP method、status code 这一整套。让一个大模型理解 `PUT /v1/pets/123/walk` 比让它理解 `start_walking` 难得多——前者要它脑补语义，后者就是个动作名。

gRPC 是给系统间高性能调用用的，强类型、二进制、IDL 一套。但模型不擅长读 .proto 文件。

MCP 实际上就一份非常薄的"工具菜单 + JSON 参数"的协议——它有意做得简单，因为它知道**它的客户端是大模型**，不是工程师。模型擅长的是看自然语言描述、生成 JSON 参数。MCP 就是奔着这一点设计的。

副作用是：MCP 也特别容易写。你不需要懂特别多东西就能给自己的项目搭一个 MCP server。`husky-pet` 那个 server 的核心逻辑，去掉错误处理也就 50 行 Rust。

---

## 七、为什么这只狗值得讲

讲了这么多，可能你会问："为啥要用一只哈士奇当例子？拿数据库不行吗？拿 GitHub API 不行吗？"

不行。或者说——可以，但没意思。

数据库和 GitHub API 那种例子讲 MCP 太抽象，因为它们本来就是"网络上的服务"。让模型调一个网络服务，听起来不够新奇——它和"调任何一个 REST API"区别不大。

但桌宠不一样。桌宠是**你电脑桌面上的一个原生进程**。它有 GUI，有 OpenGL 渲染，有窗口管理。它属于"那个世界"——一个传统 AI 工具栈完全够不到的世界。

让 Claude 控制一只桌面上的狗，意味着**模型第一次能跨过浏览器，跨过云端 API，伸手到本机的图形栈里去**。这是个非常具体的、能让人意识到"哦，原来 MCP 真的能干这个"的瞬间。

而且坦白讲，看着一只 3D 哈士奇被 LLM 远程操控、奔跑、跳跃、咬空气，这件事本身就足够让人嘴角失守。技术内容讲清楚是一回事，让你**记得住**又是另一回事。一只跑来跑去的二哈，比任何 ER 图都更难忘。

---

## 八、想自己试试？三步走

1. **跑起来桌宠 app**：克隆 husky-pet，进 `src-tauri`，`cargo tauri build`，把 release 包扔到 Applications。
2. **跑起来 MCP server**：进 `mcp-server`，`cargo build --release`，把二进制扔到 `~/.local/bin/mcp-pet-server`。
3. **配 MCP 客户端**：打开你用的客户端（Claude Desktop / Cursor / Codex / AgentGo 都行），加一段配置：

```json
{
  "mcpServers": {
    "husky-pet": {
      "command": "/Users/<you>/.local/bin/mcp-pet-server",
      "args": []
    }
  }
}
```

启动之后跟模型说：

> 我桌面上有一只哈士奇，让它跑起来。

如果一切顺利，那只二哈就会迈开腿，开始它的桌面巡逻。如果你想恶搞，让它演 `Eating` 动作，它会嘎吱嘎吱咬一段空气；让它演 `Gallop_Jump`，它会原地跳一下。

**它现在听 LLM 的话了。** 这是一句技术陈述，不是修辞。

---

## 九、最后

MCP 不是什么宏大的协议，它甚至有点"太朴素"——朴素到第一次接触你会觉得"就这？"。但正是因为朴素，它能在不同模型、不同语言、不同栈之间扎下根来。

它的价值不在于复杂，而在于**让"模型 → 工具"这件事终于有了一个共同的语法**。在这之前，每接一个工具都要写一份私有胶水。现在你写一次 server，所有客户端通用。

哈士奇这个例子告诉我们三件事：

1. **MCP 的接入成本可以非常低**——50 行 Rust 就够搞定一个能跑的 server。
2. **协议边界要拆得干净**——MCP 在外面，HTTP 在中间，eval 在最里面，一根管子一种协议。
3. **桌宠这种看起来玩具的项目，反而最适合演示"AI 真的能伸手到本地系统"**——你看见狗跑了，你就懂了。

下次再有人问你"MCP 是什么"，你可以指着屏幕右下角那只忽然站起来开始溜达的狗说：

> "你看，它现在听 Claude 的话了。这就是 MCP。"

然后你就赢了。

---

本机仓库路径：`/Users/liliang/Things/AI/projects/pets/husky-pet`

附：MCP 控制链路复盘原文也在仓库里，路径 `docs/mcp-control-architecture.md`。看完那篇，你会比看完任何官方文档都更懂"MCP 到底怎么用才不踩坑"。
