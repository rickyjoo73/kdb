package kdbadmin

import (
	"io"
	"testing"
	"time"
)

// 신규/변경 템플릿이 대표 데이터로 실행 에러 없이 렌더되는지 확인(파싱은 startup 에서
// 보장되지만 필드 참조 오류는 실행 시점에만 드러남).
func renderSmokeServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{opts: Options{SessionSecret: []byte("test-secret-test-secret-0123456789")}}
	s.loadTemplates()
	return s
}

func TestOndemandQueueRenders(t *testing.T) {
	s := renderSmokeServer(t)
	now := time.Now()
	data := map[string]any{
		"title": "온디맨드 발굴 큐",
		"items": []rqRow{
			{Ko: "봉준호", Type: "person", QStatus: "done", Attempts: 1, CreatedAt: now, FinishedAt: &now, EntityStatus: "active", Outcome: "해소됨 · active", OutcomeClass: "ok"},
			{Ko: "무명곡", Type: "unknown", QStatus: "failed", Attempts: 3, LastError: "no signal", CreatedAt: now, EntityStatus: "", Outcome: "실패", OutcomeClass: "fail"},
		},
		"p":            page{Limit: 50, Total: 2, StartIndex: 1, EndIndex: 2, PageNo: 1, TotalPages: 1, Extras: map[string]string{}},
		"statusFilter": "",
		"qPending":     int64(1), "qProgress": int64(8), "qDone": int64(2644), "qFailed": int64(0),
		"outActive": int64(1495), "outCand": int64(166), "outRej": int64(736), "outNone": int64(256),
		"page": "/admin/ondemand/queue",
	}
	if err := s.tmpl.ExecuteTemplate(io.Discard, "ondemand_queue.html", data); err != nil {
		t.Fatalf("ondemand_queue render: %v", err)
	}
}

func TestOndemandConsumersRenders(t *testing.T) {
	s := renderSmokeServer(t)
	now := time.Now()
	data := map[string]any{
		"title": "소비자 · kstory 대시보드",
		"items": []ondemandConsumerRow{
			{Label: "mediafine", Active: true, Req7d: 3218, Miss7d: 15, MissPct: 0, LastUsed: &now, TopPath: "/v1/entities/match"},
			{Label: "kstory", Active: true, Req7d: 0, Miss7d: 0, MissPct: 0, LastUsed: nil, TopPath: ""},
		},
		"totalConsumers": int64(4), "activeConsumers": int64(4),
		"req7d": int64(3268), "miss7d": int64(16), "missPct": 0,
		"page": "/admin/ondemand/consumers",
	}
	if err := s.tmpl.ExecuteTemplate(io.Discard, "ondemand_consumers.html", data); err != nil {
		t.Fatalf("ondemand_consumers render: %v", err)
	}
}
