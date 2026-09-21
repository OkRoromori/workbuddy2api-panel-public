package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OkRoromori/workbuddy2api-panel-public/internal/auth"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/pool"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/upstream"
)

// growthEnvelope 造上游 growth 任务列表信封（与线上同形：code / msg / data.tasks）。
func growthEnvelope(codes ...string) string {
	tasks := make([]map[string]any, 0, len(codes))
	for _, c := range codes {
		tasks = append(tasks, map[string]any{"task_code": c})
	}
	b, _ := json.Marshal(map[string]any{
		"code": 0, "msg": "OK",
		"data": map[string]any{"tasks": tasks},
	})
	return string(b)
}

// TestMergeMPPending 小程序口径待办的合并规则：按 task_code 去重、只收
// "未完成且可自动化"的任务、mp 列表失败时静默原样返回。
func TestMergeMPPending(t *testing.T) {
	t.Run("合并 mp 口径：去重 + 只取可自动化", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("X-Client-Platform"); got != "miniprogram" {
				t.Errorf("mp 口径请求必须带 X-Client-Platform: miniprogram，实际 %q", got)
			}
			if r.URL.Path != "/v2/activity/growth/tasks" {
				t.Errorf("路径 = %s want /v2/activity/growth/tasks", r.URL.Path)
			}
			// chat_5 已由默认口径给出（必须去重）；Expert_Philanthropy 不可自动化（不进队）
			w.Write([]byte(growthEnvelope("chat_5", "school_season", "Sequential_Tasks_1", "Expert_Philanthropy")))
		}))
		defer srv.Close()

		p := New(Config{
			Version: "test", APIKey: "k",
			Upstream: &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL},
		})
		got := p.mergeMPPending(&auth.Auth{AccessToken: "at", UID: "u1"}, []upstream.Task{{TaskCode: "chat_5"}})

		codes := make([]string, 0, len(got))
		for _, x := range got {
			codes = append(codes, x.TaskCode)
		}
		want := []string{"chat_5", "school_season", "Sequential_Tasks_1"}
		if strings.Join(codes, ",") != strings.Join(want, ",") {
			t.Errorf("合并结果 = %v want %v", codes, want)
		}
	})

	t.Run("mp 列表失败：静默原样返回", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"code":500,"msg":"boom"}`))
		}))
		defer srv.Close()

		p := New(Config{
			Version: "test", APIKey: "k",
			Upstream: &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL},
		})
		got := p.mergeMPPending(&auth.Auth{AccessToken: "at", UID: "u1"}, []upstream.Task{{TaskCode: "chat_5"}})
		if len(got) != 1 || got[0].TaskCode != "chat_5" {
			t.Errorf("mp 失败时应原样返回，实际 %+v", got)
		}
	})
}

// TestRunQueueMergesMPPending 端到端钉死回归：待办只存在于 mp 口径时，
// 执行队列也必须建队。
//
// 这正是「扫描显示 school_season 待办，点『执行全部待办』却报无可执行」的症状——
// 扫描侧合并了 mp 口径、执行队列侧漏合并。两处必须共用 mergeMPPending，
// 只测合并函数本身挡不住"某一处忘了调用"。
func TestRunQueueMergesMPPending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Client-Platform") == "miniprogram" {
			w.Write([]byte(growthEnvelope("Sequential_Tasks_1")))
			return
		}
		w.Write([]byte(growthEnvelope())) // 默认口径：没有待办
	}))
	defer srv.Close()

	pl := pool.New("")
	pl.Add(&auth.Auth{AccessToken: "at", UID: "u1", Nickname: "n1"})
	p := New(Config{
		Version: "test", APIKey: "k", Pool: pl,
		Upstream: &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL},
	})

	req := httptest.NewRequest("POST", "/panel/api/tasks/run_queue",
		strings.NewReader(`{"growth":true,"school":false,"concurrency":1}`))
	req.Header.Set("Authorization", "Bearer k")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Started bool `json:"started"`
		Total   int  `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if !out.Started || out.Total != 1 {
		t.Errorf("只有 mp 口径待办时也必须建队：started=%v total=%d body=%s",
			out.Started, out.Total, rec.Body.String())
	}
}
