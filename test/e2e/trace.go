//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// intentText is the registry the cases reference. A case never embeds its own
// wording: the verdict has to name a promise, not an assertion, so that a red
// can be read as "this interface does not do what it says".
var intentText = map[string]string{
	"I-AUTH-1":       "非回环监听必须要求鉴权（无 token 拒绝启动）",
	"I-AUTH-2":       "带 token 时未授权请求被拒，且不产生任何执行",
	"I-RUN-1":        "一次请求必然产出一个可判定的回合结果",
	"I-RUN-2":        "空输入、坏 JSON 不得进入执行",
	"I-RUN-3":        "同一会话的连续请求携带先前的用户输入（会话延续）",
	"I-RUN-4":        "GET 读资源、POST 改状态，读请求不产生执行",
	"I-TURN-1":       "同一会话多轮问答：每轮答复只是本轮自己的",
	"I-TURN-2":       "历史输入在上下文里各出现一次（不重复、不丢）",
	"I-TURN-3":       "早前回合的工具结果在后续回合仍可见",
	"I-DISPATCH-1":   "派发不阻塞调用方回合（立即结束，不等下游）",
	"I-DISPATCH-2":   "派发结果回流，并成为用户可见的唯一结论",
	"I-DISPATCH-3":   "一次派发留下可查的执行记录（任务四段可串）",
	"I-DISPATCH-4":   "下游失败必须上报到回合，链路就此终止，原因可读",
	"I-DISPATCH-5":   "同一 team 不并发（一个 team 是一个执行单元）",
	"I-DISPATCH-6":   "不同 team 可并行",
	"I-DISPATCH-7":   "声明 per_task 的 team 一任务一实例：多个派发并发执行，互不占用",
	"I-TASK-1":       "派发创建的任务可按 id 查询状态",
	"I-TASK-2":       "取消作用到执行本体，且不再产出结论",
	"I-TASKSTREAM-1": "任务流在终态收尾并关闭",
	"I-CANCEL-1":     "取消结束该回合、不产出结论，且进程仍可服务下一回合",
	"I-RECOVERY-1":   "崩溃后能识别被中断的回合（可与“还在跑”区分）",
	"I-RECOVERY-2":   "被中断的执行不得被报告为成功结论",
	"I-APPROVAL-1":   "审批拒绝时工具不执行，且拒绝原因对调用方可见",
	"I-STREAM-3":     "订阅必须活到结论落地（派发后的结论帧不能丢）",
	"I-STREAM-4":     "回合结束后非 follow 订阅自行关闭，并以最新回合的终态收尾",
	"I-ERR-3":        "每个路径只接受声明的方法：写接口不接受 GET，读接口不接受 POST",
	"I-ERR-4":        "请求体超过上限回 413，不得当成 JSON 解析失败（400）",
	"I-QUEUE-1":      "会话在跑时收到的输入必须排队、不拒绝，并在当前回合结束后真的执行",
	"I-CHAIN-1":      "一条链可以经过三层 team：中间层继续下派，结论沿原路回到用户",
	"I-AUTH-3":       "health 与其余 API 一样需要 token，不向未授权方暴露部署状态",
	"I-LOOP-3":       "流式空帧与缺 usage 不得中断回合",
	"I-TOOL-1":       "工具调用落到真实执行，结果回注给模型并影响后续推理",
	"I-TOOL-2":       "脚本内的内层工具调用真实派发，且可归属到该脚本",
	"I-TOOL-3":       "工具失败对模型可见，不得静默当成功",
	"I-CTX-1":        "跨轮答复必须对模型可见（助手消息或声明的记录注入）",
	"I-STATUS-1":     "状态必须与真实等待对象一致",
	"I-STATUS-2":     "未知会话回 404，不伪造状态",
	"I-RESULT-1":     "结果接口返回当前会话的持久状态，可轮询",
	"I-STREAM-1":     "事件帧自带序号，可断点续订",
	"I-STREAM-2":     "断线续订不重不漏",
	"I-ERR-1":        "参数问题 400、资源不存在 404，按 HTTP 语义",
	"I-ERR-2":        "错误体必须说明问题（非空、含参数名或对象标识或原因）",
	"I-OBS-1":        "过程日志能定位到它所属的会话",
	"I-OBS-2":        "定位会话不依赖 debug 级别（生产默认级别即可定位）",
	"I-OBS-3":        "一次失败可仅凭产物定位",
}

// intentCatches says which defect class a case is built to expose. It is part of
// the report on purpose: a green case is only reassuring if a reader knows what
// it would have caught.
var intentCatches = map[string]string{
	"I-AUTH-1":       "无鉴权对外暴露（远程执行面）",
	"I-AUTH-2":       "鉴权形同虚设：401 之后仍执行了回合",
	"I-RUN-1":        "回合不终止 / 结论为空 / 未落事件",
	"I-RUN-2":        "脏输入进入执行，产生幽灵会话",
	"I-RUN-3":        "会话延续失效：第二轮丢历史",
	"I-RUN-4":        "读接口触发写副作用；索引与真实会话不一致",
	"I-TURN-1":       "答复跨轮串味（上一轮答复被当本轮结论）",
	"I-TURN-2":       "历史重复注入（提示词膨胀、模型被重复问题带偏）",
	"I-TURN-3":       "工具结果跨轮丢失（追问只能靠猜）",
	"I-DISPATCH-1":   "派发同步阻塞：一次用户请求被下游耗时绑住",
	"I-DISPATCH-2":   "结论重复或丢失：过程话术当结论、下游结论不入用户视野",
	"I-DISPATCH-3":   "派发无痕：事后无法证明派过、派给谁、派了什么",
	"I-DISPATCH-4":   "下游失败被静默：用户看到成功，链路继续重试",
	"I-DISPATCH-5":   "同 team 并发或任务滞留：一个 team 跑两轮 / 任务永远 running",
	"I-DISPATCH-6":   "并行退化：不同 team 排队执行，协调成本变成串行",
	"I-DISPATCH-7":   "per_task 退化成单例：第二个派发被判 already running，声明形同虚设",
	"I-TASK-1":       "任务状态不可查：客户端无法跟踪异步工作",
	"I-TASK-2":       "取消只改记录不停执行，取消后仍产出结论",
	"I-TASKSTREAM-1": "任务流不收敛：终态后仍挂着，客户端一直等",
	"I-TOOL-1":       "工具未真正执行或结果未回注（模型凭空作答）",
	"I-TOOL-2":       "脚本内层调用未派发 / 无法归属到脚本（审计断链）",
	"I-TOOL-3":       "工具失败被当成功，模型在错误前提上继续推理",
	"I-CTX-1":        "跨轮失忆：模型看不到自己上一轮说了什么",
	"I-STREAM-1":     "事件无序号或乱序，无法重放与断点续订",
	"I-STREAM-2":     "续订重复投递或漏帧（客户端重复渲染/丢结论）",
	"I-STREAM-3":     "订阅提前关闭，派发后的结论永远收不到",
	"I-STREAM-4":     "连接永不关闭（客户端只能猜回合结束）；或订阅只回放最早回合，看到的是旧答案",
	"I-LOOP-3":       "流式空帧/缺 usage 被当成结束（空答复、成本统计为零）",
	"I-STATUS-1":     "状态与真实等待对象不一致（说等用户实际在等工具）",
	"I-STATUS-2":     "未知会话伪造状态，客户端误判会话仍可用",
	"I-RESULT-1":     "轮询与推送结论不一致，两条读取路径各自为政",
	"I-CANCEL-1":     "取消不生效或取消后会话不可用",
	"I-RECOVERY-1":   "崩溃后无法区分“还在跑”与“已中断”",
	"I-RECOVERY-2":   "中断的执行被当成成功结论交付给用户",
	"I-APPROVAL-1":   "拒绝仍执行 / 拒绝原因不告知（用户不知道为什么被拦）",
	"I-ERR-1":        "错误码混用（找不到当参数错），客户端重试策略走偏",
	"I-ERR-2":        "错误体无信息（只回 EOF/状态码），无法自助排查",
	"I-ERR-3":        "读请求触发写副作用：GET 一个 cancel/turn 路径就执行了动作",
	"I-ERR-4":        "超大请求体把引擎内存顶穿，或被误报成请求格式错误",
	"I-QUEUE-1":      "第二条输入被拒或被丢；排队后永远不执行（用户看到“已收到”却没有下文）",
	"I-AUTH-3":       "未授权方读得到部署状态（会话数、待唤醒数）",
	"I-OBS-1":        "日志无法关联会话，排查只能靠时间戳猜",
	"I-OBS-2":        "生产级别无定位信息，线上问题只能复现",
	"I-OBS-3":        "失败无证据，缺陷只能靠复跑才说得清",
}

// caseRecord is one row of cases.jsonl: what ran, which promise it judged, and
// what the run concluded.
type caseRecord struct {
	Case    string `json:"case"`
	Intent  string `json:"intent"`
	Promise string `json:"promise"`
	Catches string `json:"catches"`
	Layer   string `json:"failing_layer"`
	Verdict string `json:"verdict"`
	Detail  string `json:"detail,omitempty"`
	At      string `json:"at"`
}

var (
	recordMu sync.Mutex
	records  []caseRecord
)

// judge registers the case's verdict with the run report. Call it first in a
// case: the cleanup runs after the body regardless of how it ended.
func judge(t *testing.T, intent, layer string) {
	t.Helper()
	if _, ok := intentText[intent]; !ok {
		t.Fatalf("intent %q is not registered in contract.go", intent)
	}
	t.Cleanup(func() {
		verdict := "满足"
		if t.Failed() {
			verdict = "未满足"
		}
		recordMu.Lock()
		records = append(records, caseRecord{
			Case:    t.Name(),
			Intent:  intent,
			Promise: intentText[intent],
			Catches: intentCatches[intent],
			Layer:   layer,
			Verdict: verdict,
			At:      time.Now().UTC().Format(time.RFC3339),
		})
		recordMu.Unlock()
	})
}

// writeTrace persists the wiring between one HTTP call and the events it caused.
// A failure without this file is not a diagnosis, it is a guess.
func writeTrace(t *testing.T, e *env, httpCall httpTrace, sessionID string, extra map[string]any) string {
	t.Helper()
	dir := filepath.Join(runDir(t), "artifacts", sanitize(e.Case))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Logf("create artifact dir: %v", err)
		return ""
	}

	trace := map[string]any{
		"case":            e.Case,
		"intent":          e.Intent,
		"http":            httpCall,
		"flow_session_id": sessionID,
		"evidence":        map[string]any{},
	}
	if sessionID != "" {
		events := readEventsIn(t, filepath.Join(e.Root, ".agents", "data", "sessions", sessionID))
		files := map[string]any{}
		for _, ev := range events {
			entry, _ := files[ev.File].(map[string]any)
			if entry == nil {
				entry = map[string]any{"lines": []int{}}
			}
			lines, _ := entry["lines"].([]int)
			entry["lines"] = append(lines, ev.Line)
			files[ev.File] = entry
		}
		trace["evidence"] = map[string]any{"session": files, "event_count": len(events)}
	}
	for key, value := range extra {
		trace[key] = value
	}

	data := mustJSON(trace)
	path := filepath.Join(dir, "trace-index.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Logf("write trace: %v", err)
	}
	t.Logf("trace: %s", path)
	return path
}

type httpTrace struct {
	Method     string `json:"method"`
	Path       string `json:"path"`
	Status     int    `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	BodyLen    int    `json:"body_len"`
}

// runDir is the single directory this process writes results into.
func runDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("HERON_E2E_RUN_DIR")
	if dir == "" {
		dir = artifactRoot(t)
		_ = os.Setenv("HERON_E2E_RUN_DIR", dir)
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// finalizeReport writes cases.jsonl and summary.md. Called from TestMain after
// the run so the report exists even when every case fails.
func finalizeReport() {
	dir := os.Getenv("HERON_E2E_RUN_DIR")
	if dir == "" {
		return
	}
	recordMu.Lock()
	rows := append([]caseRecord(nil), records...)
	recordMu.Unlock()
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Case < rows[j].Case })

	var jsonl strings.Builder
	for _, row := range rows {
		data, _ := json.Marshal(row)
		jsonl.Write(data)
		jsonl.WriteString("\n")
	}
	_ = os.WriteFile(filepath.Join(dir, "cases.jsonl"), []byte(jsonl.String()), 0o644)

	passed, failed := 0, 0
	for _, row := range rows {
		if row.Verdict == "满足" {
			passed++
			continue
		}
		failed++
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# heron-ai e2e 接口结论\n\n共 %d 条：满足 %d，未满足 %d。\n\n", len(rows), passed, failed)
	md.WriteString("| 用例 | 意图 | 承诺 | 能发现的问题 | 结论 | 失败层 |\n|---|---|---|---|---|---|\n")
	for _, row := range rows {
		fmt.Fprintf(&md, "| %s | %s | %s | %s | %s | %s |\n", row.Case, row.Intent, row.Promise, row.Catches, row.Verdict, row.Layer)
	}
	if failed > 0 {
		md.WriteString("\n## 未满足（缺陷候选）\n\n")
		for _, row := range rows {
			if row.Verdict == "满足" {
				continue
			}
			fmt.Fprintf(&md, "- %s：%s 未兑现（层：%s）。证据：`artifacts/%s/`\n", row.Intent, row.Promise, row.Layer, sanitize(row.Case))
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "summary.md"), []byte(md.String()), 0o644)
}
