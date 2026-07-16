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

func TestOndemandArticlesRenders(t *testing.T) {
	s := renderSmokeServer(t)
	now := time.Now()
	data := map[string]any{
		"title": "기사별 요청",
		"items": []*articleRow{
			{SourceURL: "https://kstory.aiinplanet.com/k-pop/7-7-x", Host: "kstory.aiinplanet.com", Path: "/k-pop/7-7-x", LastAt: now, KwCount: 3, AmbigCount: 2, Kws: []articleKw{
				{Ko: "니쥬", ReqType: "group", EntityID: "11111111-1111-1111-1111-111111111111", EntityType: "group", EntStatus: "active", Tier: "evidenced", Ambig: "확정", AmbigClass: "ok"},
				{Ko: "있지", ReqType: "group", EntityID: "22222222-2222-2222-2222-222222222222", EntityType: "group", EntStatus: "active", Tier: "unverified", Ambig: "동명이인 2", AmbigClass: "ambig"},
				{Ko: "무명곡", ReqType: "unknown", EntityID: "", EntityType: "", EntStatus: "", Tier: "", Ambig: "미해소 · 미생성", AmbigClass: "none"},
			}},
		},
		"p":             page{Limit: 50, Total: 1, StartIndex: 1, EndIndex: 1, PageNo: 1, TotalPages: 1, Extras: map[string]string{}},
		"totalArticles": int64(16), "totalKw": int64(219),
		"page": "/admin/ondemand/articles",
	}
	if err := s.tmpl.ExecuteTemplate(io.Discard, "ondemand_articles.html", data); err != nil {
		t.Fatalf("ondemand_articles render: %v", err)
	}
}

func TestOndemandRequestsRenders(t *testing.T) {
	s := renderSmokeServer(t)
	now := time.Now()
	data := map[string]any{
		"title": "요청 내용 · API 본문",
		"items": []requestGroupRow{
			{At: now, Consumer: "consumer:abc123", Origin: "prepare",
				SourceURL: "https://kstory.aiinplanet.com/k-pop/7-13-x", Host: "kstory.aiinplanet.com", Path: "/k-pop/7-13-x",
				Terms: []reqTermChip{
					{Ko: "박보검", Type: "person", Status: "ready", StatusClass: "ok", HasContext: true},
					{Ko: "무빙 시즌2", Type: "drama", Status: "preparing", StatusClass: "cand"},
					{Ko: "qwerty123", Type: "", Status: "out_of_scope", StatusClass: "rej"},
				}},
			{At: now, Consumer: "anon", Origin: "lookup", SourceURL: "",
				Terms: []reqTermChip{{Ko: "도깨비", Type: "drama", Status: "found", StatusClass: "ok"}}},
		},
		"p":           page{Limit: 50, Total: 2, StartIndex: 1, EndIndex: 2, PageNo: 1, TotalPages: 1, Extras: map[string]string{}},
		"totalGroups": int64(2), "totalTerms": int64(4), "terms24h": int64(4),
		"page": "/admin/ondemand/requests",
	}
	if err := s.tmpl.ExecuteTemplate(io.Discard, "ondemand_requests.html", data); err != nil {
		t.Fatalf("ondemand_requests render: %v", err)
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

func TestVerificationRenders(t *testing.T) {
	s := renderSmokeServer(t)
	now := time.Now()
	data := map[string]any{
		"title": "검증 tier · 정체성",
		"items": []verifRow{
			{ID: "11111111-1111-1111-1111-111111111111", Ko: "무명배우", Type: "person", Tier: "unverified", Evidence: "no independent anchor", Confidence: 0.62, UpdatedAt: now},
			{ID: "22222222-2222-2222-2222-222222222222", Ko: "봉준호", Type: "person", Tier: "authoritative", Evidence: "tmdb+wikidata", Confidence: 0.95, UpdatedAt: now},
		},
		"typeRows": []verifTypeRow{
			{Type: "person", Total: 2599, Authoritative: 1800, Evidenced: 500, Unverified: 299},
			{Type: "drama", Total: 489, Authoritative: 420, Evidenced: 50, Unverified: 19},
		},
		"p":          page{Limit: 50, Total: 669, StartIndex: 1, EndIndex: 50, PageNo: 1, TotalPages: 14, Extras: map[string]string{"tier": "unverified"}},
		"tierFilter": "unverified",
		"cAuth":      int64(3125), "cEvid": int64(770), "cUnver": int64(669), "cNone": int64(0),
		"verifiedPct":  85,
		"lastVerified": &now,
		"page":         "/admin/quality/verification",
	}
	if err := s.tmpl.ExecuteTemplate(io.Discard, "verification.html", data); err != nil {
		t.Fatalf("verification render: %v", err)
	}
}

func TestDashboardRenders(t *testing.T) {
	s := renderSmokeServer(t)
	data := map[string]any{
		"title": "운영 개요", "dbErr": "",
		"entities": int64(4564), "persons": int64(2600),
		"vAuth": int64(3125), "vEvid": int64(773), "vUnver": int64(666), "vTotal": int64(4564),
		"officialPct": 85, "qPending": int64(3), "slaOverPct": 12, "done24h": int64(40), "processHealthy": true,
		"inbox": inboxCounts{
			NewCandidates: 5, Corrections: 2, LowQuality: 8, Conflicts: 1, LocaleGaps: 12,
			ClientReq7d: 3200, DiscoveryDone7d: 44, CandOldestH: 10,
		},
		"actionTotal":    int64(28),
		"entityProgress": []localeProgress{},
		"personProgress": []localeProgress{},
	}
	if err := s.tmpl.ExecuteTemplate(io.Discard, "dashboard.html", data); err != nil {
		t.Fatalf("dashboard render: %v", err)
	}
}

func TestWorkflowBoardRenders(t *testing.T) {
	s := renderSmokeServer(t)
	data := map[string]any{
		"title": "워크플로우 보드", "page": "/admin/workflow",
		"tiles": []wfTile{
			{Label: "유입 24h", Value: "560", Class: "text-slate-800", Href: "/admin/ondemand/queue"},
			{Label: "2분 SLA 초과율", Value: "12%", Class: "text-slate-800", Href: "/admin/ops/health"},
		},
		"stages": []*wfStage{
			{Key: "INTAKE", Label: "① 유입 대기", CountLabel: "대기", Accent: "bg-sky-50",
				EmptyText: "대기 0건"},
			{Key: "GATE", Label: "② 심사 게이트", CountLabel: "24h 보류", Accent: "bg-indigo-50",
				Count: 2, Backlog: 4467, BacklogLabel: "누적 보류", BacklogHref: "/admin/ondemand/queue",
				Items: []wfCard{
					{Ko: "신주쿠광고", Type: "unknown", Sub: "missing_or_unsupported_type", Age: "2시간", AgeClass: "ok", Href: "/admin/ondemand/queue?q=신주쿠광고"},
				}, More: 1, MoreHref: "/admin/ondemand/queue"},
			{Key: "FILL", Label: "④ 다국어 채움", CountLabel: "48h 진행", Accent: "bg-fuchsia-50",
				Count: 1, Items: []wfCard{
					{Ko: "돌리도", Type: "song_album", Sub: "빈칸: ja·vi", Age: "40분", AgeClass: "ok", Href: "/admin/entities/x"},
				}},
		},
		"issues": []wfIssue{
			{Title: "15분+ 정체", Count: 3, Detail: "워커 점검 필요", Href: "/admin/ondemand/queue", Severity: "red"},
			{Title: "en 빈칸", Count: 188, Detail: "가드기각 잔여", Href: "/admin/entities/locale-gaps", Severity: "amber"},
		},
		"lastAutopilot": "12분 전", "lastResolve": "3분 전", "lastFill": "25분 전",
		"now": "21:30:00",
	}
	if err := s.tmpl.ExecuteTemplate(io.Discard, "workflow_board.html", data); err != nil {
		t.Fatalf("workflow board render: %v", err)
	}
	// 이슈 0건 분기(전 구간 정상)도 렌더돼야 한다.
	data["issues"] = []wfIssue{}
	if err := s.tmpl.ExecuteTemplate(io.Discard, "workflow_board.html", data); err != nil {
		t.Fatalf("workflow board render (no issues): %v", err)
	}
}

func TestOpsHealthRenders(t *testing.T) {
	s := renderSmokeServer(t)
	now := time.Now()
	data := map[string]any{
		"title": "운영 점검 · 처리/장애",
		"d": opsHealthData{
			QPending: 3, QProgress: 2, QFailed: 0, QDone: 2673,
			Done24h: 117, Over24h: 74, AvgSec: 210, P50Sec: 144, OverPct: 63,
			New7d: 525, Official7d: 309, Evidenced7d: 157, Unverified7d: 59, OfficialPct: 88,
			Failed7d:     0,
			LastBackupAt: &now, BackupAgeH: 3, BackupSizeMB: 41,
			Sources: []sourceRow{
				{Name: "naver", Calls: 120, DayCalls: 120, Errors: 1, TooMany: 0, ErrPct: 0, Quota: 1000, QuotaPct: 12, LastErr: "http 429", LastErrAt: &now},
				{Name: "searxng", Calls: 340, DayCalls: 340, Errors: 12, TooMany: 3, ErrPct: 3},
			},
			Alerts: []opsAlert{{Level: "warn", Msg: "느린 채움: 24h 발굴의 63%가 120초 초과."}},
		},
		"sla":  120,
		"page": "/admin/ops/health",
	}
	if err := s.tmpl.ExecuteTemplate(io.Discard, "ops_health.html", data); err != nil {
		t.Fatalf("ops_health render: %v", err)
	}
}
