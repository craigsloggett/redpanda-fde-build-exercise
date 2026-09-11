package main

import (
	"testing"
	"time"
)

func TestSince(t *testing.T) {
	tests := []struct {
		age  time.Duration
		want string
	}{
		{age: -5 * time.Second, want: "now"},
		{age: 0, want: "now"},
		{age: 400 * time.Millisecond, want: "now"},
		{age: 500 * time.Millisecond, want: "1 second ago"},
		{age: time.Second, want: "1 second ago"},
		{age: 1500 * time.Millisecond, want: "2 seconds ago"},
		{age: 59 * time.Second, want: "59 seconds ago"},
		{age: time.Minute, want: "1 minute ago"},
		{age: 119 * time.Second, want: "1 minute ago"},
		{age: 2 * time.Minute, want: "2 minutes ago"},
		{age: 59*time.Minute + 59*time.Second, want: "59 minutes ago"},
		{age: time.Hour, want: "1 hour ago"},
		{age: 2*time.Hour - time.Second, want: "1 hour ago"},
		{age: 2 * time.Hour, want: "2 hours ago"},
		{age: 24*time.Hour - time.Second, want: "23 hours ago"},
		{age: 24 * time.Hour, want: "yesterday"},
		{age: 48*time.Hour - time.Second, want: "yesterday"},
		{age: 48 * time.Hour, want: "2 days ago"},
		{age: 30 * 24 * time.Hour, want: "30 days ago"},
	}

	for _, test := range tests {
		if got := since(test.age); got != test.want {
			t.Errorf("since(%v) = %q, want %q", test.age, got, test.want)
		}
	}
}
