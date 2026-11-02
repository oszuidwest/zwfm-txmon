package main

import "testing"

func TestParseMHz(t *testing.T) {
	for in, want := range map[string]int{"98.5": 98500, "98.05": 98050, "108.1": 108100, "87.50": 87500,
		"98.500": -1, "9.85e1": -1, "NaN": -1, "98.": -1, "+98.5": -1} {
		got, ok := parseMHz(in)
		if !ok {
			got = -1
		}
		if got != want {
			t.Errorf("parseMHz(%q) = %d, want %d", in, got, want)
		}
	}
}
