package service

import (
	"errors"
	"sort"
	"testing"
)

func TestShuffleIsAPermutation(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 10, 257} {
		xs := make([]int, n)
		for i := range xs {
			xs[i] = i
		}
		if err := shuffle(n, cryptoIntn, func(i, j int) { xs[i], xs[j] = xs[j], xs[i] }); err != nil {
			t.Fatal(err)
		}
		sorted := append([]int(nil), xs...)
		sort.Ints(sorted)
		for i, v := range sorted {
			if v != i {
				t.Fatalf("n=%d: not a permutation: %v", n, xs)
			}
		}
	}
}

func TestShuffleUsesInjectedSource(t *testing.T) {
	xs := []int{0, 1, 2, 3}
	zero := func(int) (int, error) { return 0, nil } // always swap with position 0
	if err := shuffle(len(xs), zero, func(i, j int) { xs[i], xs[j] = xs[j], xs[i] }); err != nil {
		t.Fatal(err)
	}
	if want := []int{1, 2, 3, 0}; !equal(xs, want) {
		t.Errorf("xs = %v, want %v", xs, want)
	}

	boom := errors.New("entropy exhausted")
	if err := shuffle(3, func(int) (int, error) { return 0, boom }, func(i, j int) {}); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCryptoIntnRange(t *testing.T) {
	seen := map[int]bool{}
	for i := 0; i < 2000; i++ {
		v, err := cryptoIntn(7)
		if err != nil || v < 0 || v >= 7 {
			t.Fatalf("cryptoIntn(7) = %d, %v", v, err)
		}
		seen[v] = true
	}
	if len(seen) != 7 {
		t.Errorf("only %d of 7 values seen", len(seen))
	}
}

// A loose uniformity check: each of 3 items lands in each position about a third of the time.
func TestShuffleRoughlyUniform(t *testing.T) {
	const trials = 30000
	var pos [3][3]int
	for i := 0; i < trials; i++ {
		xs := []int{0, 1, 2}
		if err := shuffle(3, cryptoIntn, func(i, j int) { xs[i], xs[j] = xs[j], xs[i] }); err != nil {
			t.Fatal(err)
		}
		for p, v := range xs {
			pos[v][p]++
		}
	}
	for v := range pos {
		for p, n := range pos[v] {
			if n < trials/3*9/10 || n > trials/3*11/10 {
				t.Errorf("item %d at position %d: %d of %d (expected ~%d)", v, p, n, trials, trials/3)
			}
		}
	}
}
