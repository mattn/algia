package main

import "testing"

func TestIncludeKind(t *testing.T) {
	tests := []struct {
		name       string
		kinds      []int
		candidates []int
		want       bool
	}{
		{"empty kinds", nil, []int{1}, false},
		{"empty candidates", []int{1}, nil, false},
		{"hit single", []int{1}, []int{1}, true},
		{"hit among many candidates", []int{1}, []int{4, 1059, 1}, true},
		{"miss", []int{0, 3}, []int{1, 4}, false},
		{"first kind matches second candidate", []int{42}, []int{1, 42}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := includeKind(tt.kinds, tt.candidates...); got != tt.want {
				t.Errorf("got=%v want=%v", got, tt.want)
			}
		})
	}
}
