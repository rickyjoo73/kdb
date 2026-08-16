package main

import (
	"testing"
	"time"
)

// TestNightWindowDefault — 기본 창은 00:00–05:00 KST(오너 지시 2026-08-16).
func TestNightWindowDefault(t *testing.T) {
	t.Setenv("KDB_NIGHT_START_HOUR", "")
	t.Setenv("KDB_NIGHT_END_HOUR", "")
	s, e := nightWindow()
	if s != 0 || e != 5 {
		t.Errorf("기본 창 = %d–%d; want 0–5", s, e)
	}
}

// TestNightWindowEnvOverride — env 로 창을 옮길 수 있어야 한다(서버 유휴시간이 바뀔 수 있다).
func TestNightWindowEnvOverride(t *testing.T) {
	t.Setenv("KDB_NIGHT_START_HOUR", "1")
	t.Setenv("KDB_NIGHT_END_HOUR", "6")
	if s, e := nightWindow(); s != 1 || e != 6 {
		t.Errorf("창 = %d–%d; want 1–6", s, e)
	}
}

// TestNightWindowRejectsInverted — end <= start 는 무한/음수 창이 된다. 최소 1시간으로 고친다.
func TestNightWindowRejectsInverted(t *testing.T) {
	t.Setenv("KDB_NIGHT_START_HOUR", "5")
	t.Setenv("KDB_NIGHT_END_HOUR", "2")
	s, e := nightWindow()
	if e <= s {
		t.Errorf("역전된 창을 그대로 받았다: %d–%d", s, e)
	}
	if e != 6 {
		t.Errorf("창 = %d–%d; want 5–6(최소 1시간)", s, e)
	}
}

// TestInNightWindow — 창 안/밖 판정과 남은 시간.
//
// 남은 시간이 틀리면 ctx 마감이 틀어져 (a) 창을 넘겨 낮에도 돌거나 (b) 시작하자마자
// 끊긴다. 경계값을 명시로 박아둔다.
func TestInNightWindow(t *testing.T) {
	t.Setenv("KDB_NIGHT_START_HOUR", "0")
	t.Setenv("KDB_NIGHT_END_HOUR", "5")
	at := func(h, m int) time.Time {
		return time.Date(2026, 8, 16, h, m, 0, 0, kstZone)
	}
	cases := []struct {
		name   string
		now    time.Time
		wantIn bool
		remain time.Duration
	}{
		{"창 시작 정각", at(0, 0), true, 5 * time.Hour},
		{"창 중간", at(2, 30), true, 2*time.Hour + 30*time.Minute},
		{"창 마지막 분", at(4, 59), true, time.Minute},
		{"창 끝 정각은 밖", at(5, 0), false, 0},
		{"창 시작 1분 전은 밖", at(23, 59), false, 0},
		{"한낮은 밖", at(13, 0), false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in, remain := inNightWindow(c.now)
			if in != c.wantIn {
				t.Fatalf("in = %v; want %v", in, c.wantIn)
			}
			if in && remain != c.remain {
				t.Errorf("남은 시간 = %v; want %v", remain, c.remain)
			}
		})
	}
}

// TestNightIdleRoundsNotOne — 무진전 1회로 레인을 끝내면 외부 API 일시 장애에
// 밤 작업이 통째로 조기 종료된다. 이 상수가 1 로 내려가면 실패한다.
func TestNightIdleRoundsNotOne(t *testing.T) {
	if nightIdleRounds < 2 {
		t.Errorf("nightIdleRounds = %d — 1 이면 일시 장애 한 번에 레인이 죽는다", nightIdleRounds)
	}
}
