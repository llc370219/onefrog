# 持久任务运行时（P1）

OneFrog 的对话任务不再依赖某一次 HTTP 请求或 WebSocket 连接。任务被接受后写入加密任务文件，由后台 `Runner` 在自己的 goroutine 里跑，因此断线或刷新页面不会取消任务。

这里的保证是有边界的，请不要读成“任何情况下都不会丢、都不会重”：

- **不重放**：工具调用先记 `started` 再执行，执行完记 `done`。恢复时 `done`/`failed` 直接复用结果；`started`/`interrupted` 只报告“结果未知，未重复执行”，绝不自动重跑。
- **未知副作用必须核验**：中断时正在执行的变更类调用，其效果可能已发生也可能没有。这类任务不能直接宣称完成：`finish` 会被拒绝，直到有一次**只读检查**（`inspect_services` / `panel overview` / `workspace read` 等明确只读的调用）真正执行成功。第二个变更类调用不算核验。
- **进度会丢一小段**：checkpoint 是周期写入的，进程在两次写入之间崩溃时，最后一段对话上下文可能回退到上一个 checkpoint；已执行的副作用仍由 journal 记录，不会被重放。
- **崩溃窗口**：任务在“写任务文件”和“写对话记录”之间崩溃时，任务文件里的结果才是权威来源；下一次扫描会把缺失的收尾消息补写进对话，且只补一次。

## 组件

- `internal/task`：任务注册表。加密、原子替换（临时文件 + fsync + rename）、大小与条数上限、`Update` 写失败整体回滚。身份、目标与授权选项在创建后不可改。
- `internal/bot/tasks.go`：`Runner`（周期恢复、单飞派发、暂停/取消/引导/审批决策）与 `AcceptTask` 接受流程。
- `internal/bot/taskloop.go`：窗口循环、预算、journal、审批挂起、跨窗口续跑。
- `internal/bot/tasktools.go`：`plan` / `checkpoint` / `wait` / `finish` 四个任务工具与卡片挂起。
- `internal/bot/taskapi.go`、`internal/api/tasks.go`：会话准备与 HTTP 契约。

## 状态机

`queued` →`running`→

- `completed`：只有 `finish`（带验证证据）或无副作用的纯对话才进入。
- `failed` / `budget_exhausted` / `cancelled`：终态。
- `waiting_user`：等卡片回答（`ask_user` / `request_secret`）。
- `waiting_approval`：等审批决策，调用本身没有执行。
- `waiting_job`：等外部作业或子任务，按稀疏退避轮询（`Pending.WakeAt`），受 deadline 约束。
- `paused`：被暂停，或重启时发现未知结果需要核验。

一次窗口的步数用尽（`StepBudget`）不算失败：任务自动进入下一个窗口继续，只有任务级 `MaxSteps` / deadline 用尽才 `budget_exhausted`。

## Journal 语义

每个工具调用以 `(Turn, CallID, ArgsHash)` 为身份记一条 journal：

- `started`：已记录意图，结果未知。
- `done` / `failed`：已完成，恢复时直接复用结果，不重跑。
- `waiting`：审批挂起，调用未执行。
- `interrupted`：服务重启时正在执行，结果未知。恢复时只给模型“执行结果未知，未重复执行”的提示，要求先只读核验；绝不自动重放。

每条 entry 还带两个**类型化**字段，而不是把状态编码进 `Result` 文本（工具输出不可信，可能包含任何标记）：

- `Executed`：只有调用真的执行完成才为 `true`。拒绝、跳过、结果未知都是 `false`。
- `Outcome`：`done` / `failed` / `denied` / `skipped` / `unknown`。

只有 `Executed=true` 的变更类调用才算“任务改过东西”。`Outcome=unknown` 的变更类调用会让任务进入“必须核验”状态，跨窗口、跨重启都保持，直到有一次只读检查真正跑过。

匹配规则：只有 `started` / `interrupted` 允许仅按 CallID 命中（为了拦住重放）；`waiting` 必须 (Turn, CallID, ArgsHash) 完全一致，避免“换了参数沿用旧审批”。

同一批 assistant 调用里，`finish` 或 `wait` 之后的调用会被记录为“未执行”并立即结束这一批，不会在终态或挂起之后继续产生副作用。

`plan` / `checkpoint` / `wait` / `finish` 的状态变更与 journal 条目在**同一次注册表写入**内提交，并在同一次写入里检查任务是否已被暂停/取消：操作员的暂停或取消永远不会被一个已经在飞行中的纯任务工具覆盖。

## 收尾消息（outbox）

任务的收尾消息按“先持久化、再写对话”的顺序处理：

1. 一次注册表写入同时落下最终文本、确定性的 `ResultMessageID`、验证证据、journal 条目和终态。不存在“已经 completed 但结果还没保存”的窗口。
2. 之后才把消息追加进对话。id 由任务 id + turn 派生，因此补写可以判断“这条已经写过”，不会重复。
3. `Run` 的每一轮扫描都会为 completed 但对话里缺消息的任务补写（不只是启动时），临时写失败也会在下一轮补上。
4. 窗口自己的写入和补写扫描共用一把 runner 级写锁，所以两者不会同时读到“消息不存在”而各写一次（`Memory.AddMessage` 不会按 id 去重）。

## 审批与授权

- 审批决策绑定 `Pending.ApprovalID` + `CallID` + `ArgsHash`；`Decide` 在写入前逐项校验，重复的相同决策幂等返回，不同决策报错。`Pending.Turn` 只参与 journal 的 `waiting` 条目匹配（防止后续轮次复用 call id 沿用旧审批），不是 `Decide` 的入参。
- 批准后只执行原调用：一次性 permit 绑定 speaker / 生效服务器 / 工具 / CallID / 参数哈希，且只在同一个窗口内有效。
- 生效服务器用 `effectiveServer` 解析，与共享授权钩子一致，否则指向非选中服务器的调用会被反复要求审批。
- 卡片（`ask_user` / `request_secret`）把真实的消息 id 写进 `Pending.CardMessageID` 和 journal，回答只能针对那张卡片。密钥明文只进保险箱，任务文件里只存确认信息。
- 独立审批模型是**三态**：`parseReview` 接受 `{"decision":"allow|deny|ask"}`（同时兼容旧的 `{"allow":bool}`），`ModeModel` 下 allow 直接放行、deny 直接拒绝、ask（以及调用失败、结果无法解析）一律转到人工审批卡片，绝不静默放行。风险等级只是给决策者的证据，allow + high 仍然是 allow。
- 落盘的审批决策仍是 allow / deny 两种（`ApprovalStatus` 为 `pending` / `allowed` / `denied`）：模型层的 ask 已经转成人工卡片，不需要第三种落盘状态；人工选择“稍后决定”只会让调用保持 `waiting_approval` 挂起（未执行），不会记成拒绝。

## 接受流程（`AcceptTask`）

1. 以 `AcceptancePending` 建任务，状态 `paused`，保存冻结的 goal、授权选项（含已校验的附件 id）、提交来源（`Purpose` / `Hop` / `Untrusted`）与已接受的对话快照。
2. 通过 `AppendGoal` 写用户消息。
3. 清除 `AcceptancePending`，改为 `queued` 并唤醒 runner。

失败即删除任务，不会留下“看起来已接受”的幽灵；进程在 1、3 之间崩溃时，该任务永远不会被派发，`Run` 每轮扫描会把超过 2 分钟的未完成接受标成失败（不只是启动时）。任务 id 只由 owner + `client_request_id` 派生，重试落到同一个任务；同一 id 但目标、会话、选项、附件或来源不同则视为冲突。

接受时还会冻结一个有界生命周期（`taskLifetime`，当前 12 小时），持久化在任务上，重启不会延长。期限到了任务转 `budget_exhausted`，不会永久占着执行位、卡片或外部作业。

## 预算与子任务

`Book.ReserveModelStep` 在注册表锁内原子地为一个模型调用记一步，并沿父链检查可用预算（自己的步数 + 活跃子任务的预留 + 已完成子任务的实际消耗），因此父任务与全部子任务之和不会超过根任务的 `MaxSteps`。窗口交接是确定性的文本，不再额外调用一次模型。

`ask_agent` 在任务里创建的是独立的子任务（自己的会话、checkpoint、journal、审批），父任务以 `waiting_job` 挂起在其上；子任务完成会立即唤醒父任务。父任务暂停/取消会传播到子任务，恢复父任务时，仍挂着未答复卡片或未决审批的子任务会回到对应的等待状态，而不是被直接放行。

## HTTP 契约

- `GET /api/tasks?session_id=` → `{items:[TaskView]}`
- `POST /api/tasks` → 202 + `TaskView`
- `GET /api/tasks/:id`
- `POST /api/tasks/:id/pause|resume|cancel`（`{}`）
- `POST /api/tasks/:id/guidance`（`{content}`）
- `POST /api/tasks/:id/approvals/:approvalID`（`{allow, reason?}`）

`TaskView` 只暴露 id、bot/session/project、goal、plan、progress、next、status、步数、时间、deadline、result、last_error、待处理摘要和安全的审批元数据；不下发 checkpoint、messages、options JSON、journal 原始参数或任何密钥。

### 老入口（`POST /api/chat`、`/ws/chat`）

两个老入口在配置了 runner 时都只是**共享提交路径**（`Agent.SubmitTask`）的薄传输层，自身不接受任务、不复制身份规则：

- `POST /api/chat`：带 `client_request_id` 提交任务，然后按 100ms 轮询 `WaitTask`。任务进入等待态或终态即返回；只有 `completed` 且结果非空才返回旧的 `Result` 形状。`failed` / `cancelled` / `budget_exhausted`、以及等待审批/等待卡片/等待子任务的情况返回 202 + `TaskView`，绝不把失败包装成成功，也不从会话里猜一个答案。请求 context 到期（客户端断开）同样返回 202，任务继续跑。
- `/ws/chat`：同样提交任务，然后用连接 context 观察。正常结束发 `assistant` + `done`；等待态发 `task`，并把挂起的审批以 `approval_request`（带 `id` 和 `task_id`）下发；连接断开时发 `detached` 并**保留任务**，不是取消任务。用同一个 `client_request_id` 重连会落到同一个任务，不会重复执行。
- 审批决策一律走 `Runner.Decide`：HTTP 用 `POST /api/tasks/:id/approvals/:approvalID`，WebSocket 的 `{"type":"approval","id":…,"task_id":…,"allow":…}` 也走同一个方法。**没有 `task_id` 的 socket 审批会被拒绝**，不会去猜“唯一挂起的那个任务”。决策持久化在任务上，所以断开重连后仍然可以重新决定同一个审批。
- 卡片回答（`choice_answer` / `secret_answer`）在两个老入口里都复用 `ResumeTaskCard`，与任务面板是同一条身份校验路径（精确匹配 `Pending.CardMessageID`）。
- 没有 runner 时（测试或嵌入式构建）才回退到旧的 `ReplyWithOptions` + 通道审批；这条回退路径不持久，断线即丢。

## 已知限制与后续

- **核验门是保守的，不是严格证明**：它只判断“是否有一次明确只读的工具调用成功执行”，不校验返回内容是否真的说明了状态。`run_command` 不参与，因为 shell 命令的只读性只能靠参数推断，证据强度不够。
- **`waiting_job` 没有真实外部作业探针**：目前由 `wait` 工具与子任务共用挂起通道，按稀疏退避轮询，受 deadline 约束。
- **进度可能回退一段**：checkpoint 是周期写入，不是每次模型调用后都写。
- **终态判定依赖模型自述**：`completed` 需要 `finish` 带核验证据，但“证据是否真的证明了目标”仍由模型判断，运行时只保证“有只读调用执行过”。
- **WebSocket 不中继逐步执行过程**：durable 任务没有按连接绑定的步骤流，`/ws/chat` 只发状态、审批卡片和最终答案，不重放 `say` / 工具步骤。任务面板是观察进度的入口。

子任务不继承父任务的审批结果，但继承父任务的授权来源与预算：`delegation.go` 在创建子任务时逐字段复制 `Purpose` / `Hop` / `Untrusted` 和父任务剩余的 `MaxSteps`（`StepBudget` 取窗口剩余量，`MaxSteps` 取子树剩余额度），子任务自己的审批、journal 与卡片独立。`Untrusted` 因此在委派链上只增不减，不会因为换了一个 Bot 就被洗白。

## 原生本地工作区（local workspace）

除了 SSH Git 工作区，OneFrog 还支持**原生本地工作区**：任务直接在运行 OneFrog
的服务器上、在操作员明确允许的项目目录内读、写、搜索、编辑文件并执行命令。

- **允许列表是 ENV-only 操作员策略**：`OF_LOCAL_WORKSPACE_ROOTS`
  （冒号分隔的绝对路径；为空即整个功能关闭）和 `OF_LOCAL_COMMAND_EXECUTION=1`
  （单独的命令执行开关）。它们在启动时读取一次，运行期不可更改：
  `POST /api/settings` 不携带这些字段，模型和任务也不能扩大范围。配置错误
  （相对路径、`/`）直接导致启动失败，而不是悄悄缩小允许列表。
- **精确目标作用域**：允许列表只做授权判断；执行永远使用请求的确切根目录。
  绑定到 `/allowed/project` 的任务看不到也改不了 `/allowed` 下的兄弟项目。
- **路径防护用 os.Root**：所有文件操作通过 `os.Root`（打开目录句柄）完成，
  逐个分量解析，符号链接即使在校验后被换入也无法把操作重定向到根目录之外
  （纯 pre/post `EvalSymlinks` 做不到这一点）。`.git`、`.env*`、`master.key`、
  `id_rsa`、`.ssh`、`*.pem` 等敏感名字对直接路径和遍历路径统一拒绝；
  `.env.example` 等示例配置是合法文件。
- **edit 带 revision，write 不再裸覆盖**：本地 revision 是文件内容的
  SHA-256。`edit` 校验 revision 一致且原片段唯一；`write`/编辑器保存对已有
  文件要求当前 revision，新文件用 `O_EXCL` 创建，写后以临时文件+原子 rename
  落盘并加进程内锁，并发保存不会互相覆盖。
- **命令执行是显式开关**：`run` / `job_start` 需要 `command_execution=true`。
  命令以 OneFrog 进程的操作系统账号权限运行，**这不是沙箱**：cwd 限制只约束
  路径解析，不约束权限。子进程环境被清洗（只保留 PATH/HOME/TERM/LANG），
  避免泄漏主密钥等进程环境。每次执行仍走共享审批钩子（独立审批模型可见、
  可拦截），审批卡片会明确写明"以 OneFrog 进程的系统账号权限执行（不是沙箱）"。
- **Docker 部署的真相**：容器内 `local` 指的是容器内的文件系统和容器进程的
  权限。要操作宿主机项目，必须由操作员显式挂载项目目录（最小权限，不要挂载
  Docker socket 或宿主机根目录）——面板不会"魔法地"访问宿主。
- **工具契约**：`workspace` 工具的 `action` 增加 `search`；`WorkspaceTarget`
  增加 `kind:"local"`（`server_id:"local"`）。`GET /api/workspace/capabilities`
  返回 `{local:{enabled,roots,execution_environment,command_execution}}`。

## 后台作业（job_start / job_status / job_cancel / job_wait）

本地工作区里的长命令（构建、测试、迁移）可以作为**真实后台进程**运行：

- `job_start` 启动进程（审批后），返回 `job_id` 和 pid；输出按字节上限截断
  保存，进程有 60 分钟硬超时；超时/取消杀整个进程组。作业属于启动它的
  （所有者，任务）：别的任务查不到也取消不了。并发上限按"运行中"的作业计，
  结束的作业保留 30 分钟供查询后清理，连续运行超过 32 个作业不会卡死。
- `job_status` 只读查询状态/输出/退出码。`job_cancel` 停止进程（审批）。
- `job_wait` 把任务挂起在 `waiting_job`，Runner 按 `Pending.WakeAt` 稀疏轮询
  **真实的**作业状态：作业结束后把真实退出码和输出尾部作为工具结果回灌给
  模型，模型据此修复失败再重试。运行中的作业只会继续等待，不会编造等待结果。
- **重启语义**：作业表是进程内的。服务重启后作业不可查询，恢复的任务收到
  "作业结果未知，未重放"的结果，必须先用只读方式核验工作区现状——绝不会
  重放一个不确定的变更命令。

## 任务卡片活动（TaskView.activities）

`TaskView` 新增 `model_label`、`workspace_label`、`started_at`、`finished_at`
和 `activities`（最多 40 条，最新的在最后）。每条活动只含工具名、安全摘要
（动作 + 目标，如 `workspace edit src/main.go`，绝无原始参数/输出/密钥）、
状态（running/completed/failed/interrupted）和时间。活动来自运行内存并在缺
失时由持久 journal 重建，刷新页面后任务卡片仍然连续。

## 计划进度（plan / plan_progress / TaskView.plan_completed）

- `plan` 保存步骤数组；`plan_progress {completed}` 在**实际做完并通过检查**
  之后记录已完成的前缀步数。计数持久化在任务上（`plan_completed`），跨窗口、
  跨重启保留；计划内容变化时归零（重发相同计划则保留），且永远在
  `0..len(plan)` 范围内校验。
- 进度从不从轮次数、工具调用数、预算或时间推断。finish 只在任务真正完成
  （验证证据在最后一次变更之后）时把计划标记为全部完成。
- `TaskView.plan_completed` 是 API 的唯一进度数据源；没有计划时前端显示
  「尚未生成计划」，不显示假进度。

## 验证证据（finish 门槛）

finish 不再只看文字：任务发生过变更时，必须存在一条**在最后一次已执行变更
之后**的只读观察（workspace 的 read/list/search/status/diff、inspect_services、
job_status 等），以 journal 序号比较，跨窗口和重启仍然成立；之后的任何新变更
都会使先前证据失效。

## 工作区编辑器的原始文件协议（HTTP）

- `POST /api/workspace/local/raw/read`：返回文件的**完整原文**和 revision
  （无模型摘录头、无截断；超过大小上限或二进制文件被拒绝）。
- `POST /api/workspace/local/raw`：全量保存，**必须携带** read 返回的
  revision；旧 revision 拒绝，新文件 `O_EXCL` 创建，写入原子化。绝不把
  截断的文件保存回磁盘。
- `POST /api/workspace/local`：只提供 list/status/diff/read/search；没有任意命令执行的 HTTP 端点。
- `GET /api/workspace/capabilities` 的 execution_environment 诚实返回 native 或 container；Docker 部署里的 local 是容器内文件系统，除非操作员显式挂载。
