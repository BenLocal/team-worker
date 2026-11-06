package model

import (
	"testing"
	"time"
)

func TestAutoReviewActive(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	at := func(hhmm string) time.Time {
		c, err := ParseClock(hhmm)
		if err != nil {
			t.Fatal(err)
		}
		return time.Date(2026, 11, 6, c/60, c%60, 30, 0, time.UTC)
	}
	day := AutoReview{Enabled: true, From: "09:00", To: "18:00", TimeZone: "UTC"}
	night := AutoReview{Enabled: true, From: "22:00", To: "08:00", TimeZone: "UTC"}
	// 22:00-08:00 in Shanghai (UTC+8) is 14:00-00:00 UTC.
	shanghaiNight := AutoReview{Enabled: true, From: "22:00", To: "08:00", TimeZone: "Asia/Shanghai"}
	tests := []struct {
		name string
		a    AutoReview
		now  time.Time
		want bool
	}{
		{"disabled", AutoReview{}, at("12:00"), false},
		{"disabled with a window", AutoReview{From: "09:00", To: "18:00"}, at("12:00"), false},
		{"no window", AutoReview{Enabled: true}, at("03:00"), true},
		{"same day inside", day, at("12:00"), true},
		{"same day at the start", day, at("09:00"), true},
		{"same day just before the start", day, at("08:59"), false},
		{"same day just before the end", day, at("17:59"), true},
		{"same day at the end", day, at("18:00"), false},
		{"same day after", day, at("20:00"), false},
		{"cross midnight before midnight", night, at("23:30"), true},
		{"cross midnight after midnight", night, at("02:00"), true},
		{"cross midnight at the start", night, at("22:00"), true},
		{"cross midnight at the end", night, at("08:00"), false},
		{"cross midnight in the day", night, at("12:00"), false},
		{"time zone inside", shanghaiNight, at("15:00"), true},
		{"time zone at the end", shanghaiNight, at("00:00"), false},
		{"time zone outside", shanghaiNight, at("03:00"), false},
		{"time zone of now ignored", shanghaiNight, at("15:00").In(shanghai), true},
		{"bad time zone", AutoReview{Enabled: true, From: "09:00", To: "18:00", TimeZone: "Nowhere/City"}, at("12:00"), false},
		{"half a window", AutoReview{Enabled: true, From: "09:00"}, at("12:00"), false},
	}
	for _, tt := range tests {
		if got := tt.a.Active(tt.now); got != tt.want {
			t.Errorf("%s: Active(%s) = %v, want %v", tt.name, tt.now.Format(time.RFC3339), got, tt.want)
		}
	}
}

func TestParseClock(t *testing.T) {
	for in, want := range map[string]int{"00:00": 0, "08:05": 485, "23:59": 1439} {
		if got, err := ParseClock(in); err != nil || got != want {
			t.Errorf("ParseClock(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "8:05", "24:00", "12:60", "12:5", "1200", "12:00:00", " 12:00"} {
		if _, err := ParseClock(in); err == nil {
			t.Errorf("ParseClock(%q) accepted", in)
		}
	}
}
